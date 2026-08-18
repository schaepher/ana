package sqlite

import (
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

// Q212 SQL 路径（relationsForSQL）同步 Q202 值级 taint：跨函数 write
// 终点判定与内存路径一致（外键形态回退 + taint 呼应 + 外键列呼应表名），
// filterFKNoise 统一（query 豁免）。

// TestRelationSQLCrossFuncWrite：跨函数（argument 边）write 终点——
// 值流 taint 呼应（id → order_id）保留，无呼应（create_time → res_id）
// 丢弃；SQL 路径与内存路径一致。
func TestRelationSQLCrossFuncWrite(t *testing.T) {
	r := newTestRepo(t)
	fA := "symbol:go:example.com/m:loadOrder"
	fB := "symbol:go:example.com/m:saveOrder"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(fA), Kind: domain.KindFunction, Name: "loadOrder"},
		{ID: domain.CanonicalID(fB), Kind: domain.KindFunction, Name: "saveOrder"},
		// 起点表 table_a：id / create_time 两列读出（taint 起点）
		{ID: domain.CanonicalID(fA + "#ext.sql.tb_order.id.read@6"), Kind: domain.KindFieldAccess,
			Name: "tb_order.id", FilePath: "a.go", LineStart: 6,
			Properties: map[string]any{"full_path": "tb_order.id", "access_kind": "read",
				"type_string": "sql", "is_external": "true", "func_id": fA}},
		{ID: domain.CanonicalID(fA + "#ext.sql.tb_order.create_time.read@7"), Kind: domain.KindFieldAccess,
			Name: "tb_order.create_time", FilePath: "a.go", LineStart: 7,
			Properties: map[string]any{"full_path": "tb_order.create_time", "access_kind": "read",
				"type_string": "sql", "is_external": "true", "func_id": fA}},
		// 值链：读出值 → 跨函数参数 → 被调函数内写入（taint 延续）
		{ID: domain.CanonicalID(fA + "#v1"), Kind: domain.KindSSAValue, Name: "v1",
			Properties: map[string]any{"func_id": fA, "type_string": "int64"}},
		{ID: domain.CanonicalID(fA + "#v2"), Kind: domain.KindSSAValue, Name: "v2",
			Properties: map[string]any{"func_id": fA, "type_string": "int64"}},
		{ID: domain.CanonicalID(fB + "#p1"), Kind: domain.KindSSAValue, Name: "p1",
			Properties: map[string]any{"func_id": fB, "type_string": "int64"}},
		{ID: domain.CanonicalID(fA + "#c1"), Kind: domain.KindSSAValue, Name: "c1",
			Properties: map[string]any{"func_id": fA, "type_string": "int64"}},
		{ID: domain.CanonicalID(fA + "#c2"), Kind: domain.KindSSAValue, Name: "c2",
			Properties: map[string]any{"func_id": fA, "type_string": "int64"}},
		{ID: domain.CanonicalID(fB + "#p2"), Kind: domain.KindSSAValue, Name: "p2",
			Properties: map[string]any{"func_id": fB, "type_string": "int64"}},
		// 终点：taint 呼应保留（order_id 含 id + 外键形态呼应 tb_order）
		{ID: domain.CanonicalID(fB + "#ext.sql.tb_extra.order_id.write@20"), Kind: domain.KindFieldAccess,
			Name: "tb_extra.order_id", FilePath: "b.go", LineStart: 20,
			Properties: map[string]any{"full_path": "tb_extra.order_id", "access_kind": "write",
				"type_string": "sql", "is_external": "true", "func_id": fB}},
		// 终点：无呼应丢弃（create_time 不呼应 res_id）
		{ID: domain.CanonicalID(fB + "#ext.sql.tb_extra.res_id.write@30"), Kind: domain.KindFieldAccess,
			Name: "tb_extra.res_id", FilePath: "b.go", LineStart: 30,
			Properties: map[string]any{"full_path": "tb_extra.res_id", "access_kind": "write",
				"type_string": "sql", "is_external": "true", "func_id": fB}},
	}
	edges := []*domain.Fact{
		{SourceID: domain.CanonicalID(fA + "#ext.sql.tb_order.id.read@6"), TargetID: domain.CanonicalID(fA + "#v1"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(fA + "#v1"), TargetID: domain.CanonicalID(fA + "#v2"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(fA + "#v2"), TargetID: domain.CanonicalID(fB + "#p1"),
			Kind: domain.FactArgument, ToolSource: domain.ToolSSA, Confidence: 1}, // 跨函数
		{SourceID: domain.CanonicalID(fB + "#p1"), TargetID: domain.CanonicalID(fB + "#ext.sql.tb_extra.order_id.write@20"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(fA + "#ext.sql.tb_order.create_time.read@7"), TargetID: domain.CanonicalID(fA + "#c1"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(fA + "#c1"), TargetID: domain.CanonicalID(fA + "#c2"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(fA + "#c2"), TargetID: domain.CanonicalID(fB + "#p2"),
			Kind: domain.FactArgument, ToolSource: domain.ToolSSA, Confidence: 1}, // 跨函数
		{SourceID: domain.CanonicalID(fB + "#p2"), TargetID: domain.CanonicalID(fB + "#ext.sql.tb_extra.res_id.write@30"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
	}
	save(t, r, nodes, edges)

	full, err := r.GetTableRelations("tb_order", "full")
	if err != nil {
		t.Fatalf("full: %v", err)
	}
	sqlRels, err := r.GetTableRelations("tb_order", "sql")
	if err != nil {
		t.Fatalf("sql: %v", err)
	}
	// 内存路径语义：order_id 保留（taint id 呼应 + 外键形态），
	// res_id 丢弃（create_time 不呼应 + 非主键起点）

	if len(full) != 1 || len(sqlRels) != 1 {
		t.Fatalf("full=%+v sql=%+v, want 各 1 条（仅 order_id）", full, sqlRels)
	}
	for _, rel := range append(append([]*domain.TableRelation{}, full...), sqlRels...) {
		if rel.ToTable != "tb_extra" || rel.ToCol != "order_id" {
			t.Fatalf("应只保留 tb_extra.order_id，got %+v", rel)
		}
		if rel.Type != domain.RelationWrite {
			t.Fatalf("应为 write 类型，got %s", rel.Type)
		}
	}
}

// TestRelationSQLFilterFKNoiseQueryExempt：filterFKNoise 的 query 豁免
// （Q205：hasFK 时 id 起点过滤不作用于 query——attr.id → attr_item.attr_id
// 是真实键关联）——SQL 路径（内联版漏豁免）与内存路径一致。
func TestRelationSQLFilterFKNoiseQueryExempt(t *testing.T) {
	r := newTestRepo(t)
	funcID := "symbol:go:example.com/m:find"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(funcID), Kind: domain.KindFunction, Name: "find"},
		{ID: domain.CanonicalID(funcID + "#ext.sql.attr.id.read@6"), Kind: domain.KindFieldAccess,
			Name: "attr.id", FilePath: "a.go", LineStart: 6,
			Properties: map[string]any{"full_path": "attr.id", "access_kind": "read",
				"type_string": "sql", "is_external": "true", "func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#ext.sql.attr.name.read@7"), Kind: domain.KindFieldAccess,
			Name: "attr.name", FilePath: "a.go", LineStart: 7,
			Properties: map[string]any{"full_path": "attr.name", "access_kind": "read",
				"type_string": "sql", "is_external": "true", "func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#v1"), Kind: domain.KindSSAValue, Name: "v1",
			Properties: map[string]any{"func_id": funcID, "type_string": "int64"}},
		{ID: domain.CanonicalID(funcID + "#v2"), Kind: domain.KindSSAValue, Name: "v2",
			Properties: map[string]any{"func_id": funcID, "type_string": "string"}},
		{ID: domain.CanonicalID(funcID + "#ext.sql.attr_item.attr_id.filter@10"), Kind: domain.KindFieldAccess,
			Name: "attr_item.attr_id", FilePath: "a.go", LineStart: 10,
			Properties: map[string]any{"full_path": "attr_item.attr_id", "access_kind": "filter",
				"type_string": "sql", "is_external": "true", "func_id": funcID}},
	}
	edges := []*domain.Fact{
		{SourceID: domain.CanonicalID(funcID + "#ext.sql.attr.id.read@6"), TargetID: domain.CanonicalID(funcID + "#v1"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#v1"), TargetID: domain.CanonicalID(funcID + "#ext.sql.attr_item.attr_id.filter@10"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#ext.sql.attr.name.read@7"), TargetID: domain.CanonicalID(funcID + "#v2"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#v2"), TargetID: domain.CanonicalID(funcID + "#ext.sql.attr_item.attr_id.filter@10"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
	}
	save(t, r, nodes, edges)

	full, err := r.GetTableRelations("attr", "full")
	if err != nil {
		t.Fatalf("full: %v", err)
	}
	sqlRels, err := r.GetTableRelations("attr", "sql")
	if err != nil {
		t.Fatalf("sql: %v", err)
	}
	// attr.id → attr_item.attr_id 是键关联（Q218 值流验证 → fk）——
	// hasFK（name 起点）时 id 起点过滤豁免；SQL 路径内联版缺豁免会丢弃
	var fullFK, sqlFK bool
	for _, rel := range full {
		if rel.FromCol == "id" && rel.ToCol == "attr_id" && rel.Type == domain.RelationFK {
			fullFK = true
		}
	}
	for _, rel := range sqlRels {
		if rel.FromCol == "id" && rel.ToCol == "attr_id" && rel.Type == domain.RelationFK {
			sqlFK = true
		}
	}
	if !fullFK || !sqlFK {
		t.Fatalf("id→attr_id fk 应保留（豁免）：full fk=%v sql fk=%v（full=%+v sql=%+v）",
			fullFK, sqlFK, full, sqlRels)
	}
	if len(full) != len(sqlRels) {
		t.Fatalf("两路径结果应一致：full=%d sql=%d（full=%+v sql=%+v）", len(full), len(sqlRels), full, sqlRels)
	}
}
