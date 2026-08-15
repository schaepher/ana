package sqlite

import (
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

// TestGetTableColumns：query table——按表名聚合列虚拟节点（Name=表 或 表.列），
// 每列带写入方（summary_io 入边 source 函数 + 行号）。
func TestGetTableColumns(t *testing.T) {
	r := newTestRepo(t)
	funcID := "symbol:go:example.com/m:save"
	valID := funcID + "#t0"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(funcID), Kind: domain.KindFunction, Name: "save", FilePath: "a.go"},
		{ID: domain.CanonicalID(valID), Kind: domain.KindSSAValue, Name: "t0",
			Properties: map[string]any{"func_id": funcID}},
		// users 表的列虚拟节点（Q97 持久化映射形态）
		{ID: domain.CanonicalID(funcID + "#ext.sql.users.name.write@5"), Kind: domain.KindFieldAccess,
			Name: "users.name", FilePath: "a.go", LineStart: 5,
			Properties: map[string]any{"full_path": "users.name", "instance_path": "users.name",
				"access_kind": "write", "type_string": "sql", "is_external": "true", "func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#ext.sql.users.age.write@6"), Kind: domain.KindFieldAccess,
			Name: "users.age", FilePath: "a.go", LineStart: 6,
			Properties: map[string]any{"full_path": "users.age", "instance_path": "users.age",
				"access_kind": "write", "type_string": "sql", "is_external": "true", "func_id": funcID}},
		// 干扰：其他表的虚拟节点 + 非虚拟 field_access
		{ID: domain.CanonicalID(funcID + "#ext.sql.orders.id.write@7"), Kind: domain.KindFieldAccess,
			Name: "orders.id", FilePath: "a.go", LineStart: 7,
			Properties: map[string]any{"full_path": "orders.id", "access_kind": "write", "type_string": "sql"}},
		{ID: domain.CanonicalID(funcID + "#u.Name.write@8"), Kind: domain.KindFieldAccess,
			Name: "u.Name", FilePath: "a.go", LineStart: 8,
			Properties: map[string]any{"full_path": "example.com/m.User.Name", "access_kind": "write"}},
	}
	edges := []*domain.Fact{
		{SourceID: domain.CanonicalID(valID), TargetID: domain.CanonicalID(funcID + "#ext.sql.users.name.write@5"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1,
			Metadata: map[string]any{"line_num": 5}},
		{SourceID: domain.CanonicalID(valID), TargetID: domain.CanonicalID(funcID + "#ext.sql.users.age.write@6"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1,
			Metadata: map[string]any{"line_num": 6}},
	}
	save(t, r, nodes, edges)

	cols, err := r.GetTableColumns("users")
	if err != nil {
		t.Fatalf("GetTableColumns: %v", err)
	}
	if len(cols) != 2 {
		t.Fatalf("cols = %d, want 2（orders.id 与普通字段应过滤）", len(cols))
	}
	if cols[0].Name != "users.age" || cols[1].Name != "users.name" {
		t.Fatalf("排序: %v", []string{cols[0].Name, cols[1].Name})
	}
	if len(cols[1].Writers) != 1 {
		t.Fatalf("users.name writers = %d, want 1", len(cols[1].Writers))
	}
	if cols[1].Writers[0].FuncID != funcID || cols[1].Writers[0].Line != 5 {
		t.Errorf("users.name writer = %+v", cols[1].Writers[0])
	}

	// 无虚拟节点的表 → 空不报错
	empty, err := r.GetTableColumns("nope")
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty table = %v, %v", empty, err)
	}
}
