package sqlite

import (
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

// Q234 where 条件字段识别：查询时 where 使用的字段（filter 节点）通常
// 有外键——两条规则，最终统一 fk 展示：
//   - 规则 A（BFS 终点提升）：query/write 终点 + 终点列是 where 条件
//     字段 + 键形态（isKeyCol，防 create_time）+ 呼应（同名键列 /
//     外键形态 / 值流 taint）→ 提升 fk（同源写提升 + query 筛选）
//   - 规则 B（filter 字段直接识别）：filter 字段按列名呼应直接识别
//     fk——外键形态（user_id → user 表）或同名键列（biz_id ↔ 另一表
//     biz_id）；值流不通（where 参数来自请求/字面量）也能识别。
// 自表主键（WHERE id=?）与非键字段（create_time/status）不识别。

// whereFkFixture 规则 A 场景：a_tab.biz_id 读出 → 同源写 b_tab.biz_id
// （b_tab.biz_id 同时是查询 where 条件）→ 提升 fk；同源写 c_tab.order_id
// （无 where 条件）→ 保持 write；a_tab.create_time → b_tab.create_time
// （虽被 where 使用，非键字段）→ 保持 write。
func whereFkFixture(t *testing.T, r *Repo) {
	t.Helper()
	f := "symbol:go:example.com/m:run"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(f), Kind: domain.KindFunction, Name: "run"},
		// a_tab 两列读出（BFS 起点）
		{ID: domain.CanonicalID(f + "#ext.sql.a_tab.biz_id.read@6"), Kind: domain.KindFieldAccess,
			Name: "a_tab.biz_id", FilePath: "a.go", LineStart: 6,
			Properties: map[string]any{"full_path": "a_tab.biz_id", "access_kind": "read",
				"type_string": "sql", "is_external": "true", "func_id": f}},
		{ID: domain.CanonicalID(f + "#ext.sql.a_tab.create_time.read@7"), Kind: domain.KindFieldAccess,
			Name: "a_tab.create_time", FilePath: "a.go", LineStart: 7,
			Properties: map[string]any{"full_path": "a_tab.create_time", "access_kind": "read",
				"type_string": "sql", "is_external": "true", "func_id": f}},
		// 值节点
		{ID: domain.CanonicalID(f + "#v1"), Kind: domain.KindSSAValue, Name: "v1",
			Properties: map[string]any{"func_id": f, "type_string": "int64"}},
		{ID: domain.CanonicalID(f + "#v2"), Kind: domain.KindSSAValue, Name: "v2",
			Properties: map[string]any{"func_id": f, "type_string": "int64"}},
		// 同源写终点：b_tab.biz_id（+where 条件）、c_tab.order_id（无 where）
		{ID: domain.CanonicalID(f + "#ext.sql.b_tab.biz_id.write@20"), Kind: domain.KindFieldAccess,
			Name: "b_tab.biz_id", FilePath: "b.go", LineStart: 20,
			Properties: map[string]any{"full_path": "b_tab.biz_id", "access_kind": "write",
				"type_string": "sql", "is_external": "true", "func_id": f}},
		{ID: domain.CanonicalID(f + "#ext.sql.c_tab.order_id.write@25"), Kind: domain.KindFieldAccess,
			Name: "c_tab.order_id", FilePath: "b.go", LineStart: 25,
			Properties: map[string]any{"full_path": "c_tab.order_id", "access_kind": "write",
				"type_string": "sql", "is_external": "true", "func_id": f}},
		// 非键同源写：create_time（+where 条件，不提升）
		{ID: domain.CanonicalID(f + "#ext.sql.b_tab.create_time.write@30"), Kind: domain.KindFieldAccess,
			Name: "b_tab.create_time", FilePath: "b.go", LineStart: 30,
			Properties: map[string]any{"full_path": "b_tab.create_time", "access_kind": "write",
				"type_string": "sql", "is_external": "true", "func_id": f}},
		// where 条件（filter）节点：b_tab.biz_id / b_tab.create_time 被查询使用
		{ID: domain.CanonicalID(f + "#ext.sql.b_tab.biz_id.filter@40"), Kind: domain.KindFieldAccess,
			Name: "b_tab.biz_id", FilePath: "b.go", LineStart: 40,
			Properties: map[string]any{"full_path": "b_tab.biz_id", "access_kind": "filter",
				"type_string": "sql", "is_external": "true", "func_id": f}},
		{ID: domain.CanonicalID(f + "#ext.sql.b_tab.create_time.filter@41"), Kind: domain.KindFieldAccess,
			Name: "b_tab.create_time", FilePath: "b.go", LineStart: 41,
			Properties: map[string]any{"full_path": "b_tab.create_time", "access_kind": "filter",
				"type_string": "sql", "is_external": "true", "func_id": f}},
	}
	edges := []*domain.Fact{
		{SourceID: domain.CanonicalID(f + "#ext.sql.a_tab.biz_id.read@6"), TargetID: domain.CanonicalID(f + "#v1"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(f + "#v1"), TargetID: domain.CanonicalID(f + "#ext.sql.b_tab.biz_id.write@20"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(f + "#v1"), TargetID: domain.CanonicalID(f + "#ext.sql.c_tab.order_id.write@25"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(f + "#ext.sql.a_tab.create_time.read@7"), TargetID: domain.CanonicalID(f + "#v2"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(f + "#v2"), TargetID: domain.CanonicalID(f + "#ext.sql.b_tab.create_time.write@30"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
	}
	save(t, r, nodes, edges)
}

// TestRelationWhereFkSameSourceWrite：同源写终点列是 where 条件字段 →
// 提升 fk；无 where 条件保持 write；非键字段（create_time）不提升。
// 内存（full）与 SQL（sql）路径行为一致。
func TestRelationWhereFkSameSourceWrite(t *testing.T) {
	for _, mode := range []string{"full", "sql"} {
		r := newTestRepo(t)
		whereFkFixture(t, r)
		rels, err := r.GetTableRelations("a_tab", mode)
		if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		var biz, order, ctime *domain.TableRelation
		for _, rel := range rels {
			switch {
			case rel.ToTable == "b_tab" && rel.ToCol == "biz_id":
				biz = rel
			case rel.ToTable == "c_tab" && rel.ToCol == "order_id":
				order = rel
			case rel.ToTable == "b_tab" && rel.ToCol == "create_time":
				ctime = rel
			}
		}
		if biz == nil {
			t.Fatalf("%s: a_tab.biz_id → b_tab.biz_id 应存在（同源写）", mode)
		}
		if biz.Type != domain.RelationFK {
			t.Fatalf("%s: 同源写 + where 条件应提升 fk，got %s", mode, biz.Type)
		}
		if order == nil || order.Type != domain.RelationWrite {
			t.Fatalf("%s: 同源写无 where 条件应保持 write，got %v", mode, order)
		}
		if ctime == nil || ctime.Type != domain.RelationWrite {
			t.Fatalf("%s: create_time 非键字段不提升，got %v", mode, ctime)
		}
	}
}

// whereDirectFixture 规则 B 场景（BFS 不通——where 参数来自请求/字面量）：
//   - order_tab.user_id.filter：外键形态（user_id ↔ user 表名）→
//     order_tab.user_id → user.id fk
//   - order_tab.biz_id.filter：同名键列（book_tab 有 biz_id 键列）→
//     order_tab.biz_id → book_tab.biz_id fk
//   - order_tab.create_time.filter：非键字段 → 不产生关联
//   - user.id.filter：自表主键（WHERE id=?）→ 不产生关联
func whereDirectFixture(t *testing.T, r *Repo) {
	t.Helper()
	f := "symbol:go:example.com/m:q"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(f), Kind: domain.KindFunction, Name: "q"},
		// user 表存在（id 读出）——外键形态呼应的目标
		{ID: domain.CanonicalID(f + "#ext.sql.user.id.read@5"), Kind: domain.KindFieldAccess,
			Name: "user.id", FilePath: "u.go", LineStart: 5,
			Properties: map[string]any{"full_path": "user.id", "access_kind": "read",
				"type_string": "sql", "is_external": "true", "func_id": f}},
		// book_tab 有 biz_id 键列（写入）——同名键列呼应的目标
		{ID: domain.CanonicalID(f + "#ext.sql.book_tab.biz_id.write@10"), Kind: domain.KindFieldAccess,
			Name: "book_tab.biz_id", FilePath: "b.go", LineStart: 10,
			Properties: map[string]any{"full_path": "book_tab.biz_id", "access_kind": "write",
				"type_string": "sql", "is_external": "true", "func_id": f}},
		// order_tab 查询 where 条件（值来自参数，无值流边）
		{ID: domain.CanonicalID(f + "#ext.sql.order_tab.user_id.filter@20"), Kind: domain.KindFieldAccess,
			Name: "order_tab.user_id", FilePath: "o.go", LineStart: 20,
			Properties: map[string]any{"full_path": "order_tab.user_id", "access_kind": "filter",
				"type_string": "sql", "is_external": "true", "func_id": f}},
		{ID: domain.CanonicalID(f + "#ext.sql.order_tab.biz_id.filter@21"), Kind: domain.KindFieldAccess,
			Name: "order_tab.biz_id", FilePath: "o.go", LineStart: 21,
			Properties: map[string]any{"full_path": "order_tab.biz_id", "access_kind": "filter",
				"type_string": "sql", "is_external": "true", "func_id": f}},
		{ID: domain.CanonicalID(f + "#ext.sql.order_tab.create_time.filter@22"), Kind: domain.KindFieldAccess,
			Name: "order_tab.create_time", FilePath: "o.go", LineStart: 22,
			Properties: map[string]any{"full_path": "order_tab.create_time", "access_kind": "filter",
				"type_string": "sql", "is_external": "true", "func_id": f}},
		// user 表主键查询（WHERE id=?）
		{ID: domain.CanonicalID(f + "#ext.sql.user.id.filter@30"), Kind: domain.KindFieldAccess,
			Name: "user.id", FilePath: "u.go", LineStart: 30,
			Properties: map[string]any{"full_path": "user.id", "access_kind": "filter",
				"type_string": "sql", "is_external": "true", "func_id": f}},
	}
	save(t, r, nodes, nil)
}

// TestRelationWhereFkDirect：规则 B——filter 字段列名呼应直接识别 fk
// （值流不通），自表主键/非键字段不识别。
func TestRelationWhereFkDirect(t *testing.T) {
	for _, mode := range []string{"full", "sql"} {
		r := newTestRepo(t)
		whereDirectFixture(t, r)
		rels, err := r.GetTableRelations("order_tab", mode)
		if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		var user, book, ctime *domain.TableRelation
		for _, rel := range rels {
			switch {
			case rel.ToTable == "user" && rel.ToCol == "id":
				user = rel
			case rel.ToTable == "book_tab" && rel.ToCol == "biz_id":
				book = rel
			case rel.ToCol == "create_time":
				ctime = rel
			}
		}
		if user == nil {
			t.Fatalf("%s: order_tab.user_id → user.id 应识别（外键形态 where 字段）", mode)
		}
		if user.Type != domain.RelationFK {
			t.Fatalf("%s: 外键形态 where 字段应直接 fk，got %s", mode, user.Type)
		}
		if book == nil {
			t.Fatalf("%s: order_tab.biz_id → book_tab.biz_id 应识别（同名键列 where 字段）", mode)
		}
		if book.Type != domain.RelationFK {
			t.Fatalf("%s: 同名键列 where 字段应直接 fk，got %s", mode, book.Type)
		}
		if ctime != nil {
			t.Fatalf("%s: create_time 非键字段不应识别，got %v", mode, ctime)
		}
		// user 表：WHERE id=? 是自表主键查询，不产生关联
		urels, err := r.GetTableRelations("user", mode)
		if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		for _, rel := range urels {
			if rel.FromCol == "id" {
				t.Fatalf("%s: user.id 自表主键不应产生 where 关联，got %v", mode, rel)
			}
		}
	}
}

// TestRelationWhereFkNoiseNotPromoted：Q218 换名噪声链（obj.BuyerId 读
// 断 taint → table_c.member_id.filter）——虽为 where 条件字段且键形态，
// 但呼应全不满足（列名/表名/taint）→ 保持 query 不升 fk。
func TestRelationWhereFkNoiseNotPromoted(t *testing.T) {
	for _, mode := range []string{"full", "sql"} {
		r := newTestRepo(t)
		fkFixture(t, r)
		rels, err := r.GetTableRelations("table_a", mode)
		if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		for _, rel := range rels {
			if rel.ToTable == "table_c" && rel.ToCol == "member_id" {
				if rel.Type != domain.RelationQuery {
					t.Fatalf("%s: 换名噪声链应保持 query（不升 fk），got %s", mode, rel.Type)
				}
			}
			if rel.ToTable == "table_b" && rel.ToCol == "item_id" {
				if rel.Type != domain.RelationFK {
					t.Fatalf("%s: 真实链应保持 fk，got %s", mode, rel.Type)
				}
			}
		}
	}
}
