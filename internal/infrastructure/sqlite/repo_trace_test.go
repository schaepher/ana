package sqlite

import (
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

func TestTraceBackwardMultiHop(t *testing.T) {
	r := newTestRepo(t)
	funcID := "symbol:go:example.com/m:f"

	fa := faNode(funcID+"#x.A.write@3", funcID, "example.com/m.T.A", "x.A", 3)
	b := svNode(funcID+"#t1", funcID)
	a := svNode(funcID+"#t0", funcID)
	save(t, r, []*domain.CodeEntity{fa, b, a}, []*domain.Fact{dfEdge(a.ID, b.ID), dfEdge(b.ID, fa.ID)})

	rows, err := r.TraceBackward("example.com/m.T.A", domain.CanonicalID(funcID), 8)
	if err != nil {
		t.Fatalf("TraceBackward: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3 (field + 2 hops)", len(rows))
	}

	depth0, depth1, depth2 := rows[0], rows[1], rows[2]
	if depth0.Depth != 0 || depth1.Depth != 1 || depth2.Depth != 2 {
		t.Errorf("depths = %d,%d,%d", depth0.Depth, depth1.Depth, depth2.Depth)
	}
	if string(depth0.ID) != string(fa.ID) || string(depth1.ID) != string(b.ID) || string(depth2.ID) != string(a.ID) {
		t.Errorf("chain = %s,%s,%s", depth0.ID, depth1.ID, depth2.ID)
	}
	if depth1.EdgeKinds != "data_flows_to" {
		t.Errorf("edge kinds = %q", depth1.EdgeKinds)
	}

	rows, err = r.TraceBackward("example.com/m.T.A", domain.CanonicalID(funcID), 1)
	if err != nil {
		t.Fatalf("TraceBackward depth1: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("depth-limited rows = %d, want 2", len(rows))
	}
}

// TestTraceBackwardCrossFunction：⑬ 猎 bug——trace-backward 从 callee
// 的写入出发，经 argument 反向连到调用方的产生点（值来源）。
func TestTraceBackwardCrossFunction(t *testing.T) {
	r := newTestRepo(t)
	runID := "symbol:go:example.com/m:run"
	fillID := "symbol:go:example.com/m:fill"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(runID), Kind: domain.KindFunction, Name: "run"},
		{ID: domain.CanonicalID(fillID), Kind: domain.KindFunction, Name: "fill"},
		{ID: domain.CanonicalID(runID + "#c"), Kind: domain.KindSSAValue, Name: "c",
			Properties: map[string]any{"func_id": runID, "origin_kind": "param"}},
		{ID: domain.CanonicalID(fillID + "#c"), Kind: domain.KindSSAValue, Name: "c",
			Properties: map[string]any{"func_id": fillID, "origin_kind": "param"}},
		{ID: domain.CanonicalID(fillID + "#c.Key.write@8"), Kind: domain.KindFieldAccess, Name: "c.Key",
			Properties: map[string]any{"func_id": fillID, "full_path": "example.com/m.Cfg.Key",
				"access_kind": "write"}},
	}
	edges := []*domain.Fact{
		{SourceID: domain.CanonicalID(runID + "#c"), TargetID: domain.CanonicalID(fillID + "#c"),
			Kind: domain.FactArgument, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(fillID + "#c"), TargetID: domain.CanonicalID(fillID + "#c.Key.write@8"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
	}
	save(t, r, nodes, edges)
	rows, err := r.TraceBackward("example.com/m.Cfg.Key", domain.CanonicalID(fillID), 8)
	if err != nil {
		t.Fatal(err)
	}
	hasCaller := false
	for _, row := range rows {
		if string(row.ID) == runID+"#c" {
			hasCaller = true
		}
	}
	if !hasCaller {
		t.Errorf("backward 未连到调用方产生点 run#c: %+v", rows)
	}
}

// TestFindFieldReadsOrder：⑬ 猎 bug——FindFieldReads 结果顺序稳定
// （ResolveAnchor 取首个做锚点——顺序不稳定会导致锚点漂移）。
func TestFindFieldReadsOrder(t *testing.T) {
	r := newTestRepo(t)
	funcID := "symbol:go:example.com/m:run"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(funcID + "#r1"), Kind: domain.KindFieldAccess, Name: "t.A",
			LineStart: 9, Properties: map[string]any{"func_id": funcID, "full_path": "example.com/m.T.A", "access_kind": "read"}},
		{ID: domain.CanonicalID(funcID + "#r2"), Kind: domain.KindFieldAccess, Name: "t.A",
			LineStart: 3, Properties: map[string]any{"func_id": funcID, "full_path": "example.com/m.T.A", "access_kind": "read"}},
	}
	save(t, r, nodes, nil)
	first := func() string {
		rows, err := r.FindFieldReads("example.com/m.T.A")
		if err != nil || len(rows) == 0 {
			t.Fatalf("FindFieldReads: %v", err)
		}
		return string(rows[0].ID)
	}

	want := string(nodes[1].ID)
	for i := 0; i < 3; i++ {
		if got := first(); got != want {
			t.Fatalf("FindFieldReads 首个不稳定: %s != %s", got, want)
		}
	}
}

// TestTraceBackwardIndirect：Q172——trace-backward --follow-indirect。
// outer 对 T.A 只有 indirect_write（经 inner 间接写），链解析：
// summary_origins outer→inner→fill（真实写 t.A + 赋值来源 v）。
// 断言返回 fill 的写节点与赋值来源值节点。
func TestTraceBackwardIndirect(t *testing.T) {
	r := newTestRepo(t)
	outerID := "symbol:go:example.com/m:outer"
	innerID := "symbol:go:example.com/m:inner"
	fillID := "symbol:go:example.com/m:fill"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(outerID), Kind: domain.KindFunction, Name: "outer"},
		{ID: domain.CanonicalID(innerID), Kind: domain.KindFunction, Name: "inner"},
		{ID: domain.CanonicalID(fillID), Kind: domain.KindFunction, Name: "fill"},

		{ID: domain.CanonicalID(fillID + "#t.A.write@9"), Kind: domain.KindFieldAccess,
			Name: "t.A", FilePath: "f.go", LineStart: 9,
			Properties: map[string]any{"full_path": "example.com/m.T.A",
				"instance_path": "t.A", "access_kind": "write", "func_id": fillID}},
		{ID: domain.CanonicalID(fillID + "#v"), Kind: domain.KindSSAValue, Name: "v",
			Properties: map[string]any{"func_id": fillID}},
	}
	if _, err := r.SaveBatchStats(nodes, []*domain.Fact{
		{SourceID: domain.CanonicalID(fillID + "#v"), TargetID: domain.CanonicalID(fillID + "#t.A.write@9"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
	}, []*domain.FunctionFieldSummary{
		{FunctionID: domain.CanonicalID(outerID), AccessKind: domain.SummaryIndirectWrite,
			FieldPath: "example.com/m.T.A", LineStart: 2},
		{FunctionID: domain.CanonicalID(innerID), AccessKind: domain.SummaryIndirectWrite,
			FieldPath: "example.com/m.T.A", LineStart: 5},
	}, []*domain.SummaryOrigin{
		{FunctionID: domain.CanonicalID(outerID), AccessKind: domain.SummaryIndirectWrite,
			FieldPath: "example.com/m.T.A", CallLine: 3, CalleeID: domain.CanonicalID(innerID)},
		{FunctionID: domain.CanonicalID(innerID), AccessKind: domain.SummaryIndirectWrite,
			FieldPath: "example.com/m.T.A", CallLine: 6, CalleeID: domain.CanonicalID(fillID)},
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	rows, err := r.TraceBackwardIndirect("example.com/m.T.A", domain.CanonicalID(outerID), 8)
	if err != nil {
		t.Fatalf("TraceBackwardIndirect: %v", err)
	}
	var writeSeen, valSeen bool
	for _, row := range rows {
		if row.ID == domain.CanonicalID(fillID+"#t.A.write@9") {
			writeSeen = true
		}
		if row.ID == domain.CanonicalID(fillID+"#v") {
			valSeen = true
		}
	}
	if !writeSeen {
		t.Error("--follow-indirect 应返回 fill 的真实写节点 t.A.write")
	}
	if !valSeen {
		t.Error("--follow-indirect 应返回赋值来源值节点 v")
	}

	plain, err := r.TraceBackward("example.com/m.T.A", domain.CanonicalID(outerID), 8)
	if err != nil {
		t.Fatalf("TraceBackward: %v", err)
	}
	if len(plain) != 0 {
		t.Errorf("默认 trace-backward 应为空（outer 无真实 field_access），got %d", len(plain))
	}
}
