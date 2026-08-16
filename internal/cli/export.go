package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/schaepher/codeintel/internal/action"
	"github.com/schaepher/codeintel/internal/domain"
	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
	"github.com/schaepher/codeintel/internal/logging"
	"go.uber.org/zap"
)

// cmdExport 实现 `codeintel export [--out analysis.json]`（S4，field_trace.md §2）：
// 从 function_field_summary 生成双层索引 JSON（字段 → 产生者/消费者）。
// 子命令：export graph（Q89：Mermaid/DOT 导出）。
func cmdExport(args []string) int {
	logger := zap.L()
	logger.Debug("enter cmdExport")
	defer logger.Debug("exit cmdExport")
	if len(args) > 0 && args[0] == "graph" {
		return cmdExportGraph(args[1:])
	}
	if len(args) > 0 && args[0] == "relations" {
		return cmdExportRelations(args[1:])
	}
	outPath := ""
	repoPath := "."
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--out" && i+1 < len(args):
			outPath = args[i+1]
			i++
		case strings.HasPrefix(a, "--out="):
			outPath = strings.TrimPrefix(a, "--out=")
		case a == "--repo" && i+1 < len(args):
			repoPath = args[i+1]
			i++
		case strings.HasPrefix(a, "--repo="):
			repoPath = strings.TrimPrefix(a, "--repo=")
		}
	}

	abs, _, err := resolveRepo(repoPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	// 日志切换到 .codeintel/codeintel.log（stdout 只留查询结果，Q88）
	if err := logging.ToFile(abs); err != nil {
		fmt.Fprintf(os.Stderr, "warning: 日志切换失败: %v\n", err)
	}
	db, err := sqlite.Open(abs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()
	acts := action.New(sqlite.NewRepo(db))

	index, err := acts.ExportIndex()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	data := struct {
		Fields map[string]*action.ExportField `json:"fields"`
	}{Fields: index}
	out, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if outPath == "" {
		fmt.Println(string(out))
		return 0
	}
	if err := os.WriteFile(outPath, out, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	fmt.Printf("已导出 %d 个字段的索引到 %s\n", len(index), outPath)
	return 0
}

// cmdExportRelations 实现 `codeintel export relations`（Q160）：
// 一次性导出全库表间关联 JSON（{"relations": [...]}），AGENT 单次
// 调用拿全库键关联（与 query relations --all 数据同源，合并去重）。
func cmdExportRelations(args []string) int {
	logger := zap.L()
	logger.Debug("enter cmdExportRelations")
	defer logger.Debug("exit cmdExportRelations")
	outPath := ""
	repoPath := "."
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--out" && i+1 < len(args):
			outPath = args[i+1]
			i++
		case strings.HasPrefix(a, "--out="):
			outPath = strings.TrimPrefix(a, "--out=")
		case a == "--repo" && i+1 < len(args):
			repoPath = args[i+1]
			i++
		case strings.HasPrefix(a, "--repo="):
			repoPath = strings.TrimPrefix(a, "--repo=")
		}
	}

	abs, _, err := resolveRepo(repoPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	// 日志切换到 .codeintel/codeintel.log（stdout 只留查询结果，Q88）
	if err := logging.ToFile(abs); err != nil {
		fmt.Fprintf(os.Stderr, "warning: 日志切换失败: %v\n", err)
	}
	db, err := sqlite.Open(abs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()
	acts := action.New(sqlite.NewRepo(db))

	rels, err := acts.RelationsAll()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if rels == nil {
		rels = []*domain.TableRelation{}
	}
	data := struct {
		Relations []*domain.TableRelation `json:"relations"`
	}{Relations: rels}
	out, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if outPath == "" {
		fmt.Println(string(out))
		return 0
	}
	if err := os.WriteFile(outPath, out, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	fmt.Printf("已导出 %d 条表间关联到 %s\n", len(rels), outPath)
	return 0
}

// cmdExportGraph 实现 `codeintel export graph`（Q89）：
//
//	--type value-trace|callees --target <节点> [--format mermaid|dot] [--out file]
//
// value-trace 默认 mermaid（flowchart 子图表达函数分组）；callees 默认 dot。
// 数据来自 action 层（复用查询用例，Q86 CLI 主通道）。
func cmdExportGraph(args []string) int {
	logger := zap.L()
	logger.Debug("enter cmdExportGraph")
	defer logger.Debug("exit cmdExportGraph")
	graphType := ""
	target := ""
	format := ""
	outPath := ""
	repoPath := "."
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--type" && i+1 < len(args):
			graphType = args[i+1]
			i++
		case strings.HasPrefix(a, "--type="):
			graphType = strings.TrimPrefix(a, "--type=")
		case a == "--target" && i+1 < len(args):
			target = args[i+1]
			i++
		case strings.HasPrefix(a, "--target="):
			target = strings.TrimPrefix(a, "--target=")
		case a == "--format" && i+1 < len(args):
			format = args[i+1]
			i++
		case strings.HasPrefix(a, "--format="):
			format = strings.TrimPrefix(a, "--format=")
		case a == "--out" && i+1 < len(args):
			outPath = args[i+1]
			i++
		case strings.HasPrefix(a, "--out="):
			outPath = strings.TrimPrefix(a, "--out=")
		case a == "--repo" && i+1 < len(args):
			repoPath = args[i+1]
			i++
		case strings.HasPrefix(a, "--repo="):
			repoPath = strings.TrimPrefix(a, "--repo=")
		}
	}
	if graphType != "value-trace" && graphType != "callees" && graphType != "lifecycle" && graphType != "modules" {
		fmt.Fprintln(os.Stderr, "error: --type 须为 value-trace / callees / lifecycle / modules")
		return 2
	}
	if target == "" && graphType != "modules" {
		fmt.Fprintln(os.Stderr, "error: --target <节点> 是必需的")
		return 2
	}
	if format == "" {
		if graphType == "callees" {
			format = "dot"
		} else {
			format = "mermaid"
		}
	}
	if format != "mermaid" && format != "dot" {
		fmt.Fprintln(os.Stderr, "error: --format 须为 mermaid 或 dot")
		return 2
	}

	abs, _, err := resolveRepo(repoPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if err := logging.ToFile(abs); err != nil {
		fmt.Fprintf(os.Stderr, "warning: 日志切换失败: %v\n", err)
	}
	db, err := sqlite.Open(abs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()
	acts := action.New(sqlite.NewRepo(db))

	var output string
	anchor := domain.CanonicalID(target)
	switch {
	case graphType == "callees":
		output, err = renderCalleesDot(acts, anchor)
	case graphType == "modules":
		// 模块调用图（§18.4）：无需 target 锚点
		calls, merr := acts.ModuleCalls("")
		if merr != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", merr)
			return 1
		}
		output = renderModulesMermaid(calls)
	case graphType == "lifecycle":
		// ⑤：字段路径输入解析为锚点（此前传字段路径 → 无行 → 空图）
		if anchor, err = acts.ResolveAnchor(target); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		output, err = renderLifecycleMermaid(acts, anchor)
	case format == "mermaid":
		output, err = renderValueTraceMermaid(acts, anchor)
	default:
		output, err = renderValueTraceDot(acts, anchor)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if outPath == "" {
		fmt.Println(output)
		return 0
	}
	if err := os.WriteFile(outPath, []byte(output), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	fmt.Printf("已导出 %s 图到 %s\n", format, outPath)
	return 0
}

// renderCalleesDot 渲染 callees 为 DOT digraph（节点用短名，边带 kind）。
func renderCalleesDot(acts *action.Actions, id domain.CanonicalID) (string, error) {
	facts, err := acts.Callees(id, 1)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	sb.WriteString("digraph callees {\n")
	sb.WriteString("  rankdir=LR;\n")
	for _, f := range facts {
		sb.WriteString(fmt.Sprintf("  %q -> %q [label=%q];\n",
			shortID(f.SourceID), shortID(f.TargetID), string(f.Kind)))
	}
	sb.WriteString("}\n")
	return sb.String(), nil
}

// renderValueTraceMermaid 渲染 value-trace 为 mermaid flowchart，
// 函数上下文用 subgraph 分组（Q89）。
func renderValueTraceMermaid(acts *action.Actions, id domain.CanonicalID) (string, error) {
	rows, err := acts.ValueTrace(id, 8, 0)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	sb.WriteString("flowchart LR\n")
	group := ""
	for _, r := range rows {
		if r.FuncID != group {
			if group != "" {
				sb.WriteString("  end\n")
			}
			group = r.FuncID
			gname := shortFuncName(group)
			if gname == "" {
				gname = "unknown"
			}
			sb.WriteString(fmt.Sprintf("  subgraph %q\n", gname))
		}
		arrow := "-->"
		if r.Dir == 0 {
			arrow = "<--"
		}
		sb.WriteString(fmt.Sprintf("    %q %s %q\n", shortID(r.ID), arrow, shortID(r.ID)+"|"+r.Name))
	}
	if group != "" {
		sb.WriteString("  end\n")
	}
	return sb.String(), nil
}

// renderValueTraceDot 渲染 value-trace 为 DOT（同数据，dot 形态）。
func renderValueTraceDot(acts *action.Actions, id domain.CanonicalID) (string, error) {
	rows, err := acts.ValueTrace(id, 8, 0)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	sb.WriteString("digraph value_trace {\n  rankdir=LR;\n")
	seen := map[string]bool{}
	for _, r := range rows {
		nid := string(r.ID)
		if !seen[nid] {
			seen[nid] = true
			sb.WriteString(fmt.Sprintf("  %q [label=%q];\n", shortID(r.ID), r.Name))
		}
	}
	for _, r := range rows {
		if r.Depth == 0 {
			continue
		}
		sb.WriteString(fmt.Sprintf("  %q -> %q [label=%q];\n",
			shortID(r.ID), shortID(r.ID)+"|"+r.Name, lastEdgeKind(r.EdgeKinds)))
	}
	sb.WriteString("}\n")
	return sb.String(), nil
}

// renderLifecycleMermaid 端到端生命周期图（Q99）：value-trace 全链聚合
// （含写锚点的下游跳板，⑤），节点类型标注（来源/读写/存储/观测）+
// 路径条件（Q92），mermaid flowchart 输出。复用 TraceConditions。
func renderLifecycleMermaid(acts *action.Actions, id domain.CanonicalID) (string, error) {
	rows, err := acts.Lifecycle(id)
	if err != nil {
		return "", err
	}
	rows, err = acts.TraceConditions(rows)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	sb.WriteString("flowchart LR\n")
	group := ""
	for _, r := range rows {
		if r.FuncID != group {
			if group != "" {
				sb.WriteString("  end\n")
			}
			group = r.FuncID
			gname := shortFuncName(group)
			if gname == "" {
				gname = "unknown"
			}
			sb.WriteString(fmt.Sprintf("  subgraph %q\n", gname))
		}
		// 节点类型标注（生命周期语义）
		label := r.Name
		switch {
		case strings.HasPrefix(r.Name, "sql."):
			label += " [存储]"
		case strings.HasPrefix(r.Name, "metric"):
			label += " [观测]"
		case r.Kind == domain.KindFieldAccess:
			acc := "写"
			if r.Access == "read" {
				acc = "读"
			}
			label += " [" + acc + "]"
		}
		if len(r.Conditions) > 0 {
			label += " 条件:" + strings.Join(r.Conditions, ";")
		}
		arrow := "-->"
		if r.Dir == 0 {
			arrow = "<--"
		}
		sb.WriteString(fmt.Sprintf("    %q %s %q\n", shortID(r.ID), arrow, shortID(r.ID)+"|"+label))
	}
	if group != "" {
		sb.WriteString("  end\n")
	}
	return sb.String(), nil
}
