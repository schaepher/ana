package sqlite

import (
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

// Q218 fk 类型（外键键关联）：query 终点 + 值级 taint 验证（终点 taint
// 与终点列呼应）→ 升级 fk——真实键关联（对象字段读的字段名与起点列
// lowercase 呼应，值确实从起点列流来）。ER 图默认只画 fk；fk 不限跳
// （值流已验证）；CLI 独立类型（--type fk）。
//
// 噪声链（pay_order.id → t15.BuyerId → mm_block_list.member_id）：
// 对象字段读 BuyerId 与起点列 id lowercase 求交为空（buyerid ≠ id）
// → 终点 taint 空 → 保持 query（不升 fk）。

// fkFixture 真实链（Id 字段呼应）+ 噪声链（BuyerId 字段不呼应）。
func fkFixture(t *testing.T, r *Repo) {
	t.Helper()
	fA := "symbol:go:example.com/m:load"
	fB := "symbol:go:example.com/m:save"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(fA), Kind: domain.KindFunction, Name: "load"},
		{ID: domain.CanonicalID(fB), Kind: domain.KindFunction, Name: "save"},
		// 起点：table_a.id 读出（真实链 + 噪声链共用起点）
		{ID: domain.CanonicalID(fA + "#ext.sql.table_a.id.read@6"), Kind: domain.KindFieldAccess,
			Name: "table_a.id", FilePath: "a.go", LineStart: 6,
			Properties: map[string]any{"full_path": "table_a.id", "access_kind": "read",
				"type_string": "sql", "is_external": "true", "func_id": fA}},
		// 真实链：对象 → 字段 Id 读（lowercase 与起点 id 呼应）→ 值 → filter
		{ID: domain.CanonicalID(fA + "#obj1"), Kind: domain.KindSSAValue, Name: "obj1",
			Properties: map[string]any{"func_id": fA, "type_string": "*m.Item"}},
		{ID: domain.CanonicalID(fA + "#obj1.Id"), Kind: domain.KindFieldAccess,
			Name: "obj1.Id", FilePath: "a.go", LineStart: 8,
			Properties: map[string]any{"full_path": "m.Item.Id", "access_kind": "read", "func_id": fA}},
		{ID: domain.CanonicalID(fA + "#v1"), Kind: domain.KindSSAValue, Name: "v1",
			Properties: map[string]any{"func_id": fA, "type_string": "int64"}},
		{ID: domain.CanonicalID(fB + "#ext.sql.table_b.item_id.filter@20"), Kind: domain.KindFieldAccess,
			Name: "table_b.item_id", FilePath: "b.go", LineStart: 20,
			Properties: map[string]any{"full_path": "table_b.item_id", "access_kind": "filter",
				"type_string": "sql", "is_external": "true", "func_id": fB}},
		// 噪声链：对象 → 字段 BuyerId 读（lowercase 与起点 id 不呼应）→ 值 → filter
		{ID: domain.CanonicalID(fA + "#obj2"), Kind: domain.KindSSAValue, Name: "obj2",
			Properties: map[string]any{"func_id": fA, "type_string": "*m.Payment"}},
		{ID: domain.CanonicalID(fA + "#obj2.BuyerId"), Kind: domain.KindFieldAccess,
			Name: "obj2.BuyerId", FilePath: "a.go", LineStart: 9,
			Properties: map[string]any{"full_path": "m.Payment.BuyerId", "access_kind": "read", "func_id": fA}},
		{ID: domain.CanonicalID(fA + "#v2"), Kind: domain.KindSSAValue, Name: "v2",
			Properties: map[string]any{"func_id": fA, "type_string": "int64"}},
		{ID: domain.CanonicalID(fB + "#ext.sql.table_c.member_id.filter@30"), Kind: domain.KindFieldAccess,
			Name: "table_c.member_id", FilePath: "b.go", LineStart: 30,
			Properties: map[string]any{"full_path": "table_c.member_id", "access_kind": "filter",
				"type_string": "sql", "is_external": "true", "func_id": fB}},
	}
	edges := []*domain.Fact{
		{SourceID: domain.CanonicalID(fA + "#ext.sql.table_a.id.read@6"), TargetID: domain.CanonicalID(fA + "#obj1"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(fA + "#obj1"), TargetID: domain.CanonicalID(fA + "#obj1.Id"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(fA + "#obj1.Id"), TargetID: domain.CanonicalID(fA + "#v1"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(fA + "#v1"), TargetID: domain.CanonicalID(fB + "#ext.sql.table_b.item_id.filter@20"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(fA + "#ext.sql.table_a.id.read@6"), TargetID: domain.CanonicalID(fA + "#obj2"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(fA + "#obj2"), TargetID: domain.CanonicalID(fA + "#obj2.BuyerId"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(fA + "#obj2.BuyerId"), TargetID: domain.CanonicalID(fA + "#v2"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(fA + "#v2"), TargetID: domain.CanonicalID(fB + "#ext.sql.table_c.member_id.filter@30"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
	}
	save(t, r, nodes, edges)
}

// TestRelationFKVerifiedChain：真实链（Id 字段 lowercase 呼应）→ fk；
// 噪声链（BuyerId 不呼应）→ 保持 query（taint 求交为空）。
func TestRelationFKVerifiedChain(t *testing.T) {
	r := newTestRepo(t)
	fkFixture(t, r)
	rels, err := r.GetTableRelations("table_a", "full")
	if err != nil {
		t.Fatalf("full: %v", err)
	}
	var fkCount, queryCount int
	for _, rel := range rels {
		switch {
		case rel.ToTable == "table_b" && rel.ToCol == "item_id":
			if rel.Type != domain.RelationFK {
				t.Fatalf("真实链应标 fk（Id 呼应），got %s", rel.Type)
			}
			fkCount++
		case rel.ToTable == "table_c" && rel.ToCol == "member_id":
			if rel.Type != domain.RelationQuery {
				t.Fatalf("噪声链应保持 query（BuyerId 求交空），got %s", rel.Type)
			}
			queryCount++
		}
	}
	if fkCount != 1 || queryCount != 1 {
		t.Fatalf("应各 1 条（fk=%d query=%d），rels=%+v", fkCount, queryCount, rels)
	}
}

// TestRelationFKUnlimitedHops：fk 默认不限跳（值流已验证）——12 跳 fk
// 保留；同链 query 4 跳被滤。
func TestRelationFKUnlimitedHops(t *testing.T) {
	r := newTestRepo(t)
	fA := "symbol:go:example.com/m:load"
	fB := "symbol:go:example.com/m:save"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(fA), Kind: domain.KindFunction, Name: "load"},
		{ID: domain.CanonicalID(fB), Kind: domain.KindFunction, Name: "save"},
		{ID: domain.CanonicalID(fA + "#ext.sql.table_a.id.read@6"), Kind: domain.KindFieldAccess,
			Name: "table_a.id", FilePath: "a.go", LineStart: 6,
			Properties: map[string]any{"full_path": "table_a.id", "access_kind": "read",
				"type_string": "sql", "is_external": "true", "func_id": fA}},
		{ID: domain.CanonicalID(fA + "#obj"), Kind: domain.KindSSAValue, Name: "obj",
			Properties: map[string]any{"func_id": fA, "type_string": "*m.Item"}},
		{ID: domain.CanonicalID(fA + "#obj.Id"), Kind: domain.KindFieldAccess,
			Name: "obj.Id", FilePath: "a.go", LineStart: 8,
			Properties: map[string]any{"full_path": "m.Item.Id", "access_kind": "read", "func_id": fA}},
		{ID: domain.CanonicalID(fB + "#ext.sql.table_b.item_id.filter@20"), Kind: domain.KindFieldAccess,
			Name: "table_b.item_id", FilePath: "b.go", LineStart: 20,
			Properties: map[string]any{"full_path": "table_b.item_id", "access_kind": "filter",
				"type_string": "sql", "is_external": "true", "func_id": fB}},
	}
	var edges []*domain.Fact
	// 12 跳长链：read → obj → Id 读 → 中间 10 个值节点 → filter
	prev := domain.CanonicalID(fA + "#ext.sql.table_a.id.read@6")
	edges = append(edges, &domain.Fact{SourceID: prev, TargetID: domain.CanonicalID(fA + "#obj"),
		Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1})
	prev = domain.CanonicalID(fA + "#obj")
	edges = append(edges, &domain.Fact{SourceID: prev, TargetID: domain.CanonicalID(fA + "#obj.Id"),
		Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1})
	prev = domain.CanonicalID(fA + "#obj.Id")
	for i := 0; i < 9; i++ { // read→obj→Id→m0..m8→filter = 12 跳（BFS maxDepth=12）
		mid := domain.CanonicalID(fA + "#m" + string(rune('a'+i)))
		edges = append(edges, &domain.Fact{SourceID: prev, TargetID: mid,
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1})
		prev = mid
	}
	edges = append(edges, &domain.Fact{SourceID: prev, TargetID: domain.CanonicalID(fB + "#ext.sql.table_b.item_id.filter@20"),
		Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1})
	// 中间节点需要存在（visited 遍历不检查中间 ssa_value，但为了模拟加几个）
	for i := 0; i < 9; i++ {
		mid := domain.CanonicalID(fA + "#m" + string(rune('a'+i)))
		nodes = append(nodes, &domain.CodeEntity{ID: mid, Kind: domain.KindSSAValue, Name: "m" + string(rune('a'+i)),
			Properties: map[string]any{"func_id": fA, "type_string": "int64"}})
	}
	save(t, r, nodes, edges)
	rels, err := r.GetTableRelations("table_a", "full")
	if err != nil {
		t.Fatalf("full: %v", err)
	}
	found := false
	for _, rel := range rels {
		if rel.ToTable == "table_b" && rel.ToCol == "item_id" {
			found = true
			if rel.Type != domain.RelationFK {
				t.Fatalf("12 跳真实链应标 fk 且保留（fk 不限跳），got %s", rel.Type)
			}
			if rel.Hops != 12 {
				t.Fatalf("hops 应为 12，got %d", rel.Hops)
			}
		}
	}
	if !found {
		t.Fatal("12 跳 fk 链应保留（fk 默认不限跳）")
	}
}

// TestRelationFKRank：同 key fk 与 query 并存时 fk 优先（rank 最高）。
func TestRelationFKRank(t *testing.T) {
	r := newTestRepo(t)
	fA := "symbol:go:example.com/m:load"
	fB := "symbol:go:example.com/m:save"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(fA), Kind: domain.KindFunction, Name: "load"},
		{ID: domain.CanonicalID(fB), Kind: domain.KindFunction, Name: "save"},
		{ID: domain.CanonicalID(fA + "#ext.sql.table_a.id.read@6"), Kind: domain.KindFieldAccess,
			Name: "table_a.id", FilePath: "a.go", LineStart: 6,
			Properties: map[string]any{"full_path": "table_a.id", "access_kind": "read",
				"type_string": "sql", "is_external": "true", "func_id": fA}},
		{ID: domain.CanonicalID(fA + "#ext.sql.table_a.name.read@7"), Kind: domain.KindFieldAccess,
			Name: "table_a.name", FilePath: "a.go", LineStart: 7,
			Properties: map[string]any{"full_path": "table_a.name", "access_kind": "read",
				"type_string": "sql", "is_external": "true", "func_id": fA}},
		{ID: domain.CanonicalID(fA + "#obj"), Kind: domain.KindSSAValue, Name: "obj",
			Properties: map[string]any{"func_id": fA, "type_string": "*m.Item"}},
		{ID: domain.CanonicalID(fA + "#obj.Id"), Kind: domain.KindFieldAccess,
			Name: "obj.Id", FilePath: "a.go", LineStart: 8,
			Properties: map[string]any{"full_path": "m.Item.Id", "access_kind": "read", "func_id": fA}},
		{ID: domain.CanonicalID(fA + "#v1"), Kind: domain.KindSSAValue, Name: "v1",
			Properties: map[string]any{"func_id": fA, "type_string": "int64"}},
		{ID: domain.CanonicalID(fA + "#v2"), Kind: domain.KindSSAValue, Name: "v2",
			Properties: map[string]any{"func_id": fA, "type_string": "string"}},
		{ID: domain.CanonicalID(fB + "#ext.sql.table_b.item_id.filter@20"), Kind: domain.KindFieldAccess,
			Name: "table_b.item_id", FilePath: "b.go", LineStart: 20,
			Properties: map[string]any{"full_path": "table_b.item_id", "access_kind": "filter",
				"type_string": "sql", "is_external": "true", "func_id": fB}},
	}
	edges := []*domain.Fact{
		{SourceID: domain.CanonicalID(fA + "#ext.sql.table_a.id.read@6"), TargetID: domain.CanonicalID(fA + "#obj"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(fA + "#obj"), TargetID: domain.CanonicalID(fA + "#obj.Id"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(fA + "#obj.Id"), TargetID: domain.CanonicalID(fA + "#v1"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(fA + "#v1"), TargetID: domain.CanonicalID(fB + "#ext.sql.table_b.item_id.filter@20"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
		// name → item_id（列名不呼应 item_id vs name → query 降级 read？——
		// 这里验证 fk 与 query 同 key 的 rank：name 链标 query（不升 fk）
		{SourceID: domain.CanonicalID(fA + "#ext.sql.table_a.name.read@7"), TargetID: domain.CanonicalID(fA + "#v2"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(fA + "#v2"), TargetID: domain.CanonicalID(fB + "#ext.sql.table_b.item_id.filter@20"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
	}
	save(t, r, nodes, edges)
	rels, err := r.GetTableRelations("table_a", "full")
	if err != nil {
		t.Fatalf("full: %v", err)
	}
	for _, rel := range rels {
		if rel.ToTable == "table_b" && rel.ToCol == "item_id" && rel.FromCol == "id" {
			if rel.Type != domain.RelationFK {
				t.Fatalf("同 key 应取 fk（rank 最高），got %s（rels=%+v）", rel.Type, rels)
			}
		}
	}
}
