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
