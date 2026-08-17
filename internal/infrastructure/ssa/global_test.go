package ssa

import (
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

// TestGlobalInitTrace：全局变量初始化溯源（Q98）——`var G = T{...}` 的
// init Store 产生 data_flows_to 边（初始化值 → G 节点），value-trace
// 从字段访问反向可达初始化表达式。
func TestGlobalInitTrace(t *testing.T) {
	nodes, facts, _ := indexFixtureFull(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

type T struct {
	A int
}

var G = T{A: 42}

func useG() int {
	return G.A
}
`,
	})
	// G 节点存在（origin_kind=global，跨函数共享 ID）
	gID := "symbol:go:example.com/mtest:var.G"
	g := nodeByID(t, nodes, gID)
	if g.Property("origin_kind") != "global" {
		t.Fatalf("G 全局节点 origin_kind = %q", g.Property("origin_kind"))
	}
	// init 写入：data_flows_to 边 target=G（初始化值 → G）
	found := false
	for _, f := range facts {
		if f.Kind == domain.FactDataFlowsTo && string(f.TargetID) == gID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("G 应有 data_flows_to 入边（init 初始化值 → G）")
	}
}

// TestGlobalFieldReadChain：G.A 读链（G → G.A → 结果）完整，全局溯源
// 可穿层（Q98：全局变量作为 value-trace 起点）。
func TestGlobalFieldReadChain(t *testing.T) {
	nodes, facts, _ := indexFixtureFull(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

type T struct {
	A int
}

var G = T{A: 42}

func useG() int {
	return G.A
}
`,
	})
	funcID := "symbol:go:example.com/mtest:useG"
	fa := findFieldAccess(t, nodes, funcID, "G.A", "read")
	// 基地址是跨函数共享的全局节点（UnOp 解引用结果 Name 同为 G，
	// 须按共享 ID 精确锚定）
	gBase := nodeByID(t, nodes, "symbol:go:example.com/mtest:var.G")
	// G → G.A 边
	found := false
	for _, f := range facts {
		if f.Kind == domain.FactDataFlowsTo && string(f.SourceID) == string(gBase.ID) &&
			string(f.TargetID) == string(fa.ID) {
			found = true
		}
	}
	if !found {
		t.Errorf("G → G.A 边缺失（全局变量字段读链）")
	}
}
