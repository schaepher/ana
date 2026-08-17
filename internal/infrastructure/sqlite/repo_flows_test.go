package sqlite

import (
	"strings"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

func TestGetFunctionFlows(t *testing.T) {
	r := newTestRepo(t)
	funcID := "symbol:go:example.com/m:f"

	write := faNode(funcID+"#x.A.write@3", funcID, "example.com/m.T.A", "x.A", 3)
	result := svNode(funcID+"#t1", funcID)
	read := faNodeAccess(funcID+"#x.A.read@5", funcID, "example.com/m.T.A", "x.A", 5, "read")

	other := faNode("symbol:go:example.com/m:g#y.B.write@9", "symbol:go:example.com/m:g",
		"example.com/m.T.B", "y.B", 9)
	save(t, r, []*domain.CodeEntity{write, result, read, other},
		[]*domain.Fact{
			dfEdge(write.ID, result.ID),
			dfEdge(result.ID, read.ID),
			dfEdge(write.ID, other.ID),
		})

	rows, err := r.GetFunctionFlows(domain.CanonicalID(funcID), 8)
	if err != nil {
		t.Fatalf("GetFunctionFlows: %v", err)
	}

	if len(rows) != 6 {
		t.Fatalf("rows = %d, want 6: %+v", len(rows), rows)
	}
	for _, row := range rows {
		if row.Kind != domain.KindFieldAccess && row.Kind != domain.KindSSAValue {
			t.Errorf("unexpected kind %s: %+v", row.Kind, row)
		}
		if string(row.ID) == string(other.ID) {
			t.Errorf("cross-function node leaked into flows: %s", other.ID)
		}
	}
	// 使用链（dir=1）：result@1 → read@2
	var resultFwd, readFwd *domain.TraceRow
	for _, row := range rows {
		if row.Dir != 1 {
			continue
		}
		if row.ID == result.ID {
			resultFwd = row
		}
		if row.ID == read.ID {
			readFwd = row
		}
	}
	if resultFwd == nil || readFwd == nil {
		t.Fatalf("forward chain missing: %+v", rows)
	}
	if resultFwd.Depth != 1 {
		t.Errorf("result forward depth = %d, want 1", resultFwd.Depth)
	}
	if readFwd.Depth != 2 || readFwd.Access != "read" {
		t.Errorf("read forward = depth %d access %q, want 2/read", readFwd.Depth, readFwd.Access)
	}
	// 产生链（dir=0）：result@1（从 read 反向）→ write@2
	var writeBack *domain.TraceRow
	for _, row := range rows {
		if row.Dir == 0 && row.ID == write.ID && row.Depth == 2 {
			writeBack = row
		}
	}
	if writeBack == nil {
		t.Errorf("backward chain write@2 missing: %+v", rows)
	}
}

// TestFlowsFieldScoped：⑥ 字段精度——flows 递归的字段访问步限定起始
// 字段（A 链不混入 B 访问）。
func TestFlowsFieldScoped(t *testing.T) {
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

	rows, err := r.GetFunctionFlows(domain.CanonicalID(funcID), 8)
	if err != nil {
		t.Fatal(err)
	}

	for _, row := range rows {
		if row.Kind == domain.KindFieldAccess && strings.Contains(row.Name, "faA") &&
			row.FullPath != "example.com/m.T.A" {
			t.Errorf("flows A 链混入其他字段 %s (%s)", row.Name, row.FullPath)
		}
	}
}

// TestTraceCycle：⑬ 猎 bug——trace-forward 环（a→b→a）不挂且行数有限。
func TestTraceCycle(t *testing.T) {
	r := newTestRepo(t)
	funcID := "symbol:go:example.com/m:run"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(funcID + "#p"), Kind: domain.KindSSAValue, Name: "p",
			Properties: map[string]any{"func_id": funcID, "origin_kind": "param"}},
		{ID: domain.CanonicalID(funcID + "#a"), Kind: domain.KindSSAValue, Name: "a",
			Properties: map[string]any{"func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#b"), Kind: domain.KindSSAValue, Name: "b",
			Properties: map[string]any{"func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#f.write@5"), Kind: domain.KindFieldAccess, Name: "f",
			Properties: map[string]any{"func_id": funcID, "full_path": "example.com/m.T.F", "access_kind": "write"}},
	}
	edges := []*domain.Fact{
		{SourceID: domain.CanonicalID(funcID + "#p"), TargetID: domain.CanonicalID(funcID + "#a"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#a"), TargetID: domain.CanonicalID(funcID + "#b"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#b"), TargetID: domain.CanonicalID(funcID + "#a"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#a"), TargetID: domain.CanonicalID(funcID + "#f.write@5"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
	}
	save(t, r, nodes, edges)
	rows, err := r.TraceForward("example.com/m.T.F", domain.CanonicalID(funcID), 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) > 40 {
		t.Errorf("环导致行数爆炸: %d", len(rows))
	}
	hit := false
	for _, row := range rows {
		if string(row.ID) == funcID+"#f.write@5" {
			hit = true
		}
	}
	if !hit {
		t.Errorf("环场景目标写入未到达: %+v", rows)
	}
}

// TestValueTraceCycle：⑬ 猎 bug——value-trace 环不挂。
func TestValueTraceCycle(t *testing.T) {
	r := newTestRepo(t)
	funcID := "symbol:go:example.com/m:run"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(funcID + "#a"), Kind: domain.KindSSAValue, Name: "a",
			Properties: map[string]any{"func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#b"), Kind: domain.KindSSAValue, Name: "b",
			Properties: map[string]any{"func_id": funcID}},
	}
	edges := []*domain.Fact{
		{SourceID: domain.CanonicalID(funcID + "#a"), TargetID: domain.CanonicalID(funcID + "#b"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#b"), TargetID: domain.CanonicalID(funcID + "#a"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
	}
	save(t, r, nodes, edges)
	rows, err := r.GetValueTrace(domain.CanonicalID(funcID+"#a"), 8, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) > 40 {
		t.Errorf("value-trace 环行数爆炸: %d", len(rows))
	}
}
