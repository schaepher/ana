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

	rels, err := acts.RelationsAll("")
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

// renderCalleesDot 渲染 callees 为 DOT digraph（节点用短名，边带 kind）。

// renderValueTraceMermaid 渲染 value-trace 为 mermaid flowchart，
// 函数上下文用 subgraph 分组（Q89）。

// renderValueTraceDot 渲染 value-trace 为 DOT（同数据，dot 形态）。

// renderLifecycleMermaid 端到端生命周期图（Q99）：value-trace 全链聚合
// （含写锚点的下游跳板，⑤），节点类型标注（来源/读写/存储/观测）+
// 路径条件（Q92），mermaid flowchart 输出。复用 TraceConditions。
