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
		{ID: domain.CanonicalID(funcID + "#ext.sql.table_a.id.read@6"), Kind: domain.KindFieldAccess,
			Name: "table_a.id", FilePath: "a.go", LineStart: 6,
			Properties: map[string]any{"full_path": "table_a.id", "access_kind": "read",
				"type_string": "sql", "is_external": "true", "func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#t4"), Kind: domain.KindSSAValue, Name: "t4",
			Properties: map[string]any{"func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#x"), Kind: domain.KindSSAValue, Name: "id",
			Properties: map[string]any{"func_id": funcID}},
		// 表 B 过滤虚拟节点
		{ID: domain.CanonicalID(funcID + "#ext.sql.table_b.a_id.filter@9"), Kind: domain.KindFieldAccess,
			Name: "table_b.a_id", FilePath: "a.go", LineStart: 9,
			Properties: map[string]any{"full_path": "table_b.a_id", "access_kind": "filter",
				"type_string": "sql", "is_external": "true", "func_id": funcID}},
		// 干扰：无数据流链的表 C
		{ID: domain.CanonicalID(funcID + "#ext.sql.table_c.z.read@20"), Kind: domain.KindFieldAccess,
			Name: "table_c.z", FilePath: "a.go", LineStart: 20,
			Properties: map[string]any{"full_path": "table_c.z", "access_kind": "read",
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
	if err != nil {
		t.Fatalf("GetTableRelations: %v", err)
	}
	if len(rels) != 1 {
		t.Fatalf("rels = %+v, want 1（table_b）", rels)
	}
	if rels[0].ToTable != "table_b" || rels[0].ToCol != "a_id" || rels[0].FromCol != "id" {
		t.Errorf("relation = %+v, want table_b.a_id ← table_a.id", rels[0])
	}
	if rels[0].Hops == 0 {
		t.Error("hops 应为数据流链长度（>0）")
	}
	// 终点是 filter 虚拟节点 → query 类型（键关联，高置信）
	if rels[0].Type != domain.RelationQuery {
		t.Errorf("relation type = %q, want query（终点 filter 列）", rels[0].Type)
	}
	// table_c 无链 → 不出现；无关联表 → 空
	empty, err := r.GetTableRelations("table_c", "")
	if err != nil || len(empty) != 0 {
		t.Fatalf("table_c rels = %v, %v", empty, err)
	}
}

// TestGetTables：全库表枚举（外部 gorm/sql 虚拟节点表名去重）。
func TestGetTables(t *testing.T) {
	r := newTestRepo(t)
	funcID := "symbol:go:example.com/m:find"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(funcID), Kind: domain.KindFunction, Name: "find"},
		{ID: domain.CanonicalID(funcID + "#ext.sql.table_a.id.read@6"), Kind: domain.KindFieldAccess,
			Name: "table_a.id", FilePath: "a.go", LineStart: 6,
			Properties: map[string]any{"full_path": "table_a.id", "access_kind": "read",
				"type_string": "sql", "is_external": "true", "func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#ext.sql.table_a.title.read@7"), Kind: domain.KindFieldAccess,
			Name: "table_a.title", FilePath: "a.go", LineStart: 7,
			Properties: map[string]any{"full_path": "table_a.title", "access_kind": "read",
				"type_string": "sql", "is_external": "true", "func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#ext.gorm.table_b.id.read@8"), Kind: domain.KindFieldAccess,
			Name: "table_b.id", FilePath: "a.go", LineStart: 8,
			Properties: map[string]any{"full_path": "table_b.id", "access_kind": "read",
				"type_string": "gorm", "is_external": "true", "func_id": funcID}},
		// 非外部/非 gorm-sql → 不计表
		{ID: domain.CanonicalID(funcID + "#ext.sql.table_c.z.read@9"), Kind: domain.KindFieldAccess,
			Name: "table_c.z", FilePath: "a.go", LineStart: 9,
			Properties: map[string]any{"full_path": "table_c.z", "access_kind": "read",
				"type_string": "sql", "is_external": "false", "func_id": funcID}},
	}
	save(t, r, nodes, nil)

	got, err := r.GetTables()
	if err != nil {
		t.Fatalf("GetTables: %v", err)
	}
	want := []string{"table_a", "table_b"}
	if len(got) != len(want) {
		t.Fatalf("tables = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("tables[%d] = %q, want %q（排序、去重）", i, got[i], want[i])
		}
	}
}

// TestGetAllTableRelations：全库聚合——每表 BFS 结果合并去重
// （同 from/to 列对取 hops 最小 + Type 最高），含反向 read 关联。
func TestGetAllTableRelations(t *testing.T) {
	r := newTestRepo(t)
	funcID := "symbol:go:example.com/m:find"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(funcID), Kind: domain.KindFunction, Name: "find"},
		// 表 A 读虚拟节点 + row 值 + x 变量
		{ID: domain.CanonicalID(funcID + "#ext.sql.table_a.id.read@6"), Kind: domain.KindFieldAccess,
			Name: "table_a.id", FilePath: "a.go", LineStart: 6,
			Properties: map[string]any{"full_path": "table_a.id", "access_kind": "read",
				"type_string": "sql", "is_external": "true", "func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#t4"), Kind: domain.KindSSAValue, Name: "t4",
			Properties: map[string]any{"func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#x"), Kind: domain.KindSSAValue, Name: "id",
			Properties: map[string]any{"func_id": funcID}},
		// 表 B 过滤虚拟节点
		{ID: domain.CanonicalID(funcID + "#ext.sql.table_b.a_id.filter@9"), Kind: domain.KindFieldAccess,
			Name: "table_b.a_id", FilePath: "a.go", LineStart: 9,
			Properties: map[string]any{"full_path": "table_b.a_id", "access_kind": "filter",
				"type_string": "sql", "is_external": "true", "func_id": funcID}},
		// 干扰：无数据流链的表 C
		{ID: domain.CanonicalID(funcID + "#ext.sql.table_c.z.read@20"), Kind: domain.KindFieldAccess,
			Name: "table_c.z", FilePath: "a.go", LineStart: 20,
			Properties: map[string]any{"full_path": "table_c.z", "access_kind": "read",
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

	rels, err := r.GetAllTableRelations("")
	if err != nil {
		t.Fatalf("GetAllTableRelations: %v", err)
	}
	// 正向：table_a.id → table_b.a_id（query 键关联）；反向：table_b.a_id → table_a.id（read）
	if len(rels) != 2 {
		t.Fatalf("rels = %+v, want 2（正向 query + 反向 read）", rels)
	}
	fwd, bwd := rels[0], rels[1]
	if fwd.FromTable != "table_a" || fwd.FromCol != "id" || fwd.ToTable != "table_b" || fwd.ToCol != "a_id" {
		t.Errorf("fwd = %+v, want table_a.id → table_b.a_id", fwd)
	}
	if fwd.Type != domain.RelationQuery {
		t.Errorf("fwd type = %q, want query", fwd.Type)
	}
	if bwd.FromTable != "table_b" || bwd.FromCol != "a_id" || bwd.ToTable != "table_a" || bwd.ToCol != "id" {
		t.Errorf("bwd = %+v, want table_b.a_id → table_a.id", bwd)
	}
	if bwd.Type != domain.RelationRead {
		t.Errorf("bwd type = %q, want read（起点非 filter）", bwd.Type)
	}
}
