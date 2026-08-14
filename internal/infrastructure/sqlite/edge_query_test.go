package sqlite

import (
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

// TestGetIndirectWriteEdges：P0 补全——只返回目标函数的 indirect_write 边
// （kind 过滤 + source 精确匹配），其他 kind 不入结果；无边返回空不报错。
func TestGetIndirectWriteEdges(t *testing.T) {
	r := newTestRepo(t)
	funcID := "symbol:go:example.com/m:run"
	other := "symbol:go:example.com/m:other"
	nodes := []*domain.CodeEntity{
		node(funcID, "function", "run", "a.go"),
		node(other, "function", "other", "a.go"),
		node(funcID+"#w1", "field_access", "t.A", "a.go"),
		node(funcID+"#w2", "field_access", "t.B", "a.go"),
	}
	edges := []*domain.Fact{
		{SourceID: domain.CanonicalID(funcID), TargetID: domain.CanonicalID(funcID + "#w1"),
			Kind: domain.FactIndirectWrite, ToolSource: domain.ToolSSA, Confidence: 0.9,
			Metadata: map[string]any{"call_line": 5}},
		{SourceID: domain.CanonicalID(funcID), TargetID: domain.CanonicalID(funcID + "#w2"),
			Kind: domain.FactIndirectWrite, ToolSource: domain.ToolSSA, Confidence: 0.9},
		// 干扰：其他 kind + 其他函数 source，均不应返回
		{SourceID: domain.CanonicalID(funcID), TargetID: domain.CanonicalID(funcID + "#w1"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(other), TargetID: domain.CanonicalID(funcID + "#w1"),
			Kind: domain.FactIndirectWrite, ToolSource: domain.ToolSSA, Confidence: 0.9},
	}
	save(t, r, nodes, edges)

	rows, err := r.GetIndirectWriteEdges(domain.CanonicalID(funcID))
	if err != nil {
		t.Fatalf("GetIndirectWriteEdges: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("edges = %d, want 2（其他 kind / 其他函数 source 应过滤）", len(rows))
	}
	if rows[0].Metadata["call_line"] != float64(5) {
		t.Errorf("metadata 未保留: %v", rows[0].Metadata)
	}
	for _, e := range rows {
		if e.Kind != domain.FactIndirectWrite {
			t.Errorf("kind = %s, want indirect_write", e.Kind)
		}
	}

	// 无边的函数 → 空（other 因干扰边存在 1 条，须查真正无边的函数）
	rows, err = r.GetIndirectWriteEdges("symbol:go:example.com/m:noEdges")
	if err != nil {
		t.Fatalf("GetIndirectWriteEdges(no edges): %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("no-edge function = %d, want 0", len(rows))
	}
}

// TestGetDispatchEdges：P0 补全——接口类型候选实现 dispatch_to 边
// （kind 过滤 + source 精确匹配）。
func TestGetDispatchEdges(t *testing.T) {
	r := newTestRepo(t)
	iface := "symbol:go:example.com/m:Greeter"
	impl1 := "symbol:go:example.com/m:(EN).Greet"
	impl2 := "symbol:go:example.com/m:(ZH).Greet"
	other := "symbol:go:example.com/m:main"
	nodes := []*domain.CodeEntity{
		node(iface, "interface", "Greeter", "a.go"),
		node(impl1, "method", "Greet", "en.go"),
		node(impl2, "method", "Greet", "zh.go"),
		node(other, "function", "main", "a.go"),
	}
	edges := []*domain.Fact{
		{SourceID: domain.CanonicalID(iface), TargetID: domain.CanonicalID(impl1),
			Kind: domain.FactDispatchTo, ToolSource: domain.ToolSSA, Confidence: 0.9,
			Metadata: map[string]any{"register": true}},
		{SourceID: domain.CanonicalID(iface), TargetID: domain.CanonicalID(impl2),
			Kind: domain.FactDispatchTo, ToolSource: domain.ToolSSA, Confidence: 0.7},
		// 干扰：其他 kind + 非接口 source
		{SourceID: domain.CanonicalID(other), TargetID: domain.CanonicalID(impl1),
			Kind: domain.FactDispatchTo, ToolSource: domain.ToolSSA, Confidence: 0.9},
		{SourceID: domain.CanonicalID(iface), TargetID: domain.CanonicalID(impl1),
			Kind: domain.FactCalls, ToolSource: domain.ToolCodeGraph, Confidence: 0.8},
	}
	save(t, r, nodes, edges)

	rows, err := r.GetDispatchEdges(domain.CanonicalID(iface))
	if err != nil {
		t.Fatalf("GetDispatchEdges: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("edges = %d, want 2（其他 kind / 其他 source 应过滤）", len(rows))
	}
	if rows[0].Metadata["register"] != true {
		t.Errorf("metadata 未保留: %v", rows[0].Metadata)
	}

	// 无候选的接口 → 空
	rows, err = r.GetDispatchEdges("symbol:go:example.com/m:NoImpl")
	if err != nil {
		t.Fatalf("GetDispatchEdges(no edges): %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("no-candidate interface = %d, want 0", len(rows))
	}
}

// TestAllSummaries：P0 补全——全量摘要导出（S4）：
// 返回全部行且按 field_path, access_kind 排序；空表返回空。
func TestAllSummaries(t *testing.T) {
	r := newTestRepo(t)
	funcID := "symbol:go:example.com/m:run"
	// function_field_summary.function_id 有 FK → nodes（INSERT OR IGNORE 会
	// 静默丢弃 FK 失败行），须先存函数节点再存摘要。
	save(t, r, []*domain.CodeEntity{node(funcID, "function", "run", "a.go")}, nil)
	summaries := []*domain.FunctionFieldSummary{
		{FunctionID: domain.CanonicalID(funcID), AccessKind: domain.SummaryDirectRead,
			FieldPath: "example.com/m.T.C", InstancePath: "t.C", LineStart: 9, CodeSnippet: "x := t.C"},
		{FunctionID: domain.CanonicalID(funcID), AccessKind: domain.SummaryDirectRead,
			FieldPath: "example.com/m.T.A", InstancePath: "t.A", LineStart: 3, CodeSnippet: "y := t.A"},
		{FunctionID: domain.CanonicalID(funcID), AccessKind: domain.SummaryDirectWrite,
			FieldPath: "example.com/m.T.A", InstancePath: "t.A", LineStart: 5, CodeSnippet: "t.A = 1"},
	}
	if _, err := r.SaveBatchStats(nil, nil, summaries); err != nil {
		t.Fatalf("save summaries: %v", err)
	}

	rows, err := r.AllSummaries()
	if err != nil {
		t.Fatalf("AllSummaries: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rows))
	}
	// 排序：field_path ASC（T.A < T.C），同字段 access_kind ASC（read < write）
	wantPaths := []string{"example.com/m.T.A", "example.com/m.T.A", "example.com/m.T.C"}
	wantKinds := []string{domain.SummaryDirectRead, domain.SummaryDirectWrite, domain.SummaryDirectRead}
	for i, row := range rows {
		if row.FieldPath != wantPaths[i] || row.AccessKind != wantKinds[i] {
			t.Fatalf("rows[%d] = %s/%s, want %s/%s", i, row.FieldPath, row.AccessKind, wantPaths[i], wantKinds[i])
		}
	}
	if rows[1].CodeSnippet != "t.A = 1" || rows[1].LineStart != 5 {
		t.Errorf("摘要行内容未完整回读: %+v", rows[1])
	}
}

// TestFindFieldReadsFilter：P0 补全——FindFieldReads 基本断言
// （TestFindFieldReadsOrder 只测了顺序）：只返回 read 方向 + full_path
// 精确匹配的 field_access；write / 其他路径不返回。
func TestFindFieldReadsFilter(t *testing.T) {
	r := newTestRepo(t)
	funcID := "symbol:go:example.com/m:run"
	fa := func(id, path, kind string) *domain.CodeEntity {
		return &domain.CodeEntity{
			ID: domain.CanonicalID(funcID + "#" + id), Kind: domain.KindFieldAccess,
			Name: "t.A", FilePath: "a.go", LineStart: 1,
			Properties: map[string]any{"func_id": funcID, "full_path": path, "access_kind": kind},
		}
	}
	nodes := []*domain.CodeEntity{
		fa("r1", "example.com/m.T.A", "read"),
		fa("w1", "example.com/m.T.A", "write"), // 同路径 write：不返回
		fa("r2", "example.com/m.T.B", "read"),  // 其他路径 read：不返回
	}
	save(t, r, nodes, nil)

	rows, err := r.FindFieldReads("example.com/m.T.A")
	if err != nil {
		t.Fatalf("FindFieldReads: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1（write 与异路径 read 应过滤）", len(rows))
	}
	if rows[0].ID != nodes[0].ID {
		t.Errorf("returned %s, want %s", rows[0].ID, nodes[0].ID)
	}

	// 无匹配 → 空
	rows, err = r.FindFieldReads("example.com/m.T.Missing")
	if err != nil {
		t.Fatalf("FindFieldReads(no match): %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("no-match = %d, want 0", len(rows))
	}
}
