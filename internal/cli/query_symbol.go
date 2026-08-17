package cli

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/schaepher/codeintel/internal/action"
	"github.com/schaepher/codeintel/internal/domain"
	"go.uber.org/zap"
)

// lastEdgeKind 取路径上最后一段边类型（进入当前节点的边）。
func lastEdgeKind(kinds string) string {
	if i := strings.LastIndex(kinds, ","); i >= 0 {
		return kinds[i+1:]
	}
	return kinds
}

// querySymbol 输出符号摘要（对齐 TD.md 7.1 explore_symbol 摘要层）。
func querySymbol(acts *action.Actions, input string, opts outputOpts, since *domain.SinceInfo) int {
	logger := zap.L()
	logger.Debug("enter querySymbol")
	defer logger.Debug("exit querySymbol")
	d, err := acts.SymbolDetail(input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	n := d.Node
	// 动态派发候选（Q95）：接口类型 → 候选实现（置信度/注册点）
	var dispatch []*domain.Fact
	if n.Kind == domain.KindInterface {
		dispatch, _ = acts.DispatchCandidates(n.ID)
	}
	if opts.json {
		out := map[string]any{
			"id":        string(n.ID),
			"name":      n.Name,
			"kind":      string(n.Kind),
			"file":      n.FilePath,
			"line":      n.LineStart,
			"signature": n.Signature(),
			"doc":       n.DocComment(),
			"callers":   factIDs(d.Callers, "source"),
			"callees":   factIDs(d.Callees, "target"),
		}
		if len(dispatch) > 0 {
			cands := make([]map[string]any, 0, len(dispatch))
			for _, f := range dispatch {
				cands = append(cands, map[string]any{
					"id":            string(f.TargetID),
					"method":        f.Metadata["interface_method"],
					"origin":        f.Metadata["origin"],
					"confidence":    f.Metadata["confidence"],
					"register_site": f.Metadata["register_site"],
				})
			}
			out["candidates"] = cands
		}
		encodeJSON(out)
		return 0
	}
	fmt.Printf("ID:         %s\n", n.ID)
	fmt.Printf("名称:       %s%s\n", n.Name, sinceFlag(n, since))
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

	callerIDs := make([]domain.CanonicalID, 0, len(d.Callers))
	for _, f := range d.Callers {
		callerIDs = append(callerIDs, f.SourceID)
	}
	calleeIDs := make([]domain.CanonicalID, 0, len(d.Callees))
	for _, f := range d.Callees {
		calleeIDs = append(calleeIDs, f.TargetID)
	}
	callerMarks := sinceMarks(acts, callerIDs, since)
	calleeMarks := sinceMarks(acts, calleeIDs, since)
	if len(d.Callers) > 0 {
		fmt.Println("调用者:")
		printFacts(d.Callers, "source", 50, callerMarks)
	}
	if len(d.Callees) > 0 {
		fmt.Println("被调用者:")
		printFacts(d.Callees, "target", 50, calleeMarks)
	}

	if len(dispatch) > 0 {
		fmt.Printf("候选实现:   %d 个\n", len(dispatch))
		for _, f := range dispatch {
			conf, _ := f.Metadata["confidence"].(float64)
			origin, _ := f.Metadata["origin"].(string)
			method, _ := f.Metadata["interface_method"].(string)
			site, _ := f.Metadata["register_site"].(float64)
			line := ""
			if int(site) > 0 {
				line = fmt.Sprintf(" 注册点:%d", int(site))
			}
			fmt.Printf("    %-40s %s.%s [%s %.1f]%s\n",
				shortID(f.TargetID), shortFuncName(string(f.SourceID)), method, origin, conf, line)
		}
	}
	return 0
}

// factEndpointIDs 提取边的端点 ID 列表（--since 标注用）。
func factEndpointIDs(facts []*domain.Fact, endpoint string) []domain.CanonicalID {
	out := make([]domain.CanonicalID, 0, len(facts))
	for _, f := range facts {
		if endpoint == "target" {
			out = append(out, f.TargetID)
		} else {
			out = append(out, f.SourceID)
		}
	}
	return out
}

// factIDs 提取边的端点 ID 列表（endpoint=source/target，JSON 输出用）。
func factIDs(facts []*domain.Fact, endpoint string) []map[string]any {
	out := make([]map[string]any, 0, len(facts))
	for _, f := range facts {
		id := f.SourceID
		if endpoint == "target" {
			id = f.TargetID
		}
		out = append(out, map[string]any{
			"id":         string(id),
			"tool":       f.ToolSource,
			"confidence": f.Confidence,
		})
	}
	return out
}

// nodeBriefs 提取节点摘要（JSON 输出用）。
func nodeBriefs(nodes []*domain.CodeEntity) []map[string]any {
	out := make([]map[string]any, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, map[string]any{
			"id":   string(n.ID),
			"name": n.Name,
			"kind": string(n.Kind),
			"file": n.FilePath,
			"line": n.LineStart,
		})
	}
	return out
}

// printFacts 打印边列表；endpoint 为 "source" 时显示边左端（调用者场景），
// 否则显示右端（被调用者场景）。
func printFacts(facts []*domain.Fact, endpoint string, limit int, marks map[string]string) {
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
		flag := ""
		if m, ok := marks[string(id)]; ok {
			flag = " [" + m + "]"
		}
		fmt.Printf("  %s%s  (%s, conf=%.2f)\n", shortID(id), flag, f.ToolSource, f.Confidence)
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
