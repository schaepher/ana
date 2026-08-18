package sqlite

import (
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

// TestRelationArgumentDirection：Q199——argument/returns 边只允许正向穿越
// （实参→形参），不允许形参反向回实参。
//
// 误报场景（go2o 实测）：两个函数 SavePermRole(v) / SavePermRoleRes(v)
// 各自写不同表（rbac_role.create_time / rbac_role_res.id），调用方把
// 对象传给它们。无向 BFS 从 create_time.write 出发可经
// param.v →(argument 反向) 调用方 →(data_flows_to) 另一实参 →(argument)
// 另一形参 → res.id.write 串出假"同源写"——create_time → id 明显荒谬。
//
// fixture：svc 中 u 与 o 都从 r 派生（r → u、r → o data_flows_to 无向连通），
// u/o 分别作实参传给 saveUser/saveOrder（写 users.name / orders.order_no）。
// 修复前：users.name 与 orders.order_no 产生 write 关联（假同源）；
// 修复后：argument 反向被禁，链断，无关联。
func TestRelationArgumentDirection(t *testing.T) {
	r := newTestRepo(t)
	svc := "symbol:go:example.com/m:svc"
	su := "symbol:go:example.com/m:saveUser"
	so := "symbol:go:example.com/m:saveOrder"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(svc), Kind: domain.KindFunction, Name: "svc"},
		{ID: domain.CanonicalID(su), Kind: domain.KindFunction, Name: "saveUser"},
		{ID: domain.CanonicalID(so), Kind: domain.KindFunction, Name: "saveOrder"},
		// 值节点：r 派生 u、o
		{ID: domain.CanonicalID(svc + "#r"), Kind: domain.KindSSAValue, Name: "r",
			Properties: map[string]any{"func_id": svc}},
		{ID: domain.CanonicalID(svc + "#u"), Kind: domain.KindSSAValue, Name: "u",
			Properties: map[string]any{"func_id": svc}},
		{ID: domain.CanonicalID(svc + "#o"), Kind: domain.KindSSAValue, Name: "o",
			Properties: map[string]any{"func_id": svc}},
		{ID: domain.CanonicalID(su + "#param.v"), Kind: domain.KindSSAValue, Name: "param.v",
			Properties: map[string]any{"func_id": su}},
		{ID: domain.CanonicalID(so + "#param.v"), Kind: domain.KindSSAValue, Name: "param.v",
			Properties: map[string]any{"func_id": so}},
		// 表列虚拟节点
		{ID: domain.CanonicalID(su + "#ext.gorm.users.name.write@5"), Kind: domain.KindFieldAccess,
			Name: "users.name", FilePath: "a.go", LineStart: 5,
			Properties: map[string]any{"full_path": "users.name", "instance_path": "users.name",
				"access_kind": "write", "type_string": "gorm", "is_external": "true", "func_id": su}},
		{ID: domain.CanonicalID(so + "#ext.gorm.orders.order_no.write@9"), Kind: domain.KindFieldAccess,
			Name: "orders.order_no", FilePath: "a.go", LineStart: 9,
			Properties: map[string]any{"full_path": "orders.order_no", "instance_path": "orders.order_no",
				"access_kind": "write", "type_string": "gorm", "is_external": "true", "func_id": so}},
	}
	edges := []*domain.Fact{
		// r 派生 u、o（data_flows_to 双向——值流连通）
		{SourceID: domain.CanonicalID(svc + "#r"), TargetID: domain.CanonicalID(svc + "#u"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(svc + "#r"), TargetID: domain.CanonicalID(svc + "#o"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		// 实参 → 形参（argument 有向：实参 → 形参）
		{SourceID: domain.CanonicalID(svc + "#u"), TargetID: domain.CanonicalID(su + "#param.v"),
			Kind: domain.FactArgument, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(svc + "#o"), TargetID: domain.CanonicalID(so + "#param.v"),
			Kind: domain.FactArgument, ToolSource: domain.ToolSSA, Confidence: 1},
		// 形参 → 表列写（summary_io：值 → 列）
		{SourceID: domain.CanonicalID(su + "#param.v"), TargetID: domain.CanonicalID(su + "#ext.gorm.users.name.write@5"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(so + "#param.v"), TargetID: domain.CanonicalID(so + "#ext.gorm.orders.order_no.write@9"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
	}
	save(t, r, nodes, edges)

	// users 表：修复后不应有到 orders 的 write 关联（argument 反向被禁）
	rels, err := r.GetTableRelations("users", "full")
	if err != nil {
		t.Fatalf("GetTableRelations: %v", err)
	}
	for _, rel := range rels {
		if rel.ToTable == "orders" && rel.Type == domain.RelationWrite {
			t.Errorf("users → orders 假同源 write 应消失（argument 反向穿越），got %+v", rel)
		}
	}
	// 正向链保留：orders 表自身的写入（orders.order_no write 仍存在）
	cols, err := r.GetTableColumns("orders")
	if err != nil || len(cols) == 0 {
		t.Errorf("orders 列节点应保留，got %v, %v", cols, err)
	}
}

// TestRelationWriteFieldAssign：Q202——跨函数 write 若链上存在与
// 起点列同名的字段级赋值（a.OrderId = t），则字段值真实传递，
// write 保留（order.id 读出 → 赋给 A.order_id → 写入）；仅对象整体
// 传递（无字段赋值）才丢弃（create_time 场景）。
func TestRelationWriteFieldAssign(t *testing.T) {
	r := newTestRepo(t)
	readFn := "symbol:go:example.com/m:readOrder"
	saveFn := "symbol:go:example.com/m:saveA"
	svcFn := "symbol:go:example.com/m:svc"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(readFn), Kind: domain.KindFunction, Name: "readOrder"},
		{ID: domain.CanonicalID(saveFn), Kind: domain.KindFunction, Name: "saveA"},
		{ID: domain.CanonicalID(svcFn), Kind: domain.KindFunction, Name: "svc"},
		// 值节点
		{ID: domain.CanonicalID(readFn + "#t1"), Kind: domain.KindSSAValue, Name: "t1",
			Properties: map[string]any{"func_id": readFn}},
		{ID: domain.CanonicalID(svcFn + "#t2"), Kind: domain.KindSSAValue, Name: "t2",
			Properties: map[string]any{"func_id": svcFn}},
		{ID: domain.CanonicalID(svcFn + "#a"), Kind: domain.KindSSAValue, Name: "a",
			Properties: map[string]any{"func_id": svcFn}},
		{ID: domain.CanonicalID(saveFn + "#param.v"), Kind: domain.KindSSAValue, Name: "param.v",
			Properties: map[string]any{"func_id": saveFn}},
		// 字段级赋值中间节点：a.OrderId 字段写（非 external）
		{ID: domain.CanonicalID(svcFn + "#a.OrderId.write@10"), Kind: domain.KindFieldAccess,
			Name: "a.OrderId", FilePath: "a.go", LineStart: 10,
			Properties: map[string]any{"full_path": "example.com/m.Order.OrderId", "instance_path": "a.OrderId",
				"access_kind": "write"}},
		// 表列
		{ID: domain.CanonicalID(readFn + "#ext.gorm.order.id.read@5"), Kind: domain.KindFieldAccess,
			Name: "order.id", FilePath: "a.go", LineStart: 5,
			Properties: map[string]any{"full_path": "order.id", "instance_path": "order.id",
				"access_kind": "read", "type_string": "gorm", "is_external": "true", "func_id": readFn}},
		{ID: domain.CanonicalID(saveFn + "#ext.gorm.A.order_id.write@15"), Kind: domain.KindFieldAccess,
			Name: "A.order_id", FilePath: "a.go", LineStart: 15,
			Properties: map[string]any{"full_path": "A.order_id", "instance_path": "A.order_id",
				"access_kind": "write", "type_string": "gorm", "is_external": "true", "func_id": saveFn}},
	}
	edges := []*domain.Fact{
		// order.id 读出 → t1 →(returns) t2
		{SourceID: domain.CanonicalID(readFn + "#ext.gorm.order.id.read@5"), TargetID: domain.CanonicalID(readFn + "#t1"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(readFn + "#t1"), TargetID: domain.CanonicalID(svcFn + "#t2"),
			Kind: domain.FactReturns, ToolSource: domain.ToolSSA, Confidence: 1},
		// 字段级赋值：t2 → a.OrderId 字段写 + 基地址 a ↔ 字段节点
		{SourceID: domain.CanonicalID(svcFn + "#t2"), TargetID: domain.CanonicalID(svcFn + "#a.OrderId.write@10"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(svcFn + "#a"), TargetID: domain.CanonicalID(svcFn + "#a.OrderId.write@10"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		// 基地址 a →(argument) param.v(saveA)
		{SourceID: domain.CanonicalID(svcFn + "#a"), TargetID: domain.CanonicalID(saveFn + "#param.v"),
			Kind: domain.FactArgument, ToolSource: domain.ToolSSA, Confidence: 1},
		// param.v → A.order_id 写入
		{SourceID: domain.CanonicalID(saveFn + "#param.v"), TargetID: domain.CanonicalID(saveFn + "#ext.gorm.A.order_id.write@15"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
	}
	save(t, r, nodes, edges)

	// order 表 BFS：order.id 读出值经字段赋值（a.OrderId = t）写入
	// A.order_id——跨函数 write 应保留（taint {id} 与 order_id 呼应）
	rels, err := r.GetTableRelations("order", "full")
	if err != nil {
		t.Fatalf("GetTableRelations: %v", err)
	}
	found := false
	for _, rel := range rels {
		if rel.FromTable == "order" && rel.FromCol == "id" && rel.ToTable == "A" &&
			rel.ToCol == "order_id" && rel.Type == domain.RelationWrite {
			found = true
		}
	}
	if !found {
		t.Errorf("跨函数 write 带字段级赋值（order.id → A.order_id）应保留，got %+v", rels)
	}
}

// TestRelationFKColFallback：Q202b——外键列名回退。跨函数 write 链无
// 值流 taint 时（外键值来自请求参数），列名与表名呼应仍建立关联：
// rbac_role_res.role_id（base=role）↔ rbac_role 表——业务上 role_id
// 引用 role.id。create_time → res.id（id 无 base 不呼应）不适用。
func TestRelationFKColFallback(t *testing.T) {
	r := newTestRepo(t)
	svcFn := "symbol:go:example.com/m:svc"
	saveFn := "symbol:go:example.com/m:savePermRoleRes"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(svcFn), Kind: domain.KindFunction, Name: "svc"},
		{ID: domain.CanonicalID(saveFn), Kind: domain.KindFunction, Name: "savePermRoleRes"},
		{ID: domain.CanonicalID(svcFn + "#t9"), Kind: domain.KindSSAValue, Name: "t9",
			Properties: map[string]any{"func_id": svcFn, "type_string": "*rbac.RbacRoleRes"}},
		{ID: domain.CanonicalID(saveFn + "#param.v"), Kind: domain.KindSSAValue, Name: "param.v",
			Properties: map[string]any{"func_id": saveFn}},
		{ID: domain.CanonicalID(svcFn + "#t9.RoleId.write@20"), Kind: domain.KindFieldAccess,
			Name: "t9.RoleId", FilePath: "a.go", LineStart: 20,
			Properties: map[string]any{"full_path": "example.com/m.RbacRoleRes.RoleId", "instance_path": "t9.RoleId",
				"access_kind": "write"}},
		{ID: domain.CanonicalID(saveFn + "#ext.gorm.rbac_role_res.role_id.write@30"), Kind: domain.KindFieldAccess,
			Name: "rbac_role_res.role_id", FilePath: "a.go", LineStart: 30,
			Properties: map[string]any{"full_path": "rbac_role_res.role_id", "instance_path": "rbac_role_res.role_id",
				"access_kind": "write", "type_string": "gorm", "is_external": "true", "func_id": saveFn}},
		// rbac_role 表起点节点（本表 BFS 起点）
		{ID: domain.CanonicalID(svcFn + "#ext.gorm.rbac_role.id.read@5"), Kind: domain.KindFieldAccess,
			Name: "rbac_role.id", FilePath: "a.go", LineStart: 5,
			Properties: map[string]any{"full_path": "rbac_role.id", "instance_path": "rbac_role.id",
				"access_kind": "read", "type_string": "gorm", "is_external": "true", "func_id": svcFn}},
	}
	edges := []*domain.Fact{
		// 起点 rbac_role.id.read 读出 → t9（真实链：GetRole → UpdateRoleResource）
		{SourceID: domain.CanonicalID(svcFn + "#ext.gorm.rbac_role.id.read@5"), TargetID: domain.CanonicalID(svcFn + "#t9"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(svcFn + "#t9"), TargetID: domain.CanonicalID(svcFn + "#t9.RoleId.write@20"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(svcFn + "#t9"), TargetID: domain.CanonicalID(saveFn + "#param.v"),
			Kind: domain.FactArgument, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(saveFn + "#param.v"), TargetID: domain.CanonicalID(saveFn + "#ext.gorm.rbac_role_res.role_id.write@30"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
	}
	save(t, r, nodes, edges)
	// rbac_role 表 BFS：命中 role_id write——链 crossed（argument）且无
	// 值流 taint（t9 对象基地址不延续）→ 外键列名回退（role_id ↔ rbac_role）
	rels, err := r.GetTableRelations("rbac_role", "full")
	if err != nil {
		t.Fatalf("GetTableRelations: %v", err)
	}
	found := false
	for _, rel := range rels {
		if rel.FromTable == "rbac_role" && rel.ToTable == "rbac_role_res" &&
			rel.ToCol == "role_id" && rel.Type == domain.RelationWrite {
			found = true
		}
	}
	if !found {
		t.Errorf("外键列名回退（rbac_role_res.role_id ↔ rbac_role）应建立 write，got %+v", rels)
	}
}
