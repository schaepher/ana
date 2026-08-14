package sqlite

import (
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

// 节点间路径查询（field_trace.md §17.3）。

func pathNode(id, name, file string, line int) *domain.CodeEntity {
	return &domain.CodeEntity{
		ID: domain.CanonicalID(id), Kind: domain.KindSSAValue, Name: name,
		FilePath: file, LineStart: line,
	}
}

// TestGetPathDataFlow：数据流路径存在——a#v0 → a#f.write → b#f.read → b#v1。
func TestGetPathDataFlow(t *testing.T) {
	r := newTestRepo(t)
	nodes := []*domain.CodeEntity{
		pathNode("symbol:go:example.com/m:a#v0", "v0", "a.go", 1),
		pathNode("symbol:go:example.com/m:a#f.write@2", "a.f", "a.go", 2),
		pathNode("symbol:go:example.com/m:b#f.read@3", "b.f", "b.go", 3),
		pathNode("symbol:go:example.com/m:b#v1", "v1", "b.go", 4),
		pathNode("symbol:go:example.com/m:c#v9", "v9", "c.go", 9),
	}
	edges := []*domain.Fact{
		{SourceID: "symbol:go:example.com/m:a#v0", TargetID: "symbol:go:example.com/m:a#f.write@2", Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: "symbol:go:example.com/m:a#f.write@2", TargetID: "symbol:go:example.com/m:b#f.read@3", Kind: domain.FactArgument, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: "symbol:go:example.com/m:b#f.read@3", TargetID: "symbol:go:example.com/m:b#v1", Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
	}
	save(t, r, nodes, edges)

	path, err := r.GetPath("symbol:go:example.com/m:a#v0", "symbol:go:example.com/m:b#v1", 50, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(path) != 4 {
		t.Fatalf("path len = %d, want 4（v0→a.f→b.f→v1）: %+v", len(path), path)
	}
	// 顺序与边类型
	ids := []string{}
	for _, p := range path {
		ids = append(ids, string(p.ID))
	}
	want := []string{
		"symbol:go:example.com/m:a#v0",
		"symbol:go:example.com/m:a#f.write@2",
		"symbol:go:example.com/m:b#f.read@3",
		"symbol:go:example.com/m:b#v1",
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("path[%d] = %s, want %s", i, ids[i], want[i])
		}
	}
	if path[1].EdgeKinds != "data_flows_to" || path[2].EdgeKinds != "argument" || path[3].EdgeKinds != "data_flows_to" {
		t.Errorf("边类型 = %+v", path)
	}
}

// TestGetPathUnreachable：不可达返回空（不报错）。
func TestGetPathUnreachable(t *testing.T) {
	r := newTestRepo(t)
	nodes := []*domain.CodeEntity{
		pathNode("symbol:go:example.com/m:a#v0", "v0", "a.go", 1),
		pathNode("symbol:go:example.com/m:c#v9", "v9", "c.go", 9),
	}
	save(t, r, nodes, nil)
	path, err := r.GetPath("symbol:go:example.com/m:a#v0", "symbol:go:example.com/m:c#v9", 50, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(path) != 0 {
		t.Errorf("不可达应有空路径: %+v", path)
	}
}

// TestGetPathCycle：环不挂（a→b→a），a→c 不可达返回空。
func TestGetPathCycle(t *testing.T) {
	r := newTestRepo(t)
	nodes := []*domain.CodeEntity{
		pathNode("symbol:go:example.com/m:a#v0", "v0", "a.go", 1),
		pathNode("symbol:go:example.com/m:b#v0", "v0", "b.go", 2),
		pathNode("symbol:go:example.com/m:c#v0", "v0", "c.go", 3),
	}
	edges := []*domain.Fact{
		{SourceID: "symbol:go:example.com/m:a#v0", TargetID: "symbol:go:example.com/m:b#v0", Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: "symbol:go:example.com/m:b#v0", TargetID: "symbol:go:example.com/m:a#v0", Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
	}
	save(t, r, nodes, edges)
	if _, err := r.GetPath("symbol:go:example.com/m:a#v0", "symbol:go:example.com/m:c#v0", 50, false); err != nil {
		t.Fatalf("环查询不应报错: %v", err)
	}
	path, err := r.GetPath("symbol:go:example.com/m:a#v0", "symbol:go:example.com/m:b#v0", 50, false)
	if err != nil || len(path) != 2 {
		t.Errorf("a→b 路径 = %+v, err %v", path, err)
	}
}

// TestGetPathCalls：--kind calls——函数调用路径。
func TestGetPathCalls(t *testing.T) {
	r := newTestRepo(t)
	nodes := []*domain.CodeEntity{
		pathNode("symbol:go:example.com/m:a", "a", "a.go", 1),
		pathNode("symbol:go:example.com/m:b", "b", "b.go", 2),
		pathNode("symbol:go:example.com/m:c", "c", "c.go", 3),
	}
	edges := []*domain.Fact{
		{SourceID: "symbol:go:example.com/m:a", TargetID: "symbol:go:example.com/m:b", Kind: domain.FactCalls, ToolSource: domain.ToolCodeGraph, Confidence: 0.8},
		{SourceID: "symbol:go:example.com/m:b", TargetID: "symbol:go:example.com/m:c", Kind: domain.FactCalls, ToolSource: domain.ToolCodeGraph, Confidence: 0.8},
	}
	save(t, r, nodes, edges)
	path, err := r.GetPath("symbol:go:example.com/m:a", "symbol:go:example.com/m:c", 50, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(path) != 3 || path[2].EdgeKinds != "calls" {
		t.Errorf("calls 路径 = %+v", path)
	}
	// 数据流边集下不可达
	path2, _ := r.GetPath("symbol:go:example.com/m:a", "symbol:go:example.com/m:c", 50, false)
	if len(path2) != 0 {
		t.Errorf("数据流边集下 calls 链不应可达: %+v", path2)
	}
}
