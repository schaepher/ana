package action

import (
	"strings"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
)

// Q235-5 query context：一次调用拿全链上下文（借鉴 GitNexus context
// 聚合查询——MCP 地基，transport 解耦）。复用现有查询编排，无新图
// 逻辑；子查询失败部分降级（字段 null 不整体失败）。

// contextFixture seedRepo + 补：caller 边、值流链（字段锚点）、
// 接口 dispatch 候选。
func contextFixture(t *testing.T) (*Actions, string) {
	t.Helper()
	acts, dir := seedRepo(t)
	// 写数据经 sqlite 直连（Reader 接口无写方法）
	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	r := sqlite.NewRepo(db)
	callerID := domain.CanonicalID("symbol:go:example.com/m:start")
	fieldID := domain.CanonicalID("symbol:go:example.com/m:main#ext.sql.table_a.id.read@6")
	v1 := domain.CanonicalID("symbol:go:example.com/m:main#v1")
	filterID := domain.CanonicalID("symbol:go:example.com/m:main#ext.sql.table_b.a_id.filter@9")
	ifaceID := domain.CanonicalID("symbol:go:example.com/m:Iface")
	implID := domain.CanonicalID("symbol:go:example.com/m:Impl")
	mainID := domain.CanonicalID("symbol:go:example.com/m:main")
	nodes := []*domain.CodeEntity{
		{ID: callerID, Kind: domain.KindFunction, Name: "start", FilePath: "start.go", LineStart: 1},
		{ID: fieldID, Kind: domain.KindFieldAccess, Name: "table_a.id", FilePath: "main.go", LineStart: 6,
			Properties: map[string]any{"full_path": "table_a.id", "access_kind": "read",
				"type_string": "sql", "is_external": "true", "func_id": string(mainID)}},
		{ID: v1, Kind: domain.KindSSAValue, Name: "v1", Properties: map[string]any{"func_id": string(mainID)}},
		{ID: filterID, Kind: domain.KindFieldAccess, Name: "table_b.a_id", FilePath: "main.go", LineStart: 9,
			Properties: map[string]any{"full_path": "table_b.a_id", "access_kind": "filter",
				"type_string": "sql", "is_external": "true", "func_id": string(mainID)}},
		{ID: ifaceID, Kind: domain.KindInterface, Name: "Iface", FilePath: "iface.go", LineStart: 2},
		{ID: implID, Kind: domain.KindFunction, Name: "Impl", FilePath: "impl.go", LineStart: 4},
	}
	edges := []*domain.Fact{
		{SourceID: callerID, TargetID: mainID, Kind: domain.FactCalls, ToolSource: domain.ToolCodeGraph, Confidence: 0.9},
		{SourceID: fieldID, TargetID: v1, Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: v1, TargetID: filterID, Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: ifaceID, TargetID: implID, Kind: domain.FactDispatchTo, ToolSource: domain.ToolSSA, Confidence: 0.8},
	}
	if _, err := r.SaveBatchStats(nodes, edges, nil); err != nil {
		t.Fatalf("save: %v", err)
	}
	return acts, dir
}

// TestContextComplete：函数锚点——symbol/callers/callees/fields 齐全，
// chain 存在（可空），非接口节点 dispatch 为空。
func TestContextComplete(t *testing.T) {
	acts, _ := contextFixture(t)
	ctx, err := acts.Context("symbol:go:example.com/m:main")
	if err != nil {
		t.Fatalf("Context: %v", err)
	}
	if ctx.Symbol == nil || ctx.Symbol.Kind != domain.KindFunction {
		t.Fatalf("symbol 应为主函数节点，got %+v", ctx.Symbol)
	}
	foundCaller, foundCallee := false, false
	for _, f := range ctx.Callers {
		if strings.Contains(string(f.SourceID), "start") {
			foundCaller = true
		}
	}
	for _, f := range ctx.Callees {
		if strings.Contains(string(f.TargetID), "helper") {
			foundCallee = true
		}
	}
	if !foundCaller || !foundCallee {
		t.Errorf("callers/callees 应含 start/helper，callers=%v callees=%v", ctx.Callers, ctx.Callees)
	}
	if ctx.Fields == nil || (len(ctx.Fields.DirectRead) == 0 && len(ctx.Fields.DirectWrite) == 0) {
		t.Errorf("fields 应含字段摘要（seedRepo 有 main 的读写），got %+v", ctx.Fields)
	}
	if ctx.Dispatch != nil {
		t.Errorf("非接口节点 dispatch 应为空，got %v", ctx.Dispatch)
	}
}

// TestContextChainFieldAnchor：字段锚点——chain 非空（值流主链）。
func TestContextChainFieldAnchor(t *testing.T) {
	acts, _ := contextFixture(t)
	ctx, err := acts.Context("symbol:go:example.com/m:main#ext.sql.table_a.id.read@6")
	if err != nil {
		t.Fatalf("Context: %v", err)
	}
	if len(ctx.Chain) == 0 {
		t.Errorf("字段锚点 chain 应非空（table_a.id → table_b.a_id 值流）")
	}
	if len(ctx.Traces) == 0 {
		t.Errorf("字段锚点 traces 应非空")
	}
}

// TestContextDispatch：接口节点——dispatch 候选非空。
func TestContextDispatch(t *testing.T) {
	acts, _ := contextFixture(t)
	ctx, err := acts.Context("symbol:go:example.com/m:Iface")
	if err != nil {
		t.Fatalf("Context: %v", err)
	}
	if len(ctx.Dispatch) == 0 {
		t.Errorf("接口节点 dispatch 应含候选实现，got %v", ctx.Dispatch)
	}
}

// TestContextUnknownSymbol：未知符号报错（主字段失败整体失败）。
func TestContextUnknownSymbol(t *testing.T) {
	acts, _ := seedRepo(t)
	if _, err := acts.Context("symbol:go:example.com/m:nope"); err == nil {
		t.Fatal("未知符号应报错")
	}
}
