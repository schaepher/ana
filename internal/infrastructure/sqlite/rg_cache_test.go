package sqlite

import (
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

// TestRelationMemoryModes：内存 BFS（full）与逐节点 SQL（sql）两路径
// 结果一致（P0④ --memory 参数：大仓库强制 SQL 防爆内存）。
func TestRelationMemoryModes(t *testing.T) {
	r := newTestRepo(t)
	funcID := "symbol:go:example.com/m:find"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(funcID), Kind: domain.KindFunction, Name: "find"},
		{ID: domain.CanonicalID(funcID + "#ext.sql.table_a.id.read@6"), Kind: domain.KindFieldAccess,
			Name: "table_a.id", FilePath: "a.go", LineStart: 6,
			Properties: map[string]any{"full_path": "table_a.id", "access_kind": "read",
				"type_string": "sql", "is_external": "true", "func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#t4"), Kind: domain.KindSSAValue, Name: "t4",
			Properties: map[string]any{"func_id": funcID, "type_string": "[]example.com/m.Session"}},
		{ID: domain.CanonicalID(funcID + "#n2"), Kind: domain.KindFieldAccess,
			Name: "st", FilePath: "a.go", LineStart: 8,
			Properties: map[string]any{"full_path": "m.Session.status", "access_kind": "read",
				"func_id": funcID, "is_external": "false"}},
		{ID: domain.CanonicalID(funcID + "#x"), Kind: domain.KindSSAValue, Name: "x",
			Properties: map[string]any{"func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#ext.sql.table_b.a_id.filter@10"), Kind: domain.KindFieldAccess,
			Name: "table_b.a_id", FilePath: "a.go", LineStart: 10,
			Properties: map[string]any{"full_path": "table_b.a_id", "access_kind": "filter",
				"type_string": "sql", "is_external": "true", "func_id": funcID}},
	}
	edges := []*domain.Fact{
		{SourceID: domain.CanonicalID(funcID + "#ext.sql.table_a.id.read@6"), TargetID: domain.CanonicalID(funcID + "#t4"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#n2"), TargetID: domain.CanonicalID(funcID + "#x"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#x"), TargetID: domain.CanonicalID(funcID + "#ext.sql.table_b.a_id.filter@10"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
	}
	save(t, r, nodes, edges)

	full, err := r.GetTableRelations("table_a", "full")
	if err != nil {
		t.Fatalf("full: %v", err)
	}
	sqlRels, err := r.GetTableRelations("table_a", "sql")
	if err != nil {
		t.Fatalf("sql: %v", err)
	}
	if len(full) != 1 || len(sqlRels) != 1 {
		t.Fatalf("full=%+v sql=%+v, want 各 1 条", full, sqlRels)
	}
	a, b := full[0], sqlRels[0]
	if a.FromCol != b.FromCol || a.ToTable != b.ToTable || a.ToCol != b.ToCol ||
		a.Hops != b.Hops || a.Type != b.Type {
		t.Errorf("full=%+v sql=%+v 不一致", a, b)
	}

	auto, err := r.GetTableRelations("table_a", "")
	if err != nil || len(auto) != 1 {
		t.Fatalf("auto: %v %v", auto, err)
	}
}

// TestBuildMetaCounts：节点/边数随构建元数据缓存（--memory auto 判断用，
// 不每次重新 COUNT）。
func TestBuildMetaCounts(t *testing.T) {
	r := newTestRepo(t)
	if err := r.Save(&domain.BuildMeta{BuildID: "b1", ToolName: "all", Status: "success", Nodes: 100, Edges: 50}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	m, err := r.GetLatest()
	if err != nil {
		t.Fatalf("GetLatest: %v", err)
	}
	if m.Nodes != 100 || m.Edges != 50 {
		t.Errorf("counts = %d/%d, want 100/50", m.Nodes, m.Edges)
	}
}

// TestRelationCandidatesCache：relation_candidates 缓存语义（P0③）——
// ① 有 build_id 时单表结果写缓存，图状态变化后仍返回缓存；
// ② build_id 变化 → 缓存失效 → 现场重算；
// ③ 无 build_metadata 时跳过缓存（不写行）。
func TestRelationCandidatesCache(t *testing.T) {
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

	rels, err := r.GetTableRelations("table_a", "")
	if err != nil || len(rels) != 1 {
		t.Fatalf("first rels = %v, %v", rels, err)
	}
	var cnt int
	if err := r.QueryRow(`SELECT COUNT(*) FROM relation_candidates`).Scan(&cnt); err != nil {
		t.Fatalf("count relation_candidates: %v", err)
	}
	if cnt != 0 {
		t.Fatalf("无 build_id 不应写缓存，got %d 行", cnt)
	}

	if err := r.Save(&domain.BuildMeta{BuildID: "b1", ToolName: "all", Status: "success"}); err != nil {
		t.Fatalf("Save build: %v", err)
	}
	rels, err = r.GetTableRelations("table_a", "")
	if err != nil || len(rels) != 1 {
		t.Fatalf("b1 rels = %v, %v", rels, err)
	}
	if _, err := r.Exec(`DELETE FROM nodes WHERE id = ?`, domain.CanonicalID(funcID+"#ext.sql.table_b.a_id.filter@9")); err != nil {
		t.Fatalf("delete node: %v", err)
	}
	rels, err = r.GetTableRelations("table_a", "")
	if err != nil || len(rels) != 1 {
		t.Fatalf("缓存命中应仍返回 1 条，got %v, %v", rels, err)
	}

	if err := r.Save(&domain.BuildMeta{BuildID: "b2", ToolName: "all", Status: "success"}); err != nil {
		t.Fatalf("Save build b2: %v", err)
	}
	rels, err = r.GetTableRelations("table_a", "")
	if err != nil {
		t.Fatalf("b2 rels: %v", err)
	}
	if len(rels) != 0 {
		t.Fatalf("b2 缓存失效应重算为 0 条，got %+v", rels)
	}
}

// TestGetAllTableRelationsRebuildCache：--all 全量重建缓存——先算完
// 单表（缓存只有 table_a），--all 后 relation_candidates 覆盖为全部表。
func TestGetAllTableRelationsRebuildCache(t *testing.T) {
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

	if _, err := r.GetTableRelations("table_a", ""); err != nil {
		t.Fatalf("GetTableRelations: %v", err)
	}
	rels, err := r.GetAllTableRelations("")
	if err != nil {
		t.Fatalf("GetAllTableRelations: %v", err)
	}

	if len(rels) != 2 {
		t.Fatalf("rels = %+v, want 2", rels)
	}
	var fromTables int
	if err := r.QueryRow(`SELECT COUNT(DISTINCT from_table) FROM relation_candidates WHERE build_id = 'b1'`).Scan(&fromTables); err != nil {
		t.Fatalf("count: %v", err)
	}
	if fromTables != 2 {
		t.Fatalf("--all 后缓存应覆盖 2 张表，got %d", fromTables)
	}
}
