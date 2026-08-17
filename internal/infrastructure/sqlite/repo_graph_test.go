package sqlite

import (
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

func TestCallersCalleesDepthAndConfidence(t *testing.T) {
	r := newTestRepo(t)

	nodes := []*domain.CodeEntity{
		node("symbol:go:example.com/m:a", "function", "a", "a.go"),
		node("symbol:go:example.com/m:b", "function", "b", "b.go"),
		node("symbol:go:example.com/m:c", "function", "c", "c.go"),
	}
	save(t, r, nodes, nil)
	low := &domain.Fact{SourceID: "symbol:go:example.com/m:a", TargetID: "symbol:go:example.com/m:b", Kind: domain.FactCalls, Confidence: 0.5}
	high := &domain.Fact{SourceID: "symbol:go:example.com/m:b", TargetID: "symbol:go:example.com/m:c", Kind: domain.FactCalls, Confidence: 0.9}
	save(t, r, nil, []*domain.Fact{low, high})

	callees, err := r.GetCallees("symbol:go:example.com/m:a", 2, 0.8)
	if err != nil {
		t.Fatalf("GetCallees: %v", err)
	}
	if len(callees) != 0 {
		t.Errorf("callees with min 0.8 = %+v, want none (a->b has 0.5)", callees)
	}

	callees, _ = r.GetCallees("symbol:go:example.com/m:a", 1, 0.1)
	if len(callees) != 1 {
		t.Errorf("depth1 callees = %+v", callees)
	}
	callees, _ = r.GetCallees("symbol:go:example.com/m:a", 2, 0.1)
	if len(callees) != 2 {
		t.Errorf("depth2 callees = %+v, want 2", callees)
	}

	callers, err := r.GetCallers("symbol:go:example.com/m:c", 2, 0.1)
	if err != nil {
		t.Fatalf("GetCallers: %v", err)
	}
	if len(callers) != 2 {
		t.Errorf("callers of c = %+v, want 2", callers)
	}
}
func TestGetImpact(t *testing.T) {
	r := newTestRepo(t)

	nodes := []*domain.CodeEntity{
		node("symbol:go:example.com/m:a", "function", "a", "a.go"),
		node("symbol:go:example.com/m:b", "function", "b", "b.go"),
		node("symbol:go:example.com/m:c", "function", "c", "c.go"),
		node("symbol:go:example.com/m:d", "package", "d", "d.go"),
	}
	save(t, r, nodes, nil)
	edges := []*domain.Fact{
		{SourceID: "symbol:go:example.com/m:a", TargetID: "symbol:go:example.com/m:b", Kind: domain.FactCalls, Confidence: 1},
		{SourceID: "symbol:go:example.com/m:b", TargetID: "symbol:go:example.com/m:c", Kind: domain.FactCalls, Confidence: 1},
		{SourceID: "symbol:go:example.com/m:a", TargetID: "symbol:go:example.com/m:d", Kind: domain.FactImports, Confidence: 1},
	}
	save(t, r, nil, edges)

	impact, err := r.GetImpact("symbol:go:example.com/m:b", 2)
	if err != nil {
		t.Fatalf("GetImpact: %v", err)
	}
	ids := map[domain.CanonicalID]bool{}
	for _, n := range impact {
		ids[n.ID] = true
	}
	if !ids["symbol:go:example.com/m:a"] || !ids["symbol:go:example.com/m:c"] {
		t.Errorf("impact of b = %v, want a and c", ids)
	}
}
func TestGetRoots(t *testing.T) {
	r := newTestRepo(t)
	nodes := []*domain.CodeEntity{
		node("symbol:go:example.com/m:main", "function", "main", "cmd/main.go"),

		node("symbol:go:example.com/m.test:main", "function", "main", "main_test.go"),

		func() *domain.CodeEntity {
			n := node("symbol:go:example.com/m/server:serve", "function", "serve", "server/server.go")
			n.Properties["serves_http"] = "true"
			return n
		}(),

		func() *domain.CodeEntity {
			n := node("symbol:go:example.com/m/grpc:serve", "function", "serve", "grpc/grpc.go")
			n.Properties["serves_grpc"] = "true"
			return n
		}(),

		node("symbol:go:example.com/other:main", "function", "main", "../other/main.go"),

		node("symbol:go:example.com/m/t:helper", "function", "helper", "t/helper_test.go"),

		node("symbol:go:example.com/m/util:helper", "function", "helper", "util/helper.go"),
	}
	save(t, r, nodes, nil)

	roots, err := r.GetRoots()
	if err != nil {
		t.Fatalf("GetRoots: %v", err)
	}
	ids := map[domain.CanonicalID]bool{}
	for _, n := range roots {
		ids[n.ID] = true
	}
	if !ids["symbol:go:example.com/m:main"] {
		t.Error("roots missing main")
	}
	if !ids["symbol:go:example.com/m/server:serve"] || !ids["symbol:go:example.com/m/grpc:serve"] {
		t.Error("roots missing http/grpc entries")
	}
	if ids["symbol:go:example.com/m.test:main"] {
		t.Error("test main must be excluded")
	}
	if ids["symbol:go:example.com/other:main"] || ids["symbol:go:example.com/m/t:helper"] {
		t.Error("out-of-module / _test.go files must be excluded")
	}
	if ids["symbol:go:example.com/m/util:helper"] {
		t.Error("plain function must not be a root")
	}
}
func TestGetFrameworkStructs(t *testing.T) {
	r := newTestRepo(t)

	s := node("symbol:go:example.com/m:srv:S", "struct", "S", "srv/s.go")

	t2 := node("symbol:go:example.com/m:svc:T", "struct", "T", "svc/t.go")
	mHandle := node("symbol:go:example.com/m:srv:(S).Handle", "method", "(S).Handle", "srv/s.go")
	mUse := node("symbol:go:example.com/m:svc:(T).Use", "method", "(T).Use", "svc/t.go")
	caller := node("symbol:go:example.com/m:main", "function", "main", "main.go")
	save(t, r, []*domain.CodeEntity{s, t2, mHandle, mUse, caller}, nil)

	save(t, r, nil, []*domain.Fact{{
		SourceID: caller.ID, TargetID: mUse.ID, Kind: domain.FactCalls, Confidence: 0.8,
	}})

	structs, err := r.GetFrameworkStructs()
	if err != nil {
		t.Fatalf("GetFrameworkStructs: %v", err)
	}
	seen := map[domain.CanonicalID]bool{}
	for _, n := range structs {
		seen[n.ID] = true
	}
	if !seen[s.ID] {
		t.Error("S (methods not cross-file called) should be framework struct")
	}
	if seen[t2.ID] {
		t.Error("T (method called from other file) must not be framework struct")
	}
	if n := structs[0]; n.Properties["framework"] != "true" {
		t.Errorf("framework property missing: %+v", n.Properties)
	}
}
