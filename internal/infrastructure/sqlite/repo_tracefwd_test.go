package sqlite

import (
	"strings"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

func TestTraceForwardUsagePoint(t *testing.T) {
	r := newTestRepo(t)
	funcID := "symbol:go:example.com/m:f"

	read := faNode(funcID+"#x.A.read@3", funcID, "example.com/m.T.A", "x.A", 3)
	result := svNode(funcID+"#t1", funcID)
	write := faNode(funcID+"#x.A.write@5", funcID, "example.com/m.T.A", "x.A", 5)
	save(t, r, []*domain.CodeEntity{read, result, write},
		[]*domain.Fact{dfEdge(read.ID, result.ID), dfEdge(result.ID, write.ID)})

	rows, err := r.TraceForward("example.com/m.T.A", domain.CanonicalID(funcID), 8)
	if err != nil {
		t.Fatalf("TraceForward: %v", err)
	}

	if len(rows) != 4 {
		t.Fatalf("rows = %d, want 4 (2 anchors + result + write)", len(rows))
	}
	if rows[2].Depth != 1 || string(rows[2].ID) != string(result.ID) {
		t.Errorf("depth1 row = %+v, want result", rows[2])
	}
	last := rows[3]
	if last.Depth != 2 || !last.IsUsage {
		t.Errorf("write node should be usage point at depth 2: %+v", last)
	}
}

// TestTraceForwardParamStart：trace-forward 参数起点（① 回归）——
// 调用方函数内无字段直接访问时，从参数经 argument 进入 callee 写入。
func TestTraceForwardParamStart(t *testing.T) {
	r := newTestRepo(t)
	runID := "symbol:go:example.com/m:run"
	fillID := "symbol:go:example.com/m:fillParam"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(runID), Kind: domain.KindFunction, Name: "run"},
		{ID: domain.CanonicalID(fillID), Kind: domain.KindFunction, Name: "fillParam"},

		{ID: domain.CanonicalID(runID + "#c"), Kind: domain.KindSSAValue, Name: "c",
			Properties: map[string]any{"func_id": runID, "origin_kind": "param", "type_string": "*example.com/m.Cfg"}},
		{ID: domain.CanonicalID(fillID + "#c"), Kind: domain.KindSSAValue, Name: "c",
			Properties: map[string]any{"func_id": fillID, "origin_kind": "param", "type_string": "*example.com/m.Cfg"}},

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
	if _, err := r.SaveBatchStats(nodes, edges, nil); err != nil {
		t.Fatalf("save: %v", err)
	}
	rows, err := r.TraceForward("example.com/m.Cfg.Key", domain.CanonicalID(runID), 8)
	if err != nil {
		t.Fatal(err)
	}
	hit := false
	for _, row := range rows {
		if strings.Contains(row.Name, "c.Key") {
			hit = true
		}
	}
	if !hit {
		t.Errorf("TraceForward 应从 run 参数经 argument 进入 callee 的 c.Key 写入: %+v", rows)
	}
}

// TestTraceForwardPkgBoundary：⑬ 猎 bug——跳板容器判据的包路径 LIKE
// 不得误匹配同名前缀包（example.com/app2 的类型不得被 example.com/app
// 的 LIKE '%example.com/app%' 放行）。
func TestTraceForwardPkgBoundary(t *testing.T) {
	r := newTestRepo(t)
	runID := "symbol:go:example.com/app:run"
	fillID := "symbol:go:example.com/app2:fill"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(runID), Kind: domain.KindFunction, Name: "run"},
		{ID: domain.CanonicalID(fillID), Kind: domain.KindFunction, Name: "fill"},
		{ID: domain.CanonicalID(runID + "#c"), Kind: domain.KindSSAValue, Name: "c",
			Properties: map[string]any{"func_id": runID, "origin_kind": "param", "type_string": "*example.com/app.T"}},
		{ID: domain.CanonicalID(fillID + "#c"), Kind: domain.KindSSAValue, Name: "c",
			Properties: map[string]any{"func_id": fillID, "origin_kind": "param", "type_string": "*example.com/app.T"}},

		{ID: domain.CanonicalID(fillID + "#c.Other.read@8"), Kind: domain.KindFieldAccess, Name: "c.Other",
			Properties: map[string]any{"func_id": fillID, "full_path": "example.com/app2.T2.Other",
				"access_kind": "read", "type_string": "example.com/app2.T2"}},

		{ID: domain.CanonicalID(fillID + "#c.FinalFee.write@9"), Kind: domain.KindFieldAccess, Name: "c.FinalFee",
			Properties: map[string]any{"func_id": fillID, "full_path": "example.com/app.T.FinalFee",
				"access_kind": "write"}},
	}
	edges := []*domain.Fact{
		{SourceID: domain.CanonicalID(runID + "#c"), TargetID: domain.CanonicalID(fillID + "#c"),
			Kind: domain.FactArgument, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(fillID + "#c"), TargetID: domain.CanonicalID(fillID + "#c.Other.read@8"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(fillID + "#c"), TargetID: domain.CanonicalID(fillID + "#c.FinalFee.write@9"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
	}
	save(t, r, nodes, edges)
	rows, err := r.TraceForward("example.com/app.T.FinalFee", domain.CanonicalID(runID), 8)
	if err != nil {
		t.Fatal(err)
	}
	hasWrite := false
	for _, row := range rows {
		if strings.Contains(row.Name, "Other") {
			t.Errorf("前缀包 example.com/app2 的类型被容器 LIKE 误放行: %s", row.Name)
		}
		if string(row.ID) == fillID+"#c.FinalFee.write@9" {
			hasWrite = true
		}
	}
	if !hasWrite {
		t.Errorf("目标写入未到达: %+v", rows)
	}
}

// TestTraceForwardGlobalStart：已验场景单元测试化——global 值起点
// （无 func_id、origin_kind=global——起点条件须放行）。
func TestTraceForwardGlobalStart(t *testing.T) {
	r := newTestRepo(t)
	runID := "symbol:go:example.com/m:run"
	fillID := "symbol:go:example.com/m:fill"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(runID), Kind: domain.KindFunction, Name: "run"},
		{ID: domain.CanonicalID(fillID), Kind: domain.KindFunction, Name: "fill"},

		{ID: domain.CanonicalID("symbol:go:example.com/m:var.g"), Kind: domain.KindSSAValue, Name: "g",
			Properties: map[string]any{"origin_kind": "global", "type_string": "*example.com/m.Record"}},
		{ID: domain.CanonicalID(fillID + "#c"), Kind: domain.KindSSAValue, Name: "c",
			Properties: map[string]any{"func_id": fillID, "origin_kind": "param"}},
		{ID: domain.CanonicalID(fillID + "#c.FinalFee.write@8"), Kind: domain.KindFieldAccess, Name: "c.FinalFee",
			Properties: map[string]any{"func_id": fillID, "full_path": "example.com/m.Record.FinalFee",
				"access_kind": "write"}},
	}
	edges := []*domain.Fact{
		{SourceID: domain.CanonicalID("symbol:go:example.com/m:var.g"), TargetID: domain.CanonicalID(fillID + "#c"),
			Kind: domain.FactArgument, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(fillID + "#c"), TargetID: domain.CanonicalID(fillID + "#c.FinalFee.write@8"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
	}
	save(t, r, nodes, edges)
	rows, err := r.TraceForward("example.com/m.Record.FinalFee", domain.CanonicalID(runID), 8)
	if err != nil {
		t.Fatal(err)
	}
	hasWrite := false
	for _, row := range rows {
		if string(row.ID) == fillID+"#c.FinalFee.write@8" {
			hasWrite = true
		}
	}
	if !hasWrite {
		t.Errorf("global 值起点未连到写入: %+v", rows)
	}
}

// TestTraceForwardTypeMatchStart：已验场景单元测试化——与目标字段同类型
// 的 local/phi 值起点（type_string 匹配，⑭）。
func TestTraceForwardTypeMatchStart(t *testing.T) {
	r := newTestRepo(t)
	runID := "symbol:go:example.com/m:run"
	fillID := "symbol:go:example.com/m:fill"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(runID), Kind: domain.KindFunction, Name: "run"},
		{ID: domain.CanonicalID(fillID), Kind: domain.KindFunction, Name: "fill"},

		{ID: domain.CanonicalID(runID + "#obj"), Kind: domain.KindSSAValue, Name: "obj",
			Properties: map[string]any{"func_id": runID, "origin_kind": "call",
				"type_string": "*example.com/m.Record"}},
		{ID: domain.CanonicalID(fillID + "#c"), Kind: domain.KindSSAValue, Name: "c",
			Properties: map[string]any{"func_id": fillID, "origin_kind": "param"}},
		{ID: domain.CanonicalID(fillID + "#c.FinalFee.write@8"), Kind: domain.KindFieldAccess, Name: "c.FinalFee",
			Properties: map[string]any{"func_id": fillID, "full_path": "example.com/m.Record.FinalFee",
				"access_kind": "write"}},
	}
	edges := []*domain.Fact{
		{SourceID: domain.CanonicalID(runID + "#obj"), TargetID: domain.CanonicalID(fillID + "#c"),
			Kind: domain.FactArgument, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(fillID + "#c"), TargetID: domain.CanonicalID(fillID + "#c.FinalFee.write@8"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
	}
	save(t, r, nodes, edges)
	rows, err := r.TraceForward("example.com/m.Record.FinalFee", domain.CanonicalID(runID), 8)
	if err != nil {
		t.Fatal(err)
	}
	hasWrite := false
	for _, row := range rows {
		if string(row.ID) == fillID+"#c.FinalFee.write@8" {
			hasWrite = true
		}
	}
	if !hasWrite {
		t.Errorf("同类型 local 值起点未连到写入: %+v", rows)
	}
}
