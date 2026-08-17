package sqlite

import (
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

func TestGetValueTrace(t *testing.T) {
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
		{SourceID: argVal.ID, TargetID: paramVal.ID, Kind: domain.FactArgument, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: paramVal.ID, TargetID: fa.ID, Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
	})

	rows, err := r.GetValueTrace(fa.ID, 8, 0, false)
	if err != nil {
		t.Fatalf("GetValueTrace: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3 (fa + param + arg)", len(rows))
	}

	for _, row := range rows {
		if row.Depth == 0 && row.ID != fa.ID {
			t.Errorf("anchor = %s", row.ID)
		}
		if row.Depth == 1 {
			if row.ID != paramVal.ID || row.FuncID != funcID || row.Dir != 0 {
				t.Errorf("depth1 = %+v, want param in f dir0", row)
			}
		}
		if row.Depth == 2 {
			if row.ID != argVal.ID || row.FuncID != callerID || row.Dir != 0 {
				t.Errorf("depth2 = %+v, want arg in g dir0", row)
			}
		}
	}
}

// TestValueTraceMulti：⑧ 跳板合并——多锚点一次查询返回各锚点下游。
func TestValueTraceMulti(t *testing.T) {
	r := newTestRepo(t)
	funcID := "symbol:go:example.com/m:run"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(funcID + "#r1"), Kind: domain.KindFieldAccess, Name: "r1",
			Properties: map[string]any{"func_id": funcID, "full_path": "example.com/m.T.A", "access_kind": "read"}},
		{ID: domain.CanonicalID(funcID + "#r2"), Kind: domain.KindFieldAccess, Name: "r2",
			Properties: map[string]any{"func_id": funcID, "full_path": "example.com/m.T.A", "access_kind": "read"}},
		{ID: domain.CanonicalID(funcID + "#v1"), Kind: domain.KindSSAValue, Name: "v1",
			Properties: map[string]any{"func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#v2"), Kind: domain.KindSSAValue, Name: "v2",
			Properties: map[string]any{"func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#result"), Kind: domain.KindSSAValue, Name: "result",
			Properties: map[string]any{"func_id": funcID}},
	}
	edges := []*domain.Fact{
		{SourceID: domain.CanonicalID(funcID + "#r1"), TargetID: domain.CanonicalID(funcID + "#v1"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#r2"), TargetID: domain.CanonicalID(funcID + "#v2"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#v1"), TargetID: domain.CanonicalID(funcID + "#result"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#v2"), TargetID: domain.CanonicalID(funcID + "#result"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
	}
	save(t, r, nodes, edges)

	rows, err := r.GetValueTraceMulti([]domain.CanonicalID{
		domain.CanonicalID(funcID + "#r1"), domain.CanonicalID(funcID + "#r2"),
	}, "example.com/m.T.A", 4)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, row := range rows {
		seen[string(row.ID)] = true
	}
	for _, want := range []string{funcID + "#v1", funcID + "#v2", funcID + "#result"} {
		if !seen[want] {
			t.Errorf("合并追踪缺节点 %s", want)
		}
	}
}

// TestValueTraceConvergeDedup：Q155——递归 CTE 按 (id, dir) 去重。汇聚点
// （多条路径到达同一节点）只输出一行（最短深度），行数随路径数收敛而非
// 放大：v0 → x（直接）与 v0 → a → x（绕行）两条路径达 x，x 与 y 各只
// 出现一次（现状：x/y 每路径一行，共 7 行）。
func TestValueTraceConvergeDedup(t *testing.T) {
	r := newTestRepo(t)
	funcID := "symbol:go:example.com/m:run"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(funcID + "#v0"), Kind: domain.KindSSAValue, Name: "v0",
			Properties: map[string]any{"func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#a"), Kind: domain.KindSSAValue, Name: "a",
			Properties: map[string]any{"func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#x"), Kind: domain.KindSSAValue, Name: "x",
			Properties: map[string]any{"func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#y"), Kind: domain.KindSSAValue, Name: "y",
			Properties: map[string]any{"func_id": funcID}},
	}
	edges := []*domain.Fact{

		{SourceID: domain.CanonicalID(funcID + "#v0"), TargetID: domain.CanonicalID(funcID + "#x"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#v0"), TargetID: domain.CanonicalID(funcID + "#a"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#a"), TargetID: domain.CanonicalID(funcID + "#x"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#x"), TargetID: domain.CanonicalID(funcID + "#y"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
	}
	save(t, r, nodes, edges)

	rows, err := r.GetValueTrace(domain.CanonicalID(funcID+"#v0"), 8, 0, false)
	if err != nil {
		t.Fatal(err)
	}

	if len(rows) != 4 {
		t.Errorf("rows = %d, want 4（锚点一行 + a + x + y）", len(rows))
	}
	countX, depthX, countY, depthY := 0, -1, 0, -1
	for _, row := range rows {
		switch string(row.ID) {
		case funcID + "#x":
			countX++
			depthX = row.Depth
		case funcID + "#y":
			countY++
			depthY = row.Depth
		}
	}
	if countX != 1 {
		t.Errorf("x 行数 = %d, want 1（汇聚去重）", countX)
	}
	if depthX != 1 {
		t.Errorf("x depth = %d, want 1（最短路径）", depthX)
	}
	if countY != 1 {
		t.Errorf("y 行数 = %d, want 1（汇聚去重）", countY)
	}
	if depthY != 2 {
		t.Errorf("y depth = %d, want 2", depthY)
	}
}
