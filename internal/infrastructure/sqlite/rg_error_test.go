package sqlite

import (
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

// Q220b：error 返回值链误串表关联——多返回值元组 (T, error) 共享节点，
// err 元素与业务值元素（如 (int, error) 的 int）被无向边连在一起：
// a.id → 元组 → err → ... → 元组 → pay_order.id → b.item_id 假链。
// error 值不携带业务列值，BFS 不得穿越 error 节点。
//
// go2o 实测假链：approval_log.id.write → current (*ApprovalLog, error)
// → err（error 元素）→ 跨函数 err 传播（errorV2 → DivideSuccess）→
// (int, error) 元组 → id（支付单 id）→ ... → pay_divide.pay_id [12跳 fk]。
func TestRelationErrorValueBlocked(t *testing.T) {
	r := newTestRepo(t)
	fA := "symbol:go:example.com/m:load"
	fB := "symbol:go:example.com/m:save"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(fA), Kind: domain.KindFunction, Name: "load"},
		{ID: domain.CanonicalID(fB), Kind: domain.KindFunction, Name: "save"},
		// 起点：a.id 读出（对象字段写兜底连到元组）
		{ID: domain.CanonicalID(fA + "#ext.sql.table_a.id.read@6"), Kind: domain.KindFieldAccess,
			Name: "table_a.id", FilePath: "a.go", LineStart: 6,
			Properties: map[string]any{"full_path": "table_a.id", "access_kind": "read",
				"type_string": "sql", "is_external": "true", "func_id": fA}},
		// 元组 (*X, error)：err 元素与 X 元素共享节点
		{ID: domain.CanonicalID(fA + "#tup1"), Kind: domain.KindSSAValue, Name: "tup1",
			Properties: map[string]any{"func_id": fA, "type_string": "(*example.com/m.X, error)"}},
		{ID: domain.CanonicalID(fA + "#err1"), Kind: domain.KindSSAValue, Name: "err1",
			Properties: map[string]any{"func_id": fA, "type_string": "error"}},
		// 跨函数 err 传播（errorV2 形态）
		{ID: domain.CanonicalID(fA + "#err2"), Kind: domain.KindSSAValue, Name: "err2",
			Properties: map[string]any{"func_id": fA, "type_string": "error"}},
		{ID: domain.CanonicalID(fB + "#tup2"), Kind: domain.KindSSAValue, Name: "tup2",
			Properties: map[string]any{"func_id": fB, "type_string": "(int, error)"}},
		{ID: domain.CanonicalID(fB + "#idv"), Kind: domain.KindSSAValue, Name: "idv",
			Properties: map[string]any{"func_id": fB, "type_string": "int"}},
		{ID: domain.CanonicalID(fB + "#ext.sql.table_b.item_id.filter@20"), Kind: domain.KindFieldAccess,
			Name: "table_b.item_id", FilePath: "b.go", LineStart: 20,
			Properties: map[string]any{"full_path": "table_b.item_id", "access_kind": "filter",
				"type_string": "sql", "is_external": "true", "func_id": fB}},
		// 合法链对照：a.id → v3 → c.order_id（无 error 穿越，仍 fk）
		{ID: domain.CanonicalID(fA + "#v3"), Kind: domain.KindSSAValue, Name: "v3",
			Properties: map[string]any{"func_id": fA, "type_string": "int64"}},
		{ID: domain.CanonicalID(fB + "#ext.sql.table_c.order_id.filter@30"), Kind: domain.KindFieldAccess,
			Name: "table_c.order_id", FilePath: "c.go", LineStart: 30,
			Properties: map[string]any{"full_path": "table_c.order_id", "access_kind": "filter",
				"type_string": "sql", "is_external": "true", "func_id": fB}},
	}
	edges := []*domain.Fact{
		// 假链：a.id.read → 元组 → err → err → 元组 → idv → b.item_id
		{SourceID: domain.CanonicalID(fA + "#ext.sql.table_a.id.read@6"), TargetID: domain.CanonicalID(fA + "#tup1"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(fA + "#tup1"), TargetID: domain.CanonicalID(fA + "#err1"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(fA + "#err1"), TargetID: domain.CanonicalID(fA + "#err2"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(fA + "#err2"), TargetID: domain.CanonicalID(fB + "#tup2"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(fB + "#tup2"), TargetID: domain.CanonicalID(fB + "#idv"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(fB + "#idv"), TargetID: domain.CanonicalID(fB + "#ext.sql.table_b.item_id.filter@20"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
		// 合法链
		{SourceID: domain.CanonicalID(fA + "#ext.sql.table_a.id.read@6"), TargetID: domain.CanonicalID(fA + "#v3"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(fA + "#v3"), TargetID: domain.CanonicalID(fB + "#ext.sql.table_c.order_id.filter@30"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
	}
	save(t, r, nodes, edges)

	rels, err := r.GetTableRelations("table_a", "full")
	if err != nil {
		t.Fatalf("full: %v", err)
	}
	for _, rel := range rels {
		if rel.ToTable == "table_b" && rel.ToCol == "item_id" {
			t.Fatalf("error 链不得产生关联（a.id → err → 元组 → b.item_id），got %s", rel.Type)
		}
	}
	// 合法链不受影响
	found := false
	for _, rel := range rels {
		if rel.ToTable == "table_c" && rel.ToCol == "order_id" {
			found = true
			if rel.Type != domain.RelationFK {
				t.Fatalf("合法链应标 fk，got %s", rel.Type)
			}
		}
	}
	if !found {
		t.Fatal("合法链 a.id → c.order_id 应保留")
	}
}
