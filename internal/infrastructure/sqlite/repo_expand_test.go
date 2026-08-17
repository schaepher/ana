package sqlite

import (
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

func TestExpand(t *testing.T) {
	r := newTestRepo(t)
	a := node("symbol:go:example.com/m:a", "function", "a", "a.go")
	b := node("symbol:go:example.com/m:b", "function", "b", "b.go")
	c := node("symbol:go:example.com/m:c", "function", "c", "c.go")
	save(t, r, []*domain.CodeEntity{a, b, c}, nil)
	edges := []*domain.Fact{
		{SourceID: a.ID, TargetID: b.ID, Kind: domain.FactCalls, Confidence: 0.8, Metadata: map[string]any{"line_num": 3}},
		{SourceID: c.ID, TargetID: a.ID, Kind: domain.FactCalls, Confidence: 0.8},
		{SourceID: a.ID, TargetID: c.ID, Kind: domain.FactPassesTo, Confidence: 0.8},
	}
	save(t, r, nil, edges)

	facts, neighbors, err := r.Expand(a.ID)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if len(facts) != 3 {
		t.Errorf("facts = %+v, want 3", facts)
	}
	if len(neighbors) != 2 {
		t.Errorf("neighbors = %+v, want 2 (dedup)", neighbors)
	}

	ids := map[domain.CanonicalID]bool{}
	for _, n := range neighbors {
		if n.ID == a.ID {
			t.Error("neighbors must not include self")
		}
		ids[n.ID] = true
	}
	if !ids[b.ID] || !ids[c.ID] {
		t.Error("neighbors should include b and c")
	}

	facts, neighbors, err = r.Expand(node("x", "function", "x", "x.go").ID)
	if err != nil || len(facts) != 0 {
		t.Errorf("expand missing node = %+v, %v", facts, err)
	}

	save(t, r, nil, []*domain.Fact{{SourceID: a.ID, TargetID: b.ID, Kind: "not_a_kind", Confidence: 0.7}})
	facts, _, _ = r.Expand(a.ID)
	for _, f := range facts {
		if f.Kind == "not_a_kind" {
			t.Error("Expand must not return unknown-kind edges")
		}
	}
}
func TestExpandParamResultDefinitionOrder(t *testing.T) {
	r := newTestRepo(t)
	funcID := "symbol:go:example.com/m:f"
	fn := node(funcID, "function", "f", "f.go")
	mk := func(id, kind, name string, index int) *domain.CodeEntity {
		n := node(id, kind, name, "f.go")
		n.Properties["index"] = index
		return n
	}
	recv := mk(funcID+"#param.recv.s", "parameter", "s", -1)
	p0 := mk(funcID+"#param.a", "parameter", "a", 0)
	p1 := mk(funcID+"#param.b", "parameter", "b", 1)
	r0 := mk(funcID+"#result.0", "result", "int", 0)
	r1 := mk(funcID+"#result.1", "result", "error", 1)
	callee := node("symbol:go:example.com/m:g", "function", "g", "g.go")
	save(t, r, []*domain.CodeEntity{fn, recv, p0, p1, r0, r1, callee}, nil)

	edges := []*domain.Fact{
		{SourceID: fn.ID, TargetID: r1.ID, Kind: domain.FactHasResult, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: fn.ID, TargetID: p1.ID, Kind: domain.FactHasParam, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: fn.ID, TargetID: r0.ID, Kind: domain.FactHasResult, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: fn.ID, TargetID: p0.ID, Kind: domain.FactHasParam, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: fn.ID, TargetID: callee.ID, Kind: domain.FactCalls, Confidence: 1},
		{SourceID: fn.ID, TargetID: recv.ID, Kind: domain.FactHasParam, ToolSource: domain.ToolSSA, Confidence: 1},
	}
	save(t, r, nil, edges)

	facts, _, err := r.Expand(fn.ID)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}

	want := []string{string(recv.ID), string(p0.ID), string(p1.ID), string(r0.ID), string(r1.ID), string(callee.ID)}
	got := make([]string, 0, len(facts))
	for _, f := range facts {
		got = append(got, string(f.TargetID))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("expand order[%d] = %s, want %s (full: %v)", i, got[i], want[i], got)
		}
	}
}
func TestExpandParameterDataFlow(t *testing.T) {
	r := newTestRepo(t)
	funcID := "symbol:go:example.com/m:f"
	callerID := "symbol:go:example.com/m:g"
	fn := node(funcID, "function", "f", "f.go")
	caller := node(callerID, "function", "g", "g.go")

	param := mkParamNode(funcID+"#param.a", "a", 0, funcID)
	paramVal := node(funcID+"#a", "ssa_value", "a", "f.go")
	paramVal.Properties["func_id"] = funcID

	fa := faNodeAccess(funcID+"#a.X.read@3", funcID, "example.com/m.T.X", "a.X", 3, "read")

	argVal := node(callerID+"#t0", "ssa_value", "t0", "g.go")
	argVal.Properties["func_id"] = callerID
	save(t, r, []*domain.CodeEntity{fn, caller, param, paramVal, fa, argVal}, []*domain.Fact{
		{SourceID: fn.ID, TargetID: param.ID, Kind: domain.FactHasParam, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: paramVal.ID, TargetID: fa.ID, Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: argVal.ID, TargetID: paramVal.ID, Kind: domain.FactArgument, ToolSource: domain.ToolSSA, Confidence: 1},
	})

	facts, neighbors, err := r.Expand(param.ID)
	if err != nil {
		t.Fatalf("Expand param: %v", err)
	}
	kinds := map[string]bool{}
	for _, f := range facts {
		kinds[string(f.Kind)] = true
	}
	if !kinds[string(domain.FactDataFlowsTo)] || !kinds[string(domain.FactArgument)] {
		t.Errorf("expand param facts kinds = %v, want data_flows_to+argument", kinds)
	}

	bridged := false
	for _, f := range facts {
		if f.SourceID == param.ID && f.TargetID == paramVal.ID {
			bridged = true
		}
	}
	if !bridged {
		t.Errorf("bridge edge param->value missing: %+v", facts)
	}

	nid := map[string]bool{}
	for _, n := range neighbors {
		nid[string(n.ID)] = true
	}
	for _, want := range []string{string(fa.ID), string(argVal.ID), string(paramVal.ID)} {
		if !nid[want] {
			t.Errorf("expand param neighbors missing %s (have %v)", want, nid)
		}
	}
}
func TestExpandSSAValueParamBridgesFunction(t *testing.T) {
	r := newTestRepo(t)
	funcID := "symbol:go:example.com/m:f"
	fn := node(funcID, "function", "f", "f.go")

	recvVal := node(funcID+"#m", "ssa_value", "m", "f.go")
	recvVal.Properties["origin_kind"] = "receiver"
	recvVal.Properties["func_id"] = funcID
	fa := faNodeAccess(funcID+"#m.cfg.write@3", funcID, "example.com/m.T.cfg", "m.cfg", 3, "write")
	save(t, r, []*domain.CodeEntity{fn, recvVal, fa},
		[]*domain.Fact{{SourceID: recvVal.ID, TargetID: fa.ID, Kind: domain.FactDataFlowsTo,
			ToolSource: domain.ToolSSA, Confidence: 1}})

	facts, neighbors, err := r.Expand(recvVal.ID)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}

	bridged := false
	for _, f := range facts {
		if f.SourceID == fn.ID && f.TargetID == recvVal.ID && f.Kind == domain.FactHasParam {
			bridged = true
		}
	}
	if !bridged {
		t.Errorf("func bridge edge missing: %+v", facts)
	}

	nid := map[string]bool{}
	for _, n := range neighbors {
		nid[string(n.ID)] = true
	}
	if !nid[string(fn.ID)] || !nid[string(fa.ID)] {
		t.Errorf("neighbors = %v, want fn + fa", nid)
	}

	callVal := node(funcID+"#t5", "ssa_value", "t5", "f.go")
	callVal.Properties["origin_kind"] = "call_result"
	callVal.Properties["func_id"] = funcID
	save(t, r, []*domain.CodeEntity{callVal}, nil)
	facts, _, err = r.Expand(callVal.ID)
	if err != nil {
		t.Fatalf("Expand callVal: %v", err)
	}
	for _, f := range facts {
		if f.Kind == domain.FactHasParam {
			t.Errorf("non-param ssa_value must not bridge function: %+v", facts)
		}
	}
}
