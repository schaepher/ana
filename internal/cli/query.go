package cli

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/schaepher/codeintel/internal/domain"
	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
	"go.uber.org/zap"
)

// MinConfidence 调用关系查询默认置信度阈值。
// 说明：CALLS 边来自 CodeGraph 角色（TD.md 5.1 表：置信度 0.8），
// 阈值取 0.8 才能覆盖调用边；TD.md 决策 10 的 0.85 会过滤掉全部调用边。
const MinConfidence = 0.8

// cmdQuery 实现 `codeintel query ...`。
func cmdQuery(args []string) int {
	logger := zap.L()
	logger.Debug("enter cmdQuery")
	defer logger.Debug("exit cmdQuery")
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "error: query 需要一个子命令（symbol/callers/callees/impact）")
		return 2
	}
	sub := args[0]
	rest := args[1:]

	// 手动解析 flags（flag 包遇到位置参数即停止，无法支持 "query symbol X --repo Y" 形式）
	repoPath, depth, positional := parseQueryFlags(rest)
	if len(positional) < 1 {
		fmt.Fprintf(os.Stderr, "error: 缺少符号参数\n")
		return 2
	}
	target := positional[0]

	abs, _, err := resolveRepo(repoPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	db, err := sqlite.Open(abs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()
	repo := sqlite.NewRepo(db)

	switch sub {
	case "symbol":
		return querySymbol(repo, target)
	case "trace":
		return queryTrace(repo, target)
	case "callers", "callees", "impact":
		d := depth
		if d <= 0 {
			switch sub {
			case "impact":
				d = 3
			default:
				d = 1
			}
		}
		return queryGraph(repo, sub, target, d)
	default:
		fmt.Fprintf(os.Stderr, "error: unknown query subcommand %q\n", sub)
		return 2
	}
}

// queryTrace 输出符号的数据流信息（TD.md 7.3 trace_data_flow）：
// 方法内路径（source_var -> ... -> sink_var）与跨方法 DATA_FLOWS_TO 边。
func queryTrace(repo *sqlite.Repo, input string) int {
	n, err := resolveSymbol(repo, input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	flows, facts, err := repo.GetDataFlows(n.ID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	fmt.Printf("数据流（%s）:\n", n.Name)
	if len(flows) == 0 && len(facts) == 0 {
		fmt.Println("  无数据流信息（需启用 Joern 构建）")
		return 0
	}
	for _, f := range flows {
		fmt.Printf("  方法内: %s\n", f)
	}
	for _, f := range facts {
		src := shortID(f.SourceID)
		dst := shortID(f.TargetID)
		srcVar, _ := f.Metadata["source"].(string)
		dstVar, _ := f.Metadata["sink"].(string)
		fmt.Printf("  跨方法: %s(%s) -> %s(%s)\n", src, srcVar, dst, dstVar)
	}
	return 0
}

// parseQueryFlags 手动解析 query 子命令的参数，支持 flags 与位置参数任意顺序。
func parseQueryFlags(args []string) (repoPath string, depth int, positional []string) {
	logger := zap.L()
	logger.Debug("enter parseQueryFlags")
	defer logger.Debug("exit parseQueryFlags")
	repoPath = "."
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--repo" && i+1 < len(args):
			repoPath = args[i+1]
			i++
		case strings.HasPrefix(a, "--repo="):
			repoPath = strings.TrimPrefix(a, "--repo=")
		case a == "--depth" && i+1 < len(args):
			depth, _ = strconv.Atoi(args[i+1])
			i++
		case strings.HasPrefix(a, "--depth="):
			depth, _ = strconv.Atoi(strings.TrimPrefix(a, "--depth="))
		case strings.HasPrefix(a, "-"):
			// 未知 flag：忽略
		default:
			positional = append(positional, a)
		}
	}
	return
}

// resolveSymbol 将用户输入解析为符号：canonical ID 直接命中，否则按名称查找。
func resolveSymbol(repo *sqlite.Repo, input string) (*domain.CodeEntity, error) {
	logger := zap.L()
	logger.Debug("enter resolveSymbol")
	defer logger.Debug("exit resolveSymbol")
	if strings.HasPrefix(input, "symbol:") || strings.HasPrefix(input, "file:") || strings.HasPrefix(input, "commit:") {
		n, err := repo.GetSymbol(domain.CanonicalID(input))
		if err == nil {
			return n, nil
		}
		if !errors.Is(err, domain.ErrNotFound) {
			return nil, err
		}
	}
	matches, err := repo.GetSymbolByName(input)
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("符号 %q 不存在", input)
	}
	if len(matches) > 1 {
		return nil, fmt.Errorf("符号 %q 有 %d 个匹配，请使用 canonical ID:\n  %s",
			input, len(matches), joinIDs(matches))
	}
	return matches[0], nil
}

func joinIDs(nodes []*domain.CodeEntity) string {
	logger := zap.L()
	logger.Debug("enter joinIDs")
	defer logger.Debug("exit joinIDs")
	ids := make([]string, 0, len(nodes))
	for _, n := range nodes {
		ids = append(ids, string(n.ID))
	}
	return strings.Join(ids, "\n  ")
}

// querySymbol 输出符号摘要（对齐 TD.md 7.1 explore_symbol 摘要层）。
func querySymbol(repo *sqlite.Repo, input string) int {
	logger := zap.L()
	logger.Debug("enter querySymbol")
	defer logger.Debug("exit querySymbol")
	n, err := resolveSymbol(repo, input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	fmt.Printf("ID:         %s\n", n.ID)
	fmt.Printf("名称:       %s\n", n.Name)
	fmt.Printf("种类:       %s\n", n.Kind)
	if n.FilePath != "" {
		fmt.Printf("文件:       %s", n.FilePath)
		if n.LineStart > 0 {
			fmt.Printf(":%d", n.LineStart)
		}
		fmt.Println()
	}
	if sig := n.Signature(); sig != "" {
		fmt.Printf("签名:       %s\n", sig)
	}
	if doc := n.DocComment(); doc != "" {
		fmt.Printf("文档:       %s\n", strings.Split(doc, "\n")[0])
	}
	callers, _ := repo.GetCallers(n.ID, 1, MinConfidence)
	callees, _ := repo.GetCallees(n.ID, 1, MinConfidence)
	fmt.Printf("调用者数:   %d\n", len(callers))
	fmt.Printf("被调用数:   %d\n", len(callees))

	// 详情层：列出调用者与被调用者（上限 50，TD.md 7.1）
	if len(callers) > 0 {
		fmt.Println("调用者:")
		printFacts(callers, "source", 50)
	}
	if len(callees) > 0 {
		fmt.Println("被调用者:")
		printFacts(callees, "target", 50)
	}
	return 0
}

// queryGraph 输出 callers/callees/impact 查询结果。
func queryGraph(repo *sqlite.Repo, sub, input string, depth int) int {
	logger := zap.L()
	logger.Debug("enter queryGraph")
	defer logger.Debug("exit queryGraph")
	n, err := resolveSymbol(repo, input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	switch sub {
	case "callers":
		facts, err := repo.GetCallers(n.ID, depth, MinConfidence)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		fmt.Printf("调用者（深度 %d，置信度 ≥%.2f）: %d 个\n", depth, MinConfidence, len(facts))
		printFacts(facts, "source", 100)
	case "callees":
		facts, err := repo.GetCallees(n.ID, depth, MinConfidence)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		fmt.Printf("被调用者（深度 %d，置信度 ≥%.2f）: %d 个\n", depth, MinConfidence, len(facts))
		printFacts(facts, "target", 100)
	case "impact":
		nodes, err := repo.GetImpact(n.ID, depth)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		fmt.Printf("影响范围（深度 %d）: %d 个节点\n", depth, len(nodes))
		printNodes(nodes)
	}
	return 0
}

// printFacts 打印边列表；endpoint 为 "source" 时显示边左端（调用者场景），
// 否则显示右端（被调用者场景）。
func printFacts(facts []*domain.Fact, endpoint string, limit int) {
	logger := zap.L()
	logger.Debug("enter printFacts")
	defer logger.Debug("exit printFacts")
	truncated := len(facts) > limit
	if truncated {
		facts = facts[:limit]
	}
	for _, f := range facts {
		id := f.SourceID
		if endpoint == "target" {
			id = f.TargetID
		}
		fmt.Printf("  %s  (%s, conf=%.2f)\n", shortID(id), f.ToolSource, f.Confidence)
	}
	if truncated {
		fmt.Printf("  ...（已截断，共 %d 条）\n", len(facts)+1)
	}
}

// shortID 压缩 canonical ID 显示：保留 pkg 末段与符号名。
func shortID(id domain.CanonicalID) string {
	logger := zap.L()
	logger.Debug("enter shortID")
	defer logger.Debug("exit shortID")
	s := string(id)
	prefix := "symbol:go:"
	if !strings.HasPrefix(s, prefix) {
		return s
	}
	rest := strings.TrimPrefix(s, prefix)
	if i := strings.LastIndex(rest, ":"); i >= 0 {
		pkg := rest[:i]
		name := rest[i+1:]
		if j := strings.LastIndex(pkg, "/"); j >= 0 {
			pkg = pkg[j+1:]
		}
		return pkg + ":" + name
	}
	return rest
}

// printNodes 打印节点列表。
func printNodes(nodes []*domain.CodeEntity) {
	logger := zap.L()
	logger.Debug("enter printNodes")
	defer logger.Debug("exit printNodes")
	sorted := make([]*domain.CodeEntity, len(nodes))
	copy(sorted, nodes)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Kind != sorted[j].Kind {
			return sorted[i].Kind < sorted[j].Kind
		}
		return sorted[i].Name < sorted[j].Name
	})
	for _, n := range sorted {
		loc := ""
		if n.FilePath != "" {
			loc = " " + n.FilePath
			if n.LineStart > 0 {
				loc += fmt.Sprintf(":%d", n.LineStart)
			}
		}
		fmt.Printf("  %s %s%s\n", n.Kind, n.Name, loc)
	}
}
