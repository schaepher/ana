package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/schaepher/codeintel/internal/action"
	"github.com/schaepher/codeintel/internal/domain"
	"go.uber.org/zap"
)

// queryValueTrace 输出数据值在整条链路上的处理过程，按函数上下文分组
// （field_trace.md §14.2 数据值全链追踪）。
func queryValueTrace(acts *action.Actions, nodeID string, maxDepth int, minConf float64, includeContainer bool, opts outputOpts) int {
	logger := zap.L()
	logger.Debug("enter queryValueTrace")
	defer logger.Debug("exit queryValueTrace")
	rows, err := acts.ValueTrace(domain.CanonicalID(nodeID), maxDepth, minConf, includeContainer)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	rows, err = acts.TraceConditions(rows)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: 条件标注: %v\n", err)
	}
	if opts.json {
		type flowRow struct {
			ID            string             `json:"id"`
			Name          string             `json:"name"`
			Depth         int                `json:"depth"`
			Dir           int                `json:"dir"`
			Edge          string             `json:"edge"`
			Line          int                `json:"line"`
			Kind          string             `json:"kind"`
			Access        string             `json:"access"`
			FuncID        string             `json:"func_id"`
			FuncName      string             `json:"func_name"`
			Conditions    []string           `json:"conditions,omitempty"`
			Dispatch      *dispatchJSON      `json:"dispatch,omitempty"`       // Q157 P1 候选标注
			EdgeCandidate *edgeCandidateJSON `json:"edge_candidate,omitempty"` // Q161 边级候选标注
		}
		jrows := make([]flowRow, 0, len(rows))
		for _, r := range rows {
			var disp *dispatchJSON
			if r.DispatchCandidate {
				disp = &dispatchJSON{Origin: r.DispatchOrigin, Confidence: r.DispatchConf}
			}
			var ec *edgeCandidateJSON
			if r.EdgeOrigin != "" {
				ec = &edgeCandidateJSON{Iface: r.EdgeIface, Origin: r.EdgeOrigin, Confidence: r.EdgeConf}
			}
			jrows = append(jrows, flowRow{ID: string(r.ID), Name: r.Name, Depth: r.Depth, Dir: r.Dir,
				Edge: lastEdgeKind(r.EdgeKinds), Line: r.Line, Kind: string(r.Kind), Access: r.Access,
				FuncID: r.FuncID, FuncName: shortFuncName(r.FuncID), Conditions: r.Conditions,
				Dispatch: disp, EdgeCandidate: ec})
		}
		encodeJSON(map[string]any{"flows": jrows})
		return 0
	}
	if len(rows) == 0 {
		fmt.Println("无数据流（节点不存在或无数据流边）")
		return 0
	}
	fmt.Printf("数据值追踪（%s，--max-depth %d）:\n", nodeID, maxDepth)
	var curFunc string
	for _, r := range rows {

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
		cond := ""
		if len(r.Conditions) > 0 {
			cond = " [条件: " + strings.Join(r.Conditions, "; ") + "]"
		}
		disp := ""
		if r.DispatchCandidate {
			disp = fmt.Sprintf(" [候选 %s %.1f]", r.DispatchOrigin, r.DispatchConf)
		}
		if r.EdgeOrigin != "" {
			disp = fmt.Sprintf(" [动态候选 %s %.1f %s]", r.EdgeOrigin, r.EdgeConf, r.EdgeIface)
		}
		indent := strings.Repeat("  ", r.Depth)
		if opts.compact {
			indent = ""
		}
		fmt.Printf("%s%s %s %s%s%s%s\n", indent, arrow, edge, r.Name+acc, line, cond, disp)
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

// querySummary 跨层摘要（Q100）：字段生命周期主链
// （入口 → 计算 → 写入 → 消费），每步带 file:line。
func querySummary(acts *action.Actions, input string, opts outputOpts, format string) int {
	logger := zap.L()
	logger.Debug("enter querySummary")
	defer logger.Debug("exit querySummary")

	anchor, err := acts.ResolveAnchor(input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	steps, err := acts.SummaryChain(anchor)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if len(steps) == 0 {
		fmt.Println("无生命周期链路（节点不存在或无数据流边）")
		return 0
	}
	if opts.json {
		encodeJSON(map[string]any{"steps": steps})
		return 0
	}
	if format == "mermaid" {
		var sb strings.Builder
		sb.WriteString("flowchart LR\n")
		for _, st := range steps {
			label := st.Name
			if st.Line > 0 {
				label += fmt.Sprintf(":%d", st.Line)
			}
			sb.WriteString(fmt.Sprintf("    %q --> %q\n", st.Func+"|"+label, st.Kind+"|"+st.Name))
		}
		fmt.Println(sb.String())
		return 0
	}
	fmt.Printf("字段生命周期: %s（%d 步）\n", steps[0].Name, len(steps))
	for _, st := range steps {
		loc := ""
		if st.File != "" {
			loc = st.File
			if st.Line > 0 {
				loc += fmt.Sprintf(":%d", st.Line)
			}
		}
		fmt.Printf("  %-8s %-40s %s %s\n", "["+st.Kind+"]", st.Name, loc, st.Func)
	}
	return 0
}
