package sqlite

import (
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

// TestGetTableRelations：表关联分析——沿数据流边（data_flows_to/
// summary_io/argument/returns）从表虚拟节点出发 BFS，收集其他表的
// 虚拟节点（A.x 读出值流入 B.y 过滤列 → 表 A 关联表 B）。
func TestGetTableRelations(t *testing.T) {
	r := newTestRepo(t)
	funcID := "symbol:go:example.com/m:find"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(funcID), Kind: domain.KindFunction, Name: "find"},
		// 表 A 读虚拟节点 + row 值 + x 变量
		{ID: domain.CanonicalID(funcID + "#ext.sql.table_a.x.read@6"), Kind: domain.KindFieldAccess,
			Name: "table_a.x", FilePath: "a.go", LineStart: 6,
			Properties: map[string]any{"full_path": "table_a.x", "access_kind": "read",
				"type_string": "sql", "is_external": "true", "func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#t4"), Kind: domain.KindSSAValue, Name: "t4",
			Properties: map[string]any{"func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#x"), Kind: domain.KindSSAValue, Name: "x",
			Properties: map[string]any{"func_id": funcID}},
		// 表 B 过滤虚拟节点
		{ID: domain.CanonicalID(funcID + "#ext.sql.table_b.y.filter@9"), Kind: domain.KindFieldAccess,
			Name: "table_b.y", FilePath: "a.go", LineStart: 9,
			Properties: map[string]any{"full_path": "table_b.y", "access_kind": "filter",
				"type_string": "sql", "is_external": "true", "func_id": funcID}},
		// 干扰：无数据流链的表 C
		{ID: domain.CanonicalID(funcID + "#ext.sql.table_c.z.read@20"), Kind: domain.KindFieldAccess,
			Name: "table_c.z", FilePath: "a.go", LineStart: 20,
			Properties: map[string]any{"full_path": "table_c.z", "access_kind": "read",
				"type_string": "sql", "is_external": "true", "func_id": funcID}},
	}
	edges := []*domain.Fact{
		{SourceID: domain.CanonicalID(funcID + "#ext.sql.table_a.x.read@6"), TargetID: domain.CanonicalID(funcID + "#t4"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#t4"), TargetID: domain.CanonicalID(funcID + "#x"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#x"), TargetID: domain.CanonicalID(funcID + "#ext.sql.table_b.y.filter@9"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
	}
	save(t, r, nodes, edges)

	rels, err := r.GetTableRelations("table_a")
	if err != nil {
		t.Fatalf("GetTableRelations: %v", err)
	}
	if len(rels) != 1 {
		t.Fatalf("rels = %+v, want 1（table_b）", rels)
	}
	if rels[0].ToTable != "table_b" || rels[0].ToCol != "y" || rels[0].FromCol != "x" {
		t.Errorf("relation = %+v, want table_b.y ← table_a.x", rels[0])
	}
	if rels[0].Hops == 0 {
		t.Error("hops 应为数据流链长度（>0）")
	}
	// table_c 无链 → 不出现；无关联表 → 空
	empty, err := r.GetTableRelations("table_c")
	if err != nil || len(empty) != 0 {
		t.Fatalf("table_c rels = %v, %v", empty, err)
	}
}
