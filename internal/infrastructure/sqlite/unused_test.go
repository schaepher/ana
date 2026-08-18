package sqlite

import (
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

// 未调用函数与孤立链查询（field_trace.md §16）。

func unusedNode(id, name, kind string, exported bool) *domain.CodeEntity {
	n := &domain.CodeEntity{
		ID:   domain.CanonicalID(id),
		Kind: domain.EntityKind(kind),
		Name: name,
	}
	if exported {
		n.Properties = map[string]any{"exported": "true"}
	}
	return n
}

// TestGetUncalledFunctions：两档判定——无调用（calls/passes_result 入边）
// 与无任何引用（+passes_to/dispatch_to/initializes/var 初始化）。
func TestGetUncalledFunctions(t *testing.T) {
	r := newTestRepo(t)
	// 图：
	//   dead()         ← 无任何边 → 无调用 + 无引用
	//   calleeOnly()   ← 被 dead 调用 → 有调用
	//   reg()          ← 被 passes_to 引用（回调注册）→ 无调用但有引用
	//   impl()         ← 被 dispatch_to 引用（接口实现）→ 无调用但有引用
	//   ctor()         ← 被 initializes 引用（&T{} 实例化）→ 无调用但有引用
	//   initRef()      ← 被 data_flows_to 引用（var 初始化）→ 无调用但有引用
	nodes := []*domain.CodeEntity{
		unusedNode("symbol:go:example.com/m:dead", "dead", "function", false),
		unusedNode("symbol:go:example.com/m:calleeOnly", "calleeOnly", "function", false),
		unusedNode("symbol:go:example.com/m:reg", "reg", "function", false),
		unusedNode("symbol:go:example.com/m:impl", "impl", "method", false),
		unusedNode("symbol:go:example.com/m:ctor", "ctor", "function", false),
		unusedNode("symbol:go:example.com/m:initRef", "initRef", "function", false),
		unusedNode("symbol:go:example.com/m:main", "main", "function", false),
	}
	edges := []*domain.Fact{
		{SourceID: "symbol:go:example.com/m:dead", TargetID: "symbol:go:example.com/m:calleeOnly", Kind: domain.FactCalls, ToolSource: domain.ToolCodeGraph, Confidence: 0.8},
		{SourceID: "symbol:go:example.com/m:main", TargetID: "symbol:go:example.com/m:reg", Kind: domain.FactPassesTo, ToolSource: domain.ToolCodeGraph, Confidence: 0.8},
		{SourceID: "symbol:go:example.com/m:main", TargetID: "symbol:go:example.com/m:impl", Kind: domain.FactDispatchTo, ToolSource: domain.ToolSSA, Confidence: 0.7},
		{SourceID: "symbol:go:example.com/m:main", TargetID: "symbol:go:example.com/m:ctor", Kind: domain.FactInitializes, ToolSource: domain.ToolCodeGraph, Confidence: 0.8},
		{SourceID: "symbol:go:example.com/m:initRef#t0", TargetID: "symbol:go:example.com/m:var.G", Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1.0},
	}
	// initRef 的 var 初始化引用：data_flows_to 边 source 的 func_id=initRef
	// （target var.G 节点须存在——外键约束，否则边被静默跳过）
	nodes = append(nodes,
		&domain.CodeEntity{
			ID:   "symbol:go:example.com/m:initRef#t0",
			Kind: domain.KindSSAValue,
			Name: "t0",
			Properties: map[string]any{
				"origin_kind": "call_result",
				"func_id":     "symbol:go:example.com/m:initRef",
			},
		},
		&domain.CodeEntity{
			ID:         "symbol:go:example.com/m:var.G",
			Kind:       domain.KindSSAValue,
			Name:       "G",
			Properties: map[string]any{"origin_kind": "global"},
		})
	save(t, r, nodes, edges)

	rows, err := r.GetUncalledFunctions()
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]*domain.UnusedFunc{}
	for _, u := range rows {
		byName[u.Name] = u
	}
	// dead：无调用且无引用
	if u := byName["dead"]; u == nil || u.Called || u.Referenced {
		t.Errorf("dead: got %+v, want called=false referenced=false", u)
	}
	// calleeOnly：有调用（被 dead 调用）
	if u := byName["calleeOnly"]; u == nil || !u.Called {
		t.Errorf("calleeOnly: got %+v, want called=true", u)
	}
	// reg：无调用但有引用（passes_to）
	if u := byName["reg"]; u == nil || u.Called || !u.Referenced {
		t.Errorf("reg: got %+v, want called=false referenced=true", u)
	}
	// impl：无调用但有引用（dispatch_to）
	if u := byName["impl"]; u == nil || u.Called || !u.Referenced {
		t.Errorf("impl: got %+v, want called=false referenced=true", u)
	}
	// ctor：无调用但有引用（initializes）
	if u := byName["ctor"]; u == nil || u.Called || !u.Referenced {
		t.Errorf("ctor: got %+v, want called=false referenced=true", u)
	}
	// initRef：无调用但有引用（var 初始化 data_flows_to）
	if u := byName["initRef"]; u == nil || u.Called || !u.Referenced {
		t.Errorf("initRef: got %+v, want called=false referenced=true", u)
	}
	// main 永不报告
	if _, ok := byName["main"]; ok {
		t.Errorf("main 不应报告为未调用: %+v", byName["main"])
	}
}

// TestGetIsolatedChains：孤立链——链头无 caller，链内 caller ⊆ 链；
// 有链外 caller 断开；互调环整环孤立；单节点链。
func TestGetIsolatedChains(t *testing.T) {
	r := newTestRepo(t)
	// 图：
	//   a() → b() → c()          a 无 caller → 链 [a→b→c]
	//   x() → b()                另有一条边也到 b？——换：x() → y()，z()（有 caller main）→ y()
	//   环：p() → q() → p()
	//   单节点：solo()
	//   main → z() → y()
	// 预期链：
	//   [a→b→c]（a 无 caller；b 的 caller={a}⊆链；c 的 caller={b}⊆链）
	//   [x]（x 无 caller；x→y 但 y 有链外 caller z → 断开，y 不入链）
	//   [p→q]（环：p 无 caller；q 的 caller={p}⊆链；p 的 caller={q}⊆链）
	//   [solo]（单节点无 caller 无 callee）
	nodes := []*domain.CodeEntity{
		unusedNode("symbol:go:example.com/m:a", "a", "function", false),
		unusedNode("symbol:go:example.com/m:b", "b", "function", false),
		unusedNode("symbol:go:example.com/m:c", "c", "function", false),
		unusedNode("symbol:go:example.com/m:x", "x", "function", false),
		unusedNode("symbol:go:example.com/m:y", "y", "function", false),
		unusedNode("symbol:go:example.com/m:z", "z", "function", false),
		unusedNode("symbol:go:example.com/m:p", "p", "function", false),
		unusedNode("symbol:go:example.com/m:q", "q", "function", false),
		unusedNode("symbol:go:example.com/m:solo", "solo", "function", false),
		unusedNode("symbol:go:example.com/m:main", "main", "function", false),
	}
	edges := []*domain.Fact{
		{SourceID: "symbol:go:example.com/m:a", TargetID: "symbol:go:example.com/m:b", Kind: domain.FactCalls, ToolSource: domain.ToolCodeGraph, Confidence: 0.8},
		{SourceID: "symbol:go:example.com/m:b", TargetID: "symbol:go:example.com/m:c", Kind: domain.FactCalls, ToolSource: domain.ToolCodeGraph, Confidence: 0.8},
		{SourceID: "symbol:go:example.com/m:x", TargetID: "symbol:go:example.com/m:y", Kind: domain.FactCalls, ToolSource: domain.ToolCodeGraph, Confidence: 0.8},
		{SourceID: "symbol:go:example.com/m:z", TargetID: "symbol:go:example.com/m:y", Kind: domain.FactCalls, ToolSource: domain.ToolCodeGraph, Confidence: 0.8},
		{SourceID: "symbol:go:example.com/m:p", TargetID: "symbol:go:example.com/m:q", Kind: domain.FactCalls, ToolSource: domain.ToolCodeGraph, Confidence: 0.8},
		{SourceID: "symbol:go:example.com/m:q", TargetID: "symbol:go:example.com/m:p", Kind: domain.FactCalls, ToolSource: domain.ToolCodeGraph, Confidence: 0.8},
		{SourceID: "symbol:go:example.com/m:main", TargetID: "symbol:go:example.com/m:z", Kind: domain.FactCalls, ToolSource: domain.ToolCodeGraph, Confidence: 0.8},
	}
	save(t, r, nodes, edges)

	chains, err := r.GetIsolatedChains()
	if err != nil {
		t.Fatal(err)
	}
	chainNames := func(ch []*domain.UnusedFunc) string {
		s := ""
		for _, u := range ch {
			s += u.Name + "→"
		}
		return s
	}
	got := map[string]bool{}
	for _, ch := range chains {
		got[chainNames(ch)] = true
	}
	for _, want := range []string{"a→b→c→", "x→", "p→q→", "solo→"} {
		if !got[want] {
			t.Errorf("缺孤立链 %q，实际: %v", want, got)
		}
	}
	// y/z 不得入链（y 有链外 caller z；z 被 main 调用）
	if got["x→y→"] || got["y→"] || got["z→"] {
		t.Errorf("y/z 不应入孤立链: %v", got)
	}
	// main 不得入链
	for _, ch := range chains {
		for _, u := range ch {
			if u.Name == "main" {
				t.Errorf("main 不应在孤立链中: %v", ch)
			}
		}
	}
}
