package sqlite

import (
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

// TestValueTraceFieldAnchorNoCrossField：⑥ 字段精度——从字段锚点追踪时，
// 共享值节点引入的其他字段访问不得入链（T.B 读不混入 T.A 的链）。
func TestValueTraceFieldAnchorNoCrossField(t *testing.T) {
	r := newTestRepo(t)
	funcID := "symbol:go:example.com/m:run"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(funcID + "#faA.read@1"), Kind: domain.KindFieldAccess, Name: "faA",
			Properties: map[string]any{"func_id": funcID, "full_path": "example.com/m.T.A", "access_kind": "read"}},
		{ID: domain.CanonicalID(funcID + "#faA.write@2"), Kind: domain.KindFieldAccess, Name: "faA",
			Properties: map[string]any{"func_id": funcID, "full_path": "example.com/m.T.A", "access_kind": "write"}},
		{ID: domain.CanonicalID(funcID + "#faB.read@3"), Kind: domain.KindFieldAccess, Name: "faB",
			Properties: map[string]any{"func_id": funcID, "full_path": "example.com/m.T.B", "access_kind": "read"}},
		{ID: domain.CanonicalID(funcID + "#v0"), Kind: domain.KindSSAValue, Name: "v0",
			Properties: map[string]any{"func_id": funcID}},
	}
	edges := []*domain.Fact{
		{SourceID: domain.CanonicalID(funcID + "#faA.read@1"), TargetID: domain.CanonicalID(funcID + "#v0"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#v0"), TargetID: domain.CanonicalID(funcID + "#faA.write@2"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},

		{SourceID: domain.CanonicalID(funcID + "#faB.read@3"), TargetID: domain.CanonicalID(funcID + "#v0"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
	}
	save(t, r, nodes, edges)

	rows, err := r.GetValueTrace(domain.CanonicalID(funcID+"#faA.write@2"), 8, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	hasSrc, hasSelf := false, false
	for _, row := range rows {
		if row.Name == "faB" {
			hasSrc = true
		}
		if row.Name == "faA" && row.Kind == domain.KindFieldAccess {
			hasSelf = true
		}
	}
	if !hasSelf {
		t.Errorf("字段锚点追踪应含同字段读 faA: %+v", rows)
	}
	if !hasSrc {
		t.Errorf("字段锚点反向应含值来源读跳板 faB（v0 的 phi 来源）: %+v", rows)
	}

	rows, err = r.GetValueTrace(domain.CanonicalID(funcID+"#v0"), 8, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Dir == 0 && row.Kind == domain.KindFieldAccess && row.Access != "read" {
			t.Errorf("对象锚点反向不应含写 %s", row.Name)
		}
		if row.Dir == 1 && row.Kind == domain.KindFieldAccess && row.Access != "write" {
			t.Errorf("对象锚点正向不应含读 %s", row.Name)
		}
	}
}

// TestGetValueTraceMinConf：Q161——动态候选边（metadata 带
// candidate_origin/confidence）低于阈值时被 BFS 剪枝；普通边（无
// 候选 metadata）不受影响。
func TestGetValueTraceMinConf(t *testing.T) {
	r := newTestRepo(t)
	callerID := "symbol:go:example.com/m:g"
	funcID := "symbol:go:example.com/m:f"
	caller := node(callerID, "function", "g", "g.go")
	fn := node(funcID, "function", "f", "f.go")
	argVal := node(callerID+"#t0", "ssa_value", "t0", "g.go")
	argVal.Properties["func_id"] = callerID
	paramVal := node(funcID+"#a", "ssa_value", "a", "f.go")
	paramVal.Properties["func_id"] = funcID
	fa := faNodeAccess(funcID+"#a.X.read@3", funcID, "example.com/m.T.X", "a.X", 3, "read")
	save(t, r, []*domain.CodeEntity{caller, fn, argVal, paramVal, fa}, []*domain.Fact{

		{SourceID: argVal.ID, TargetID: paramVal.ID, Kind: domain.FactArgument, ToolSource: domain.ToolSSA,
			Confidence: 1, Metadata: map[string]any{"interface": "example.com/m.Fee",
				"candidate_origin": "enum", "confidence": 0.7}},
		{SourceID: paramVal.ID, TargetID: fa.ID, Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
	})

	rows, err := r.GetValueTrace(fa.ID, 8, 0, false)
	if err != nil {
		t.Fatalf("GetValueTrace(0): %v", err)
	}
	found := false
	for _, row := range rows {
		if row.ID == argVal.ID {
			found = true
			if row.EdgeOrigin != "enum" || row.EdgeConf != 0.7 || row.EdgeIface != "example.com/m.Fee" {
				t.Errorf("候选边标注 = %s/%v/%s, want enum/0.7/example.com/m.Fee",
					row.EdgeOrigin, row.EdgeConf, row.EdgeIface)
			}
		}
	}
	if !found {
		t.Fatal("minConf=0 时候选路径应可达 argVal")
	}

	rows, err = r.GetValueTrace(fa.ID, 8, 0.8, false)
	if err != nil {
		t.Fatalf("GetValueTrace(0.8): %v", err)
	}
	for _, row := range rows {
		if row.ID == argVal.ID {
			t.Error("minConf=0.8 时候选路径不应出现 argVal")
		}
	}
}

// TestGetValueTraceEdgeCandidateFwd：Q161 场景树——正向（dir=1 使用链）
// 经候选 returns 边到达的行同样标注候选元数据（出边 source=dp.id）。
func TestGetValueTraceEdgeCandidateFwd(t *testing.T) {
	r := newTestRepo(t)
	callerID := "symbol:go:example.com/m:g"
	implID := "symbol:go:example.com/m:(Impl).M"
	callVal := node(callerID+"#t0", "ssa_value", "t0", "g.go")
	callVal.Properties["func_id"] = callerID
	retVal := node(implID+"#t5", "ssa_value", "t5", "i.go")
	retVal.Properties["func_id"] = implID
	save(t, r, []*domain.CodeEntity{callVal, retVal}, []*domain.Fact{

		{SourceID: retVal.ID, TargetID: callVal.ID, Kind: domain.FactReturns, ToolSource: domain.ToolSSA,
			Confidence: 1, Metadata: map[string]any{"interface": "example.com/m.Iface",
				"candidate_origin": "register", "confidence": 0.9}},
	})

	rows, err := r.GetValueTrace(callVal.ID, 8, 0, false)
	if err != nil {
		t.Fatalf("GetValueTrace: %v", err)
	}
	marked := false
	for _, row := range rows {
		if row.ID == retVal.ID {
			marked = true
			if row.Dir != 0 {
				t.Errorf("ret 行 dir = %d, want 0（产生链）", row.Dir)
			}
			if row.EdgeOrigin != "register" || row.EdgeConf != 0.9 || row.EdgeIface != "example.com/m.Iface" {
				t.Errorf("候选 returns 标注 = %s/%v/%s, want register/0.9/example.com/m.Iface",
					row.EdgeOrigin, row.EdgeConf, row.EdgeIface)
			}
		}
	}
	if !marked {
		t.Fatal("反向链未到达候选返回值 t5 且未标注")
	}
}

// TestGetValueTraceContainerBoundary：Q163 回归——默认（精确匹配）从
// 叶子字段 SettledFee.write 追踪到"容器读节点"（inst=invoice）即断，
// 不进入 RefundSource 候选实现路径；--include-container 显式放行父
// 容器路径后可达，且候选标记沿路径累计（refundParam 行继承 returns
// 候选边的状态，不被后续普通边覆盖）。
func TestGetValueTraceContainerBoundary(t *testing.T) {
	r := newTestRepo(t)
	funcID := "symbol:go:example.com/m:calc"

	write := faNodeAccess(funcID+"#invoice.SettledFee.write@3", funcID,
		"example.com/m.Invoice.SettledFee", "invoice.SettledFee", 3, "write")

	v := node(funcID+"#t0", "ssa_value", "t0", "m.go")
	v.Properties["func_id"] = funcID

	invRead := faNodeAccess(funcID+"#invoice.read@5", funcID,
		"example.com/m.Invoice", "invoice", 5, "read")
	invRead.Properties["type_string"] = "*example.com/m.Invoice"

	rv := node(funcID+"#rv", "ssa_value", "rv", "m.go")
	rv.Properties["func_id"] = funcID

	refundParam := node(funcID+"#refund", "ssa_value", "refund", "m.go")
	refundParam.Properties["func_id"] = funcID
	save(t, r, []*domain.CodeEntity{write, v, invRead, rv, refundParam}, []*domain.Fact{
		{SourceID: v.ID, TargetID: write.ID, Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: invRead.ID, TargetID: v.ID, Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: rv.ID, TargetID: invRead.ID, Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},

		{SourceID: refundParam.ID, TargetID: rv.ID, Kind: domain.FactReturns, ToolSource: domain.ToolSSA,
			Confidence: 1, Metadata: map[string]any{"interface": "example.com/m.RefundSource",
				"candidate_origin": "enum", "confidence": 0.7}},
	})

	rows, err := r.GetValueTrace(write.ID, 8, 1.0, false)
	if err != nil {
		t.Fatalf("GetValueTrace: %v", err)
	}
	for _, row := range rows {
		if row.ID == refundParam.ID {
			t.Error("默认模式不应出现 RefundSource 路径（候选边越界）")
		}
	}

	rows, err = r.GetValueTrace(write.ID, 8, 0, true)
	if err != nil {
		t.Fatalf("GetValueTrace(includeContainer): %v", err)
	}
	marked := false
	invSeen := false
	for _, row := range rows {
		if row.ID == refundParam.ID {
			marked = true
			if row.EdgeOrigin != "enum" || row.EdgeConf != 0.7 || row.EdgeIface != "example.com/m.RefundSource" {
				t.Errorf("候选标记 = %s/%v/%s, want enum/0.7/example.com/m.RefundSource（累计不被普通边覆盖）",
					row.EdgeOrigin, row.EdgeConf, row.EdgeIface)
			}
		}
		if row.ID == invRead.ID {
			invSeen = true
		}
	}
	if !marked {
		t.Fatal("includeContainer 模式 refundParam 应出现且标候选")
	}
	if !invSeen {
		t.Error("includeContainer 模式容器读节点应放行")
	}
}
