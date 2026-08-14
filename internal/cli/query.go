package cli

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/schaepher/codeintel/internal/action"
	"github.com/schaepher/codeintel/internal/domain"
	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
	"go.uber.org/zap"
)

// cmdQuery 实现 `codeintel query ...`。
func cmdQuery(args []string) int {
	logger := zap.L()
	logger.Debug("enter cmdQuery")
	defer logger.Debug("exit cmdQuery")
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "error: query 需要一个子命令（symbol/fields/trace-backward/trace-forward/value-trace/callers/callees/impact）")
		return 2
	}
	sub := args[0]
	rest := args[1:]

	// 手动解析 flags（flag 包遇到位置参数即停止，无法支持 "query symbol X --repo Y" 形式）
	f := parseQueryFlags(rest)
	if len(f.positional) < 1 {
		fmt.Fprintf(os.Stderr, "error: 缺少符号参数\n")
		return 2
	}
	target := f.positional[0]

	abs, _, err := resolveRepo(f.repoPath)
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
	acts := action.New(sqlite.NewRepo(db))

	switch sub {
	case "symbol":
		return querySymbol(acts, target)
	case "fields":
		return queryFields(acts, target)
	case "trace-backward", "trace-forward":
		return queryTraceDir(acts, target, f.funcPath, f.maxDepth, sub == "trace-forward")
	case "value-trace":
		return queryValueTrace(acts, target, f.maxDepth)
	case "callers", "callees", "impact":
		d := f.depth
		if d <= 0 {
			switch sub {
			case "impact":
				d = 3
			default:
				d = 1
			}
		}
		return queryGraph(acts, sub, target, d)
	default:
		fmt.Fprintf(os.Stderr, "error: unknown query subcommand %q\n", sub)
		return 2
	}
}

// queryFlags 是 query 子命令的手动解析结果。
type queryFlags struct {
	repoPath   string
	depth      int
	maxDepth   int
	funcPath   string
	positional []string
}

// parseQueryFlags 手动解析 query 子命令的参数，支持 flags 与位置参数任意顺序。
func parseQueryFlags(args []string) queryFlags {
	logger := zap.L()
	logger.Debug("enter parseQueryFlags")
	defer logger.Debug("exit parseQueryFlags")
	f := queryFlags{repoPath: "."}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--repo" && i+1 < len(args):
			f.repoPath = args[i+1]
			i++
		case strings.HasPrefix(a, "--repo="):
			f.repoPath = strings.TrimPrefix(a, "--repo=")
		case a == "--depth" && i+1 < len(args):
			f.depth, _ = strconv.Atoi(args[i+1])
			i++
		case strings.HasPrefix(a, "--depth="):
			f.depth, _ = strconv.Atoi(strings.TrimPrefix(a, "--depth="))
		case a == "--max-depth" && i+1 < len(args):
			f.maxDepth, _ = strconv.Atoi(args[i+1])
			i++
		case strings.HasPrefix(a, "--max-depth="):
			f.maxDepth, _ = strconv.Atoi(strings.TrimPrefix(a, "--max-depth="))
		case a == "--func" && i+1 < len(args):
			f.funcPath = args[i+1]
			i++
		case strings.HasPrefix(a, "--func="):
			f.funcPath = strings.TrimPrefix(a, "--func=")
		case strings.HasPrefix(a, "-"):
			// 未知 flag：忽略
		default:
			f.positional = append(f.positional, a)
		}
	}
	return f
}

// queryFields 输出函数的字段读写摘要（S1，field_trace.md §6.2），
// 按 direct_read / direct_write / indirect_write 分组。
func queryFields(acts *action.Actions, input string) int {
	logger := zap.L()
	logger.Debug("enter queryFields")
	defer logger.Debug("exit queryFields")
	n, rows, err := acts.FunctionFields(input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	fmt.Printf("字段读写（%s）:\n", n.Name)
	if len(rows) == 0 {
		fmt.Println("  无字段访问（SSA 字段追溯未产出，或该函数无字段读写）")
		return 0
	}
	groups := map[string][]*domain.FunctionFieldSummary{
		domain.SummaryDirectRead:    nil,
		domain.SummaryDirectWrite:   nil,
		domain.SummaryIndirectWrite: nil,
	}
	for _, r := range rows {
		groups[r.AccessKind] = append(groups[r.AccessKind], r)
	}
	for _, kind := range []string{domain.SummaryDirectRead, domain.SummaryDirectWrite, domain.SummaryIndirectWrite} {
		items := groups[kind]
		if len(items) == 0 {
			continue
		}
		fmt.Printf("  [%s] %d 个字段\n", kind, len(items))
		for _, it := range items {
			line := ""
			if it.LineStart > 0 {
				line = fmt.Sprintf(":%d", it.LineStart)
			}
			fmt.Printf("    %-60s %-24s %-6s %s\n",
				it.FieldPath, it.InstancePath, line, it.CodeSnippet)
		}
	}
	return 0
}

// queryTraceDir 输出字段追溯路径（S2/S3，field_trace.md §6.3/6.4）。
// 树形渲染：缩进 + 边类型 + 节点名 + (行号)（Q28）。
func queryTraceDir(acts *action.Actions, field, funcPath string, maxDepth int, forward bool) int {
	logger := zap.L()
	logger.Debug("enter queryTraceDir")
	defer logger.Debug("exit queryTraceDir")
	if funcPath == "" {
		fmt.Fprintln(os.Stderr, "error: trace 需要 --func <函数>（canonical ID 或名称）")
		return 2
	}
	n, rows, err := acts.Trace(action.TraceParams{Field: field, Func: funcPath, MaxDepth: maxDepth, Forward: forward})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if len(rows) == 0 {
		fmt.Printf("无追溯路径：%s 在 %s 中无匹配的字段访问点（--max-depth %d）\n",
			field, n.Name, maxDepth)
		return 0
	}
	direction := "←"
	title := "产生点（反向追溯）"
	if forward {
		direction = "→"
		title = "使用点（正向追踪）"
	}
	fmt.Printf("%s: %s @ %s\n", title, field, n.Name)
	for _, r := range rows {
		edge := lastEdgeKind(r.EdgeKinds)
		mark := ""
		if forward && r.IsUsage {
			mark = " [使用点]"
		}
		line := ""
		if r.Line > 0 {
			line = fmt.Sprintf(" (%d)", r.Line)
		}
		fmt.Printf("%s%s %s %s%s%s\n", strings.Repeat("  ", r.Depth), direction, edge, r.Name, line, mark)
	}
	return 0
}

// lastEdgeKind 取路径上最后一段边类型（进入当前节点的边）。
func lastEdgeKind(kinds string) string {
	if i := strings.LastIndex(kinds, ","); i >= 0 {
		return kinds[i+1:]
	}
	return kinds
}

// querySymbol 输出符号摘要（对齐 TD.md 7.1 explore_symbol 摘要层）。
func querySymbol(acts *action.Actions, input string) int {
	logger := zap.L()
	logger.Debug("enter querySymbol")
	defer logger.Debug("exit querySymbol")
	d, err := acts.SymbolDetail(input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	n := d.Node
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
	fmt.Printf("调用者数:   %d\n", len(d.Callers))
	fmt.Printf("被调用数:   %d\n", len(d.Callees))

	// 详情层：列出调用者与被调用者（上限 50，TD.md 7.1）
	if len(d.Callers) > 0 {
		fmt.Println("调用者:")
		printFacts(d.Callers, "source", 50)
	}
	if len(d.Callees) > 0 {
		fmt.Println("被调用者:")
		printFacts(d.Callees, "target", 50)
	}
	return 0
}

// queryGraph 输出 callers/callees/impact 查询结果。
func queryGraph(acts *action.Actions, sub, input string, depth int) int {
	logger := zap.L()
	logger.Debug("enter queryGraph")
	defer logger.Debug("exit queryGraph")
	n, err := acts.ResolveSymbol(input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	switch sub {
	case "callers":
		facts, err := acts.Callers(n.ID, depth)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		fmt.Printf("调用者（深度 %d，置信度 ≥%.2f）: %d 个\n", depth, action.MinConfidence, len(facts))
		printFacts(facts, "source", 100)
	case "callees":
		facts, err := acts.Callees(n.ID, depth)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		fmt.Printf("被调用者（深度 %d，置信度 ≥%.2f）: %d 个\n", depth, action.MinConfidence, len(facts))
		printFacts(facts, "target", 100)
	case "impact":
		nodes, err := acts.Impact(n.ID, depth)
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

// queryValueTrace 输出数据值在整条链路上的处理过程，按函数上下文分组
// （field_trace.md §14.2 数据值全链追踪）。
func queryValueTrace(acts *action.Actions, nodeID string, maxDepth int) int {
	logger := zap.L()
	logger.Debug("enter queryValueTrace")
	defer logger.Debug("exit queryValueTrace")
	rows, err := acts.ValueTrace(domain.CanonicalID(nodeID), maxDepth)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if len(rows) == 0 {
		fmt.Println("无数据流（节点不存在或无数据流边）")
		return 0
	}
	fmt.Printf("数据值追踪（%s，--max-depth %d）:\n", nodeID, maxDepth)
	var curFunc string
	for _, r := range rows {
		// 函数上下文分组
		if r.FuncID != curFunc {
			curFunc = r.FuncID
			group := shortFuncName(curFunc)
			if group == "" {
				group = "（未知函数）"
			}
			fmt.Printf("\n【%s】\n", group)
		}
		arrow := "→"
		if r.Dir == 0 {
			arrow = "←"
		}
		edge := lastEdgeKind(r.EdgeKinds)
		acc := ""
		if r.Kind == domain.KindFieldAccess {
			if r.Access == "read" {
				acc = " [读]"
			} else {
				acc = " [写]"
			}
		}
		line := ""
		if r.Line > 0 {
			line = fmt.Sprintf(":%d", r.Line)
		}
		fmt.Printf("  %s%s %s %s%s\n", strings.Repeat("  ", r.Depth), arrow, edge, r.Name+acc, line)
	}
	return 0
}

// shortFuncName 从函数 canonical ID 提取短名（symbol:go:<pkg>:<name> → <name>）。
func shortFuncName(id string) string {
	if i := strings.LastIndex(id, ":"); i >= 0 {
		return id[i+1:]
	}
	return id
}
