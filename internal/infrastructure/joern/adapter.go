// Package joern 实现 Joern 适配器（TD.md 5.1：数据流，置信度 0.7）。
// 流程：
//  1. joern-parse --language GOLANG 生成 CPG（gosrc2cpg 前端）
//  2. joern-slice data-flow 导出数据流切片（JSON）
//  3. 解析切片 → DATA_FLOWS_TO 边（source=起点所在方法，target=终点所在方法，
//     metadata.path 保存 source_var -> ... -> sink_var 路径）
//
// Joern 缺失或失败时由 Orchestrator 标记降级（TD.md 9.2）。
package joern

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/schaepher/codeintel/internal/canonicalizer"
	"github.com/schaepher/codeintel/internal/domain"
)

// Adapter 是 Joern 数据流适配器。
type Adapter struct {
	// JoernBinDir joern 安装目录（含 joern-parse/joern-slice），空则自动查找
	JoernBinDir string
}

var _ domain.IndexerPort = (*Adapter)(nil)

// Name 实现 IndexerPort。
func (a *Adapter) Name() string { return "joern" }

// Index 生成 CPG 并导出数据流，产出 DATA_FLOWS_TO 边。
func (a *Adapter) Index(ctx context.Context, repo *domain.Repository, emit domain.EmitFunc) error {
	joernDir, err := a.resolveJoern()
	if err != nil {
		return err
	}
	workDir, err := os.MkdirTemp("", "codeintel-joern-*")
	if err != nil {
		return fmt.Errorf("joern temp dir: %w", err)
	}
	defer os.RemoveAll(workDir)

	cpgPath := filepath.Join(workDir, "cpg.bin")
	// 1. 生成 CPG
	parseBin := filepath.Join(joernDir, "joern-parse")
	cmd := exec.CommandContext(ctx, parseBin, repo.Path, "-o", cpgPath, "--language", "GOLANG")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("joern-parse failed: %v: %s", err, string(out))
	}
	// 2. 数据流切片（JSON）
	sliceOut := filepath.Join(workDir, "slices.json")
	sliceBin := filepath.Join(joernDir, "joern-slice")
	cmd = exec.CommandContext(ctx, sliceBin, "data-flow", cpgPath, "-o", sliceOut)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("joern-slice failed: %v: %s", err, string(out))
	}
	// 3. 解析并产出边
	return parseSlices(repo, sliceOut, emit)
}

func (a *Adapter) resolveJoern() (string, error) {
	if a.JoernBinDir != "" {
		return a.JoernBinDir, nil
	}
	// PATH 查找
	for _, bin := range []string{"joern-parse", "joern-slice"} {
		if _, err := exec.LookPath(bin); err != nil {
			return "", fmt.Errorf("joern not found in PATH (install from https://github.com/joernio/joern/releases)")
		}
	}
	dir, _ := filepath.Split(mustLookPath("joern-parse"))
	return strings.TrimSuffix(dir, string(filepath.Separator)), nil
}

func mustLookPath(bin string) string {
	p, err := exec.LookPath(bin)
	if err != nil {
		return bin
	}
	return p
}

// ---------- JSON 结构（joern-slice data-flow 输出） ----------

// sliceOutput 顶层结构：单条 DataFlowSlice（数据流图：REACHING_DEF 边集合）
type sliceOutput struct {
	Type  string      `json:"$type"`
	Nodes []sliceNode `json:"nodes"`
	Edges []sliceEdge `json:"edges"`
}

type sliceNode struct {
	ID           int64  `json:"id"`
	Label        string `json:"label"`
	Name         string `json:"name"`
	Code         string `json:"code"`
	TypeFullName string `json:"typeFullName"`
	ParentMethod string `json:"parentMethod"`
	ParentFile   string `json:"parentFile"`
	LineNumber   int    `json:"lineNumber"`
	ColumnNumber int    `json:"columnNumber"`
}

type sliceEdge struct {
	Src   int64  `json:"src"`
	Dst   int64  `json:"dst"`
	Label string `json:"label"`
}

// parseSlices 解析数据流切片 JSON：
//   - 方法内 REACHING_DEF 边聚合为数据流路径，写入方法节点
//     properties.data_flows（trace_data_flow 查询用）
//   - 跨方法边产出 DATA_FLOWS_TO 边（起点方法 → 终点方法）
//
// 注：gosrc2cpg 当前只产出方法内数据流（REACHING_DEF 均为同方法），
// 跨方法参数流需 Joern 交互式数据流分析，MVP 不接入。
func parseSlices(repo *domain.Repository, path string, emit domain.EmitFunc) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read joern slices: %w", err)
	}
	var out sliceOutput
	if err := json.Unmarshal(data, &out); err != nil {
		return fmt.Errorf("parse joern slices: %w", err)
	}
	byID := map[int64]sliceNode{}
	for _, n := range out.Nodes {
		byID[n.ID] = n
	}

	// 方法内路径聚合：methodID → 有序变量序列（源 → 汇）
	type flowEntry struct {
		ref      methodRef
		segments []string
	}
	methodFlows := map[domain.CanonicalID]*flowEntry{}
	emitted := map[string]bool{}

	for _, e := range out.Edges {
		srcNode, ok1 := byID[e.Src]
		dstNode, ok2 := byID[e.Dst]
		if !ok1 || !ok2 {
			continue
		}
		srcRef, ok1 := methodRefFor(repo, srcNode)
		dstRef, ok2 := methodRefFor(repo, dstNode)
		if !ok1 || !ok2 {
			continue
		}
		if srcRef.ID == dstRef.ID {
			// 方法内数据流：记录路径片段（去重）
			fe := methodFlows[srcRef.ID]
			if fe == nil {
				fe = &flowEntry{ref: srcRef}
				methodFlows[srcRef.ID] = fe
			}
			seg := strings.TrimSpace(srcNode.Code)
			if seg == "" {
				seg = strings.TrimSpace(dstNode.Code)
			}
			if seg != "" && (len(fe.segments) == 0 || fe.segments[len(fe.segments)-1] != seg) {
				fe.segments = append(fe.segments, seg)
			}
			continue
		}
		// 跨方法数据流：DATA_FLOWS_TO 边
		key := string(srcRef.ID) + "|" + string(dstRef.ID) + "|" + strings.TrimSpace(srcNode.Code)
		if emitted[key] {
			continue
		}
		emitted[key] = true
		_ = emit(domain.Item{Node: nodeFromRef(srcRef)})
		_ = emit(domain.Item{Node: nodeFromRef(dstRef)})
		if err := emit(domain.Item{Fact: &domain.Fact{
			SourceID:   srcRef.ID,
			TargetID:   dstRef.ID,
			Kind:       domain.FactDataFlowsTo,
			ToolSource: domain.ToolJoern,
			Confidence: 0.7,
			Metadata: map[string]any{
				"source":   strings.TrimSpace(srcNode.Code),
				"sink":     strings.TrimSpace(dstNode.Code),
				"line_num": srcNode.LineNumber,
			},
		}}); err != nil {
			return err
		}
	}

	// 方法内数据流路径写入节点 properties.data_flows（一次聚合后 emit，
	// 避免 json_patch 数组互相覆盖）
	for _, fe := range methodFlows {
		if len(fe.segments) < 2 {
			continue
		}
		n := nodeFromRef(fe.ref)
		n.Properties = map[string]any{
			"data_flows": []string{strings.Join(fe.segments, " -> ")},
		}
		_ = emit(domain.Item{Node: n})
	}
	return nil
}

// methodRef 是切片节点定位到的方法引用（ID + 节点信息）。
type methodRef struct {
	ID       domain.CanonicalID
	Name     string
	Kind     domain.EntityKind
	FilePath string
	Line     int
}

// methodRefFor 将切片节点定位到方法：用 parentFile（相对路径）推导包路径
// + parentMethod 匹配方法名。parentMethod 形如 "main.process" / "(S).Foo"。
func methodRefFor(repo *domain.Repository, n sliceNode) (methodRef, bool) {
	file := n.ParentFile
	if file == "" {
		return methodRef{}, false
	}
	// 相对仓库根
	rel := strings.TrimPrefix(filepath.ToSlash(file), "./")
	dir := filepath.ToSlash(filepath.Dir(rel))
	if dir == "." || dir == "" {
		dir = ""
	} else {
		dir += "/"
	}
	pkgPath := repo.Module + "/" + dir
	pkgPath = strings.TrimSuffix(pkgPath, "/")

	m := n.ParentMethod
	if i := strings.LastIndex(m, "."); i >= 0 && !strings.HasPrefix(m, "(") {
		m = m[i+1:]
	}
	if m == "" {
		return methodRef{}, false
	}
	ref := methodRef{
		FilePath: rel,
		Line:     n.LineNumber,
		Kind:     domain.KindFunction,
	}
	if strings.HasPrefix(m, "(") {
		// (T).M → (T).M 规范：提取 T 与 M
		if j := strings.Index(m, ")."); j >= 0 {
			t := strings.TrimPrefix(m[1:j], "*")
			ref.Name = canonicalizer.MethodName(t, m[j+2:])
			ref.Kind = domain.KindMethod
		} else {
			return methodRef{}, false
		}
	} else {
		ref.Name = m
	}
	ref.ID = canonicalizer.GoSymbolID(pkgPath, ref.Name)
	if string(ref.ID) == "" {
		return methodRef{}, false
	}
	return ref, true
}

// nodeFromRef 为方法引用生成轻量节点（UPSERT 合并，不覆盖 SCIP 节点）。
func nodeFromRef(ref methodRef) *domain.CodeEntity {
	return &domain.CodeEntity{
		ID:        ref.ID,
		Kind:      ref.Kind,
		Name:      ref.Name,
		FilePath:  ref.FilePath,
		LineStart: ref.Line,
		LineEnd:   ref.Line,
	}
}
