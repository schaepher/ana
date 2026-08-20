package ast

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

// TestSetChangedFilesSkipsUnchanged：P1-1——SetChangedFiles 后只分析变更
// 文件（增量更新 AST 文件级跳过，§20.3 唯一真实加速点）；未设置时全量。
func TestSetChangedFilesSkipsUnchanged(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), fixtureGoMod)

	writeFile(t, filepath.Join(dir, "a.go"), "package m\n\nfunc A() { B() }\n")
	writeFile(t, filepath.Join(dir, "b.go"), "package m\n\nfunc B() { C() }\n")
	writeFile(t, filepath.Join(dir, "c.go"), "package m\n\nfunc C() {}\n")

	load := func(withChanged []string) ([]*domain.CodeEntity, []*domain.Fact) {
		pkgs, err := astLoadTestPackages(dir)
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		var nodes []*domain.CodeEntity
		var facts []*domain.Fact
		adapter := &Adapter{}
		adapter.SetChangedFiles(withChanged)
		repo := &domain.Repository{Path: dir, Module: "example.com/mtest", Modules: []string{"example.com/mtest"}}
		if err := adapter.Index(context.Background(), repo, pkgs, func(item domain.Item) error {
			if item.Node != nil {
				nodes = append(nodes, item.Node)
			}
			if item.Fact != nil {
				facts = append(facts, item.Fact)
			}
			return nil
		}); err != nil {
			t.Fatalf("Index: %v", err)
		}
		return nodes, facts
	}

	_, facts := load(nil)
	findFact(t, facts, "symbol:go:example.com/mtest:A", "symbol:go:example.com/mtest:B", string(domain.FactCalls))
	findFact(t, facts, "symbol:go:example.com/mtest:B", "symbol:go:example.com/mtest:C", string(domain.FactCalls))

	_, facts2 := load([]string{"a.go"})
	findFact(t, facts2, "symbol:go:example.com/mtest:A", "symbol:go:example.com/mtest:B", string(domain.FactCalls))
	if hasFact(facts2, "symbol:go:example.com/mtest:B", "symbol:go:example.com/mtest:C", string(domain.FactCalls)) {
		t.Fatalf("B→C 调用点在 b.go，SetChangedFiles([a.go]) 后不应分析")
	}
}
func TestIndexCallsAndImports(t *testing.T) {
	nodes, facts := indexFixture(t, map[string]string{
		"go.mod":    fixtureGoMod,
		"main.go":   "package main\n\nimport \"example.com/mtest/svc\"\n\nfunc main() {\n\ts := &svc.Service{}\n\ts.Handle()\n}\n",
		"svc/s.go":  "package svc\n\ntype Service struct{}\n\nfunc (s *Service) Handle() {}\n",
		"util/u.go": "package util\n\nfunc Helper() {}\n",
	})

	findFact(t, facts,
		"symbol:go:example.com/mtest:main",
		"symbol:go:example.com/mtest/svc:(Service).Handle",
		"calls")

	findFact(t, facts,
		"symbol:go:example.com/mtest:mtest",
		"symbol:go:example.com/mtest/svc:svc",
		"imports")

	if n := findNode(t, nodes, "symbol:go:example.com/mtest/svc:(Service).Handle"); n.Kind != domain.KindMethod {
		t.Errorf("Handle kind = %s", n.Kind)
	}

	if hasFact(facts, "symbol:go:example.com/mtest:mtest", "symbol:go:example.com/mtest/util:util", "imports") {
		t.Error("util should not be imported")
	}
}
func TestIndexNestedArgPassesResult(t *testing.T) {

	_, facts := indexFixture(t, map[string]string{
		"go.mod":  fixtureGoMod,
		"main.go": "package main\n\nfunc C() int { return 0 }\nfunc B(x int) int { return x }\nfunc A(x int) {}\n\nfunc caller() {\n\tA(B(C()))\n}\n",
	})

	findFact(t, facts,
		"symbol:go:example.com/mtest:A",
		"symbol:go:example.com/mtest:B",
		"passes_result")
	findFact(t, facts,
		"symbol:go:example.com/mtest:B",
		"symbol:go:example.com/mtest:C",
		"passes_result")
	if hasFact(facts, "symbol:go:example.com/mtest:A", "symbol:go:example.com/mtest:B", "calls") {
		t.Error("A should not have direct calls edge to B (arg position)")
	}

	findFact(t, facts,
		"symbol:go:example.com/mtest:caller",
		"symbol:go:example.com/mtest:A",
		"calls")
}
func TestIndexFunctionAsArgPassesTo(t *testing.T) {

	_, facts := indexFixture(t, map[string]string{
		"go.mod":  fixtureGoMod,
		"main.go": "package main\n\nfunc foo(fn func()) { fn() }\n\nfunc bar() {}\n\nfunc caller() {\n\tfoo(bar)\n}\n",
	})
	findFact(t, facts,
		"symbol:go:example.com/mtest:foo",
		"symbol:go:example.com/mtest:bar",
		"passes_to")

	if hasFact(facts, "symbol:go:example.com/mtest:foo", "symbol:go:example.com/mtest:bar", "calls") {
		t.Error("foo should not calls bar (bar is arg)")
	}
}
func TestIndexExternalCalleeAsArg(t *testing.T) {

	_, facts := indexFixture(t, map[string]string{
		"go.mod":  fixtureGoMod,
		"main.go": "package main\n\nimport \"net/http\"\n\nfunc handler(w http.ResponseWriter, r *http.Request) {}\n\nfunc setup() {\n\thttp.HandleFunc(\"/x\", handler)\n}\n",
	})

	findFact(t, facts,
		"symbol:go:example.com/mtest:setup",
		"symbol:go:net/http:HandleFunc",
		"calls")

	findFact(t, facts,
		"symbol:go:net/http:HandleFunc",
		"symbol:go:example.com/mtest:handler",
		"passes_to")

	_, facts2 := indexFixture(t, map[string]string{
		"go.mod":  fixtureGoMod,
		"main.go": "package main\n\nimport \"fmt\"\n\nfunc logIt() {\n\tfmt.Println(\"hello\")\n}\n",
	})
	if hasFact(facts2, "symbol:go:example.com/mtest:logIt", "symbol:go:fmt:Println", "calls") {
		t.Error("plain external call must not create calls edge")
	}
}
func TestIndexInitializesAndUses(t *testing.T) {

	_, facts := indexFixture(t, map[string]string{
		"go.mod":   fixtureGoMod,
		"main.go":  "package main\n\nimport \"example.com/mtest/svc\"\n\nfunc main() {\n\ts := &svc.Service{}\n\ts.Handle()\n}\n",
		"svc/s.go": "package svc\n\ntype Service struct{}\n\nfunc (s *Service) Handle() {}\n",
	})
	findFact(t, facts,
		"symbol:go:example.com/mtest:main",
		"symbol:go:example.com/mtest/svc:Service",
		"initializes")

	findFact(t, facts,
		"symbol:go:example.com/mtest/svc:Service",
		"symbol:go:example.com/mtest/svc:(Service).Handle",
		"uses")
}
func TestIndexHasMethod(t *testing.T) {

	_, facts := indexFixture(t, map[string]string{
		"go.mod":   fixtureGoMod,
		"main.go":  "package main\n\nfunc main() {}\n",
		"svc/s.go": "package svc\n\ntype Service struct{}\n\nfunc (s *Service) Handle() {}\n\nfunc (s *Service) helper() {}\n",
	})
	findFact(t, facts,
		"symbol:go:example.com/mtest/svc:Service",
		"symbol:go:example.com/mtest/svc:(Service).Handle",
		"has_method")
	findFact(t, facts,
		"symbol:go:example.com/mtest/svc:Service",
		"symbol:go:example.com/mtest/svc:(Service).helper",
		"has_method")
}
func TestIndexServiceFlags(t *testing.T) {

	nodes, _ := indexFixture(t, map[string]string{
		"go.mod":  fixtureGoMod,
		"main.go": "package main\n\nimport \"net/http\"\n\nfunc serve() {\n\thttp.ListenAndServe(\":8080\", nil)\n}\n",
	})
	n := findNode(t, nodes, "symbol:go:example.com/mtest:serve")
	if n.Properties["serves_http"] != "true" {
		t.Errorf("serve should carry serves_http flag, got %+v", n.Properties)
	}
}
func TestIndexStructFields(t *testing.T) {

	nodes, _ := indexFixture(t, map[string]string{
		"go.mod":   fixtureGoMod,
		"main.go":  "package main\n\nfunc main() {}\n",
		"svc/s.go": "package svc\n\ntype Service struct {\n\tName string\n\tAddr *netAddr\n}\n\ntype netAddr struct{}\n",
	})
	n := findNode(t, nodes, "symbol:go:example.com/mtest/svc:Service")
	fields, ok := n.Properties["fields"].([]map[string]any)
	if !ok || len(fields) != 2 {
		t.Fatalf("fields = %+v", n.Properties["fields"])
	}
	f0 := fields[0]
	if f0["name"] != "Name" || f0["type"] != "string" {
		t.Errorf("field0 = %+v", f0)
	}
}
func TestIndexInterfaceMethodChaining(t *testing.T) {

	_, facts := indexFixture(t, map[string]string{
		"go.mod":   fixtureGoMod,
		"main.go":  "package main\n\nimport \"example.com/mtest/svc\"\n\nfunc main() {\n\ts := svc.New()\n\ts.Handle()\n}\n",
		"svc/s.go": "package svc\n\ntype Service struct{}\n\nfunc (s *Service) Handle() {}\n\nfunc New() *Service {\n\treturn &Service{}\n}\n",
	})

	findFact(t, facts,
		"symbol:go:example.com/mtest:main",
		"symbol:go:example.com/mtest/svc:(Service).Handle",
		"calls")
}
func TestIndexUnrelatedPackagesNotIncluded(t *testing.T) {

	_, facts := indexFixture(t, map[string]string{
		"go.mod":   fixtureGoMod,
		"main.go":  "package main\n\nimport \"example.com/mtest/svc\"\n\nfunc main() {\n\tsvc.Helper()\n}\n",
		"svc/s.go": "package svc\n\nfunc Helper() {}\n",
	})
	for _, f := range facts {
		if !strings.HasPrefix(string(f.SourceID), "symbol:go:example.com/") {
			t.Errorf("fact escapes module: %+v", f)
		}
	}
}
