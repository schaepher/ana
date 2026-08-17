package sqlite

import (
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

// TestGetTableRelationsBridge：循环读出桥（Q152）——BFS 到 ssa_value 节点
// （类型 []example.com/m.Session → Session）时，桥接同函数、同类型的字段
// 读取节点（非外部 read field_access，full_path 含类型名，下游 2 跳可达
// filter 外部节点）：对象读出的值经字段读取后进入 WHERE。
func TestGetTableRelationsBridge(t *testing.T) {
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

		{ID: domain.CanonicalID(funcID + "#n3"), Kind: domain.KindFieldAccess,
			Name: "other", FilePath: "a.go", LineStart: 12,
			Properties: map[string]any{"full_path": "m.Session.token", "access_kind": "read",
				"func_id": funcID, "is_external": "false"}},
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

	rels, err := r.GetTableRelations("table_a", "")
	if err != nil {
		t.Fatalf("GetTableRelations: %v", err)
	}

	if len(rels) != 1 {
		t.Fatalf("rels = %+v, want 1（桥接 table_b.a_id）", rels)
	}
	if rels[0].ToTable != "table_b" || rels[0].ToCol != "a_id" {
		t.Errorf("relation = %+v, want table_b.a_id", rels[0])
	}
	if rels[0].Type != domain.RelationQuery {
		t.Errorf("type = %q, want query（终点 filter）", rels[0].Type)
	}
}

// TestGetTableRelationsXORM：Q175 修复——xorm 外部节点（type_string='xorm'）
// 也要参与关联终点判定（旧实现 byNode 只认 sql/gorm，xorm 表关联全丢）。
func TestGetTableRelationsXORM(t *testing.T) {
	r := newTestRepo(t)
	funcID := "symbol:go:example.com/m:find"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(funcID), Kind: domain.KindFunction, Name: "find"},
		{ID: domain.CanonicalID(funcID + "#ext.xorm.table_a.id.read@6"), Kind: domain.KindFieldAccess,
			Name: "table_a.id", FilePath: "a.go", LineStart: 6,
			Properties: map[string]any{"full_path": "table_a.id", "access_kind": "read",
				"type_string": "xorm", "is_external": "true", "func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#t4"), Kind: domain.KindSSAValue, Name: "t4",
			Properties: map[string]any{"func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#x"), Kind: domain.KindSSAValue, Name: "x",
			Properties: map[string]any{"func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#ext.xorm.table_b.a_id.filter@9"), Kind: domain.KindFieldAccess,
			Name: "table_b.a_id", FilePath: "a.go", LineStart: 9,
			Properties: map[string]any{"full_path": "table_b.a_id", "access_kind": "filter",
				"type_string": "xorm", "is_external": "true", "func_id": funcID}},
	}
	edges := []*domain.Fact{
		{SourceID: domain.CanonicalID(funcID + "#ext.xorm.table_a.id.read@6"), TargetID: domain.CanonicalID(funcID + "#t4"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#t4"), TargetID: domain.CanonicalID(funcID + "#x"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#x"), TargetID: domain.CanonicalID(funcID + "#ext.xorm.table_b.a_id.filter@9"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
	}
	save(t, r, nodes, edges)

	rels, err := r.GetTableRelations("table_a", "")
	if err != nil {
		t.Fatalf("GetTableRelations: %v", err)
	}
	if len(rels) != 1 {
		t.Fatalf("rels = %+v, want 1（xorm 表关联）", rels)
	}
	if rels[0].ToTable != "table_b" || rels[0].Type != domain.RelationQuery {
		t.Errorf("relation = %+v, want table_b.a_id query", rels[0])
	}
}

// TestGetTableRelationsBridgeDirectional：桥 2 跳检查是定向出边
// （旧 SQL EXISTS：e1.source_id = n2.id）——只有"反向边"连到 filter 的
// read 节点不应被桥（双向误桥会引入噪音关联）。
func TestGetTableRelationsBridgeDirectional(t *testing.T) {
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

		{ID: domain.CanonicalID(funcID + "#m"), Kind: domain.KindFieldAccess,
			Name: "st2", FilePath: "a.go", LineStart: 12,
			Properties: map[string]any{"full_path": "m.Session.token", "access_kind": "read",
				"func_id": funcID, "is_external": "false"}},
		{ID: domain.CanonicalID(funcID + "#d"), Kind: domain.KindSSAValue, Name: "d",
			Properties: map[string]any{"func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#ext.sql.table_d.z.read@15"), Kind: domain.KindFieldAccess,
			Name: "table_d.z", FilePath: "a.go", LineStart: 15,
			Properties: map[string]any{"full_path": "table_d.z", "access_kind": "read",
				"type_string": "sql", "is_external": "true", "func_id": funcID}},
	}
	edges := []*domain.Fact{
		{SourceID: domain.CanonicalID(funcID + "#ext.sql.table_a.id.read@6"), TargetID: domain.CanonicalID(funcID + "#t4"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#n2"), TargetID: domain.CanonicalID(funcID + "#x"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#x"), TargetID: domain.CanonicalID(funcID + "#ext.sql.table_b.a_id.filter@10"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},

		{SourceID: domain.CanonicalID(funcID + "#ext.sql.table_b.a_id.filter@10"), TargetID: domain.CanonicalID(funcID + "#m"),
			Kind: domain.FactKind("uses"), ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#m"), TargetID: domain.CanonicalID(funcID + "#d"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#d"), TargetID: domain.CanonicalID(funcID + "#ext.sql.table_d.z.read@15"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
	}
	save(t, r, nodes, edges)

	rels, err := r.GetTableRelations("table_a", "full")
	if err != nil {
		t.Fatalf("GetTableRelations: %v", err)
	}

	if len(rels) != 1 {
		t.Fatalf("rels = %+v, want 仅 table_b.a_id（反向边不触发桥）", rels)
	}
	if rels[0].ToTable != "table_b" {
		t.Errorf("relation = %+v, want table_b.a_id", rels[0])
	}
}

// TestGetTableRelationsTypeRank：同列多节点（read + write）时 Type 取
// rank 最高（query > write > read）——与遍历顺序无关（旧实现只升级 query，
// write 不覆盖 read，结果依赖 map 遍历顺序不确定）。
func TestGetTableRelationsTypeRank(t *testing.T) {
	r := newTestRepo(t)
	funcID := "symbol:go:example.com/m:find"
	mk := func(id, name, access string, line int) *domain.CodeEntity {
		return &domain.CodeEntity{
			ID: domain.CanonicalID(funcID + id), Kind: domain.KindFieldAccess,
			Name: name, FilePath: "a.go", LineStart: line,
			Properties: map[string]any{"full_path": name, "access_kind": access,
				"type_string": "sql", "is_external": "true", "func_id": funcID},
		}
	}
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(funcID), Kind: domain.KindFunction, Name: "find"},

		mk("#ext.sql.table_a.id.read@6", "table_a.id", "read", 6),

		mk("#v1", "v1", "", 7), mk("#v2", "v2", "", 8), mk("#v3", "v3", "", 9),
		mk("#w1", "w1", "", 10), mk("#w2", "w2", "", 11),
		mk("#ext.sql.table_b.a_id.read@12", "table_b.a_id", "read", 12),
		mk("#ext.sql.table_b.a_id.write@13", "table_b.a_id", "write", 13),
	}
	edges := []*domain.Fact{
		{SourceID: domain.CanonicalID(funcID + "#ext.sql.table_a.id.read@6"), TargetID: domain.CanonicalID(funcID + "#v1"), Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#v1"), TargetID: domain.CanonicalID(funcID + "#v2"), Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#v2"), TargetID: domain.CanonicalID(funcID + "#ext.sql.table_b.a_id.read@12"), Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#v1"), TargetID: domain.CanonicalID(funcID + "#w1"), Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#w1"), TargetID: domain.CanonicalID(funcID + "#w2"), Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#w2"), TargetID: domain.CanonicalID(funcID + "#ext.sql.table_b.a_id.write@13"), Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
	}
	save(t, r, nodes, edges)
	rels, err := r.GetTableRelations("table_a", "")
	if err != nil {
		t.Fatalf("GetTableRelations: %v", err)
	}
	if len(rels) != 1 {
		t.Fatalf("rels = %+v, want 1（table_b.a_id）", rels)
	}

	if rels[0].Type != domain.RelationWrite {
		t.Errorf("type = %q, want write（read+write 并存取 rank 最高）", rels[0].Type)
	}
}
