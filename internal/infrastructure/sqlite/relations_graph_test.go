package sqlite

import (
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

// TestGetAllTableRelationsCacheHit：--all 缓存优先（Q177）——完整计算
// 一次后改图（删节点），再次 --all 直接读 relation_candidates 返回
// （不重新加载全图/BFS）；build_id 变化后缓存失效重算。
func TestGetAllTableRelationsCacheHit(t *testing.T) {
	r := newTestRepo(t)
	funcID := "symbol:go:example.com/m:find"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(funcID), Kind: domain.KindFunction, Name: "find"},
		{ID: domain.CanonicalID(funcID + "#ext.sql.table_a.id.read@6"), Kind: domain.KindFieldAccess,
			Name: "table_a.id", FilePath: "a.go", LineStart: 6,
			Properties: map[string]any{"full_path": "table_a.id", "access_kind": "read",
				"type_string": "sql", "is_external": "true", "func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#t4"), Kind: domain.KindSSAValue, Name: "t4",
			Properties: map[string]any{"func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#x"), Kind: domain.KindSSAValue, Name: "x",
			Properties: map[string]any{"func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#ext.sql.table_b.a_id.filter@9"), Kind: domain.KindFieldAccess,
			Name: "table_b.a_id", FilePath: "a.go", LineStart: 9,
			Properties: map[string]any{"full_path": "table_b.a_id", "access_kind": "filter",
				"type_string": "sql", "is_external": "true", "func_id": funcID}},
	}
	edges := []*domain.Fact{
		{SourceID: domain.CanonicalID(funcID + "#ext.sql.table_a.id.read@6"), TargetID: domain.CanonicalID(funcID + "#t4"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#t4"), TargetID: domain.CanonicalID(funcID + "#x"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#x"), TargetID: domain.CanonicalID(funcID + "#ext.sql.table_b.a_id.filter@9"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
	}
	save(t, r, nodes, edges)
	if err := r.Save(&domain.BuildMeta{BuildID: "b1", ToolName: "all", Status: "success"}); err != nil {
		t.Fatalf("Save build: %v", err)
	}
	// Q228：全量查询要求计算完成——先预计算（进度 done + 写缓存）
	if err := r.PrecomputeAllRelations(nil); err != nil {
		t.Fatalf("precompute: %v", err)
	}
	// 第一次 --all：命中缓存（正向 fk + 反向 read = 2 条）
	rels1, err := r.GetAllTableRelations("")
	if err != nil || len(rels1) != 2 {
		t.Fatalf("first --all = %+v, %v; want 2", rels1, err)
	}
	// 改图：删 filter 节点（级联删边）——缓存命中时应忽略图变化
	if _, err := r.Exec(`DELETE FROM nodes WHERE id = ?`,
		domain.CanonicalID(funcID+"#ext.sql.table_b.a_id.filter@9")); err != nil {
		t.Fatalf("delete: %v", err)
	}
	rels2, err := r.GetAllTableRelations("")
	if err != nil || len(rels2) != len(rels1) {
		t.Fatalf("缓存命中应返回与首次一致，got %d, %v", len(rels2), err)
	}
	// build_id 变化 → 缓存失效 → 重算（filter 已删 → 无关联）
	if err := r.Save(&domain.BuildMeta{BuildID: "b2", ToolName: "all", Status: "success"}); err != nil {
		t.Fatalf("Save b2: %v", err)
	}
	// Q228：新 build_id 无进度——需重新预计算（filter 已删 → 空结果）
	if err := r.PrecomputeAllRelations(nil); err != nil {
		t.Fatalf("precompute b2: %v", err)
	}
	rels3, err := r.GetAllTableRelations("")
	if err != nil {
		t.Fatalf("b2 --all: %v", err)
	}
	if len(rels3) != 0 {
		t.Fatalf("b2 失效应重算为空，got %+v", rels3)
	}
}
