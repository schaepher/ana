package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/schaepher/codeintel/internal/action"
	"github.com/schaepher/codeintel/internal/domain"
	"go.uber.org/zap"
)

// queryFields 输出函数的字段读写摘要（S1，field_trace.md §6.2），
// 按 direct_read / direct_write / indirect_write 分组。
func queryFields(acts *action.Actions, input string, opts outputOpts, since *domain.SinceInfo) int {
	logger := zap.L()
	logger.Debug("enter queryFields")
	defer logger.Debug("exit queryFields")
	n, rows, err := acts.FunctionFields(input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if opts.json {
		type originRow struct {
			CallLine   int     `json:"call_line"`
			Callee     string  `json:"callee"`
			Origin     string  `json:"origin,omitempty"`
			Confidence float64 `json:"confidence,omitempty"`
		}
		type fieldRow struct {
			AccessKind   string      `json:"access_kind"`
			FieldPath    string      `json:"field_path"`
			InstancePath string      `json:"instance_path"`
			Line         int         `json:"line"`
			CodeSnippet  string      `json:"code_snippet"`
			Origins      []originRow `json:"origins,omitempty"` // Q161 间接写多来源
		}
		jrows := make([]fieldRow, 0, len(rows))
		for _, r := range rows {
			fr := fieldRow{r.AccessKind, r.FieldPath, r.InstancePath, r.LineStart, r.CodeSnippet, nil}
			if len(r.Origins) > 0 {
				for _, o := range r.Origins {
					fr.Origins = append(fr.Origins, originRow{
						CallLine: o.CallLine, Callee: shortFuncName(string(o.CalleeID)),
						Origin: o.Origin, Confidence: o.Confidence,
					})
				}
			}
			jrows = append(jrows, fr)
		}
		encodeJSON(map[string]any{"name": n.Name, "rows": jrows})
		return 0
	}
	fmt.Printf("字段读写（%s%s）:\n", n.Name, sinceFlag(n, since))
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

		if kind == domain.SummaryIndirectWrite && !opts.json {
			if sites, err := acts.IndirectWriteSites(n.ID); err == nil {
				for _, f := range sites {
					line, _ := f.Metadata["call_line"].(float64)
					args, _ := f.Metadata["call_args"].(string)
					callee := shortFuncName(string(f.TargetID))
					fmt.Printf("    调用点: :%d %s(%s)\n", int(line), callee, args)
				}
			}
		}
		for _, it := range items {
			line := ""
			if it.LineStart > 0 {
				line = fmt.Sprintf(":%d", it.LineStart)
			}
			fmt.Printf("    %-60s %-24s %-6s %s\n",
				it.FieldPath, it.InstancePath, line, it.CodeSnippet)
			for _, o := range it.Origins {
				tag := ""
				if o.Origin != "" {
					tag = fmt.Sprintf(" [候选 %s %.1f]", o.Origin, o.Confidence)
				}
				fmt.Printf("        ↳ 来源: :%d %s%s\n", o.CallLine, shortFuncName(string(o.CalleeID)), tag)
			}
		}
	}
	return 0
}

// queryTraceDir 输出字段追溯路径（S2/S3，field_trace.md §6.3/6.4）。
// 树形渲染：缩进 + 边类型 + 节点名 + (行号)（Q28）；--compact 去缩进。
func queryTraceDir(acts *action.Actions, field, funcPath string, maxDepth int, forward, followIndirect bool, opts outputOpts) int {
	logger := zap.L()
	logger.Debug("enter queryTraceDir")
	defer logger.Debug("exit queryTraceDir")
	if funcPath == "" {
		fmt.Fprintln(os.Stderr, "error: trace 需要 --func <函数>（canonical ID 或名称）")
		return 2
	}
	n, rows, err := acts.Trace(action.TraceParams{Field: field, Func: funcPath, MaxDepth: maxDepth, Forward: forward, FollowIndirect: followIndirect})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	rows, err = acts.TraceConditions(rows)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: 条件标注: %v\n", err)
	}
	if opts.json {
		type traceRow struct {
			ID         string   `json:"id"`
			Depth      int      `json:"depth"`
			Name       string   `json:"name"`
			Edge       string   `json:"edge"`
			Line       int      `json:"line"`
			IsUsage    bool     `json:"is_usage"`
			Conditions []string `json:"conditions,omitempty"`
		}
		jrows := make([]traceRow, 0, len(rows))
		for _, r := range rows {
			jrows = append(jrows, traceRow{string(r.ID), r.Depth, r.Name, lastEdgeKind(r.EdgeKinds), r.Line, r.IsUsage, r.Conditions})
		}
		encodeJSON(map[string]any{"field": field, "func": n.Name, "rows": jrows})
		return 0
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
		cond := ""
		if len(r.Conditions) > 0 {
			cond = " [条件: " + strings.Join(r.Conditions, "; ") + "]"
		}
		indent := strings.Repeat("  ", r.Depth)
		if opts.compact {
			indent = ""
		}
		fmt.Printf("%s%s %s %s%s%s%s\n", indent, direction, edge, r.Name, line, mark, cond)
	}
	return 0
}

// sinceFlag 函数/方法节点的 --since 标注（§17.2）：[new]/[mod]/空。
func sinceFlag(n *domain.CodeEntity, since *domain.SinceInfo) string {
	if since == nil || (n.Kind != domain.KindFunction && n.Kind != domain.KindMethod) {
		return ""
	}
	if m := action.MarkSince(n.FilePath, n.LineStart, n.LineEnd, since); m != "" {
		return " [" + m + "]"
	}
	return ""
}

// sinceMarks 对 ID 列表批量计算 --since 标注（callers/callees 邻居用）。
func sinceMarks(acts *action.Actions, ids []domain.CanonicalID, since *domain.SinceInfo) map[string]string {
	out := map[string]string{}
	if since == nil {
		return out
	}
	for _, id := range ids {
		n, err := acts.Symbol(id)
		if err != nil || (n.Kind != domain.KindFunction && n.Kind != domain.KindMethod) {
			continue
		}
		if m := action.MarkSince(n.FilePath, n.LineStart, n.LineEnd, since); m != "" {
			out[string(id)] = m
		}
	}
	return out
}
