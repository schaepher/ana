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

// sliceOutput 顶层结构：DataFlowSlice
type sliceOutput struct {
	DataFlowSlice []slice `json:"dataFlowSlice"`
}

type slice struct {
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

// parseSlices 解析数据流切片 JSON 并产出 DATA_FLOWS_TO 边。
// 每条切片路径：起点节点所在方法 → 终点节点所在方法（metadata 记录路径）。
func parseSlices(repo *domain.Repository, path string, emit domain.EmitFunc) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read joern slices: %w", err)
	}
	var out sliceOutput
	if err := json.Unmarshal(data, &out); err != nil {
		return fmt.Errorf("parse joern slices: %w", err)
	}
	for _, s := range out.DataFlowSlice {
		if len(s.Nodes) < 2 {
			continue
		}
		// 路径按 edges 顺序整理（nodes 集合无序，用 edges 重建顺序）
		pathNodes := orderNodes(s)
		if len(pathNodes) < 2 {
			continue
		}
		first, last := pathNodes[0], pathNodes[len(pathNodes)-1]
		src, ok1 := methodIDFor(repo, first)
		dst, ok2 := methodIDFor(repo, last)
		if !ok1 || !ok2 || src == dst {
			continue
		}
		pathText := flowPathText(pathNodes)
		if err := emit(domain.Item{Fact: &domain.Fact{
			SourceID:   src,
			TargetID:   dst,
			Kind:       domain.FactDataFlowsTo,
			ToolSource: domain.ToolJoern,
			Confidence: 0.7,
			Metadata: map[string]any{
				"path":     pathText,
				"source":   first.Code,
				"sink":     last.Code,
				"line_num": first.LineNumber,
			},
		}}); err != nil {
			return err
		}
	}
	return nil
}

// orderNodes 用 edges 把切片节点整理成路径顺序（起点为无入边的节点）。
func orderNodes(s slice) []sliceNode {
	byID := map[int64]sliceNode{}
	for _, n := range s.Nodes {
		byID[n.ID] = n
	}
	hasIn := map[int64]bool{}
	for _, e := range s.Edges {
		hasIn[e.Dst] = true
	}
	// 找起点（无入边）
	var start int64
	for _, n := range s.Nodes {
		if !hasIn[n.ID] {
			start = n.ID
			break
		}
	}
	if start == 0 {
		return nil
	}
	var ordered []sliceNode
	cur := start
	for {
		n, ok := byID[cur]
		if !ok {
			break
		}
		ordered = append(ordered, n)
		// 找下一跳
		next := int64(0)
		for _, e := range s.Edges {
			if e.Src == cur {
				next = e.Dst
				break
			}
		}
		if next == 0 {
			break
		}
		cur = next
	}
	return ordered
}

// flowPathText 生成 source_var -> ... -> sink_var 路径文本（TD.md 7.3）。
func flowPathText(nodes []sliceNode) string {
	parts := make([]string, 0, len(nodes))
	for _, n := range nodes {
		parts = append(parts, flowVarName(n))
	}
	return strings.Join(parts, " -> ")
}

// flowVarName 提取路径节点的变量显示名（优先 code，去掉多余空白）。
func flowVarName(n sliceNode) string {
	code := strings.TrimSpace(n.Code)
	if code == "" {
		return n.Name
	}
	if len(code) > 60 {
		return code[:60]
	}
	return code
}

// methodIDFor 将切片节点定位到方法 canonical ID。
// 用 parentFile（相对路径）推导包路径 + parentMethod 匹配方法名。
func methodIDFor(repo *domain.Repository, n sliceNode) (domain.CanonicalID, bool) {
	file := n.ParentFile
	if file == "" {
		return "", false
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

	// parentMethod 形如 "main" / "(S).Foo" / "pkg.Func"（Joern 输出待实测）
	m := n.ParentMethod
	if i := strings.LastIndex(m, "."); i >= 0 && !strings.HasPrefix(m, "(") {
		m = m[i+1:]
	}
	if m == "" {
		return "", false
	}
	name := m
	if strings.HasPrefix(m, "(") {
		// (T).M → T.M 规范：提取 T 与 M
		if j := strings.Index(m, ")."); j >= 0 {
			t := strings.TrimPrefix(m[1:j], "*")
			name = canonicalizer.MethodName(t, m[j+2:])
		}
	}
	id := canonicalizer.GoSymbolID(pkgPath, name)
	if string(id) == "" {
		return "", false
	}
	return id, true
}
