package sqlite

import (
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

// TestRelationCacheHopsWiden：Q208 缓存与 hops 参数——缓存必须存
// 未过滤全量，hops 过滤是读取期行为。此前缓存写入 dedup 后的行：
// 首次窄参数查询后放宽 q_hops 无法展示长链（长链行没进缓存）。
func TestRelationCacheHopsWiden(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	r := NewRepo(db)

	funcID := "symbol:go:example.com/m:find"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(funcID), Kind: domain.KindFunction, Name: "find", FilePath: "a.go"},
		// 5 跳 query 链：a.id read → t1 → t2 → t3 → t4 → b.a_id filter
		{ID: domain.CanonicalID(funcID + "#ext.sql.table_a.id.read@6"), Kind: domain.KindFieldAccess,
			Name: "table_a.id", FilePath: "a.go", LineStart: 6,
			Properties: map[string]any{"full_path": "table_a.id", "instance_path": "table_a.id",
				"access_kind": "read", "type_string": "sql", "is_external": "true", "func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#ext.sql.table_b.a_id.filter@9"), Kind: domain.KindFieldAccess,
			Name: "table_b.a_id", FilePath: "a.go", LineStart: 9,
			Properties: map[string]any{"full_path": "table_b.a_id", "instance_path": "table_b.a_id",
				"access_kind": "filter", "type_string": "sql", "is_external": "true", "func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#t1"), Kind: domain.KindSSAValue, Name: "t1",
			Properties: map[string]any{"func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#t2"), Kind: domain.KindSSAValue, Name: "t2",
			Properties: map[string]any{"func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#t3"), Kind: domain.KindSSAValue, Name: "t3",
			Properties: map[string]any{"func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#t4"), Kind: domain.KindSSAValue, Name: "t4",
			Properties: map[string]any{"func_id": funcID}},
	}
	edges := []*domain.Fact{
		{SourceID: domain.CanonicalID(funcID + "#ext.sql.table_a.id.read@6"), TargetID: domain.CanonicalID(funcID + "#t1"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#t1"), TargetID: domain.CanonicalID(funcID + "#t2"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#t2"), TargetID: domain.CanonicalID(funcID + "#t3"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#t3"), TargetID: domain.CanonicalID(funcID + "#t4"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#t4"), TargetID: domain.CanonicalID(funcID + "#ext.sql.table_b.a_id.filter@9"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
	}
	if _, err := r.SaveBatchStats(nodes, edges, nil); err != nil {
		t.Fatalf("save: %v", err)
	}
	// build_metadata：让 relation_candidates 缓存生效（fixture 否则无 build_id 不缓存）
	if err := r.Save(&domain.BuildMeta{BuildID: "b1", CommitSHA: "c1", ToolName: "all", Status: "success"}); err != nil {
		t.Fatalf("save meta: %v", err)
	}

	// ① 首次窄参数（默认 4）：5 跳被滤
	rels, err := r.GetTableRelations("table_a", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range rels {
		t.Logf("默认 4 跳 rel: %s.%s -> %s.%s hops=%d type=%s", rel.FromTable, rel.FromCol, rel.ToTable, rel.ToCol, rel.Hops, rel.Type)
	}
	if len(rels) != 0 {
		t.Fatalf("默认 4 跳应滤 5 跳 query，got %d 条", len(rels))
	}
	// ② 放宽 q_hops=0：缓存命中后应能展示 5 跳长链
	r.SetRelationHops(domain.RelationHops{Query: 0, Write: 4, Read: 4})
	rels, err = r.GetTableRelations("table_a", "")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, rel := range rels {
		if rel.FromTable == "table_a" && rel.FromCol == "id" &&
			rel.ToTable == "table_b" && rel.ToCol == "a_id" && rel.Hops == 5 {
			found = true
		}
	}
	if !found {
		t.Errorf("放宽 q_hops=0 后 5 跳长链不可见（缓存存了过滤后行）：%v", rels)
	}
}
