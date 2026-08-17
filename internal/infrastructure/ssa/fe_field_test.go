package ssa

import (
	"strings"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

func TestFieldReadWrite(t *testing.T) {
	nodes, facts := indexFixture(t, map[string]string{
		"go.mod":	moduleGoMod,
		"main.go": `package m

type T struct {
	A int
	B string
}

func f(x *T, v int) int {
	x.A = v
	return x.A
}
`,
	})
	funcID := "symbol:go:example.com/mtest:f"

	w := findFieldAccess(t, nodes, funcID, "x.A", "write")
	if w.Property("full_path") != "example.com/mtest.T.A" {
		t.Errorf("write full_path = %q", w.Property("full_path"))
	}
	if w.FilePath != "main.go" {
		t.Errorf("write file = %q", w.FilePath)
	}
	if !strings.Contains(w.Property("code_snippet"), "x.A = v") {
		t.Errorf("write snippet = %q", w.Property("code_snippet"))
	}

	findFieldAccess(t, nodes, funcID, "x.A", "read")

	r := findFieldAccess(t, nodes, funcID, "x.A", "read")
	base := findSSAValue(t, nodes, funcID, "x")
	val := findSSAValue(t, nodes, funcID, "v")

	baseEdges := factsFrom(facts, string(base.ID))
	if len(baseEdges) != 2 {
		t.Errorf("base edges = %+v, want 2 (write+read)", baseEdges)
	}
	targets := map[string]bool{}
	for _, f := range baseEdges {
		if f.Kind != domain.FactDataFlowsTo {
			t.Errorf("base edge kind = %s", f.Kind)
		}
		targets[string(f.TargetID)] = true
	}
	if !targets[string(w.ID)] || !targets[string(r.ID)] {
		t.Errorf("base must reach write and read nodes, got %v", targets)
	}
	if f := factsFrom(facts, string(val.ID)); len(f) != 1 || string(f[0].TargetID) != string(w.ID) {
		t.Errorf("val->write edges = %+v", f)
	}
	out := factsFrom(facts, string(r.ID))
	if len(out) != 1 || out[0].Kind != domain.FactDataFlowsTo {
		t.Fatalf("read->result edges = %+v", out)
	}
	target := nodeByID(t, nodes, string(out[0].TargetID))
	if target.Kind != domain.KindSSAValue {
		t.Errorf("read edge target kind = %s, want ssa_value", target.Kind)
	}
	if target.Property("func_id") != funcID {
		t.Errorf("result func_id = %q", target.Property("func_id"))
	}
}
func TestFieldCompoundReadWrite(t *testing.T) {

	nodes, _ := indexFixture(t, map[string]string{
		"go.mod":	moduleGoMod,
		"main.go": `package m

type T struct {
	A int
}

func g(x *T) {
	x.A = x.A + 1
}
`,
	})
	funcID := "symbol:go:example.com/mtest:g"
	read := findFieldAccess(t, nodes, funcID, "x.A", "read")
	write := findFieldAccess(t, nodes, funcID, "x.A", "write")
	if read.ID == write.ID {
		t.Errorf("read/write nodes must be distinct, both = %s", read.ID)
	}
	if read.LineStart != write.LineStart {
		t.Errorf("read line %d != write line %d", read.LineStart, write.LineStart)
	}
}
func TestFieldNested(t *testing.T) {

	nodes, _ := indexFixture(t, map[string]string{
		"go.mod":	moduleGoMod,
		"main.go": `package m

type Inner struct {
	V int
}

type Outer struct {
	In Inner
}

func n(o *Outer) {
	o.In.V = 7
}
`,
	})
	funcID := "symbol:go:example.com/mtest:n"
	first := findFieldAccess(t, nodes, funcID, "o.In", "write")
	if first.Property("full_path") != "example.com/mtest.Outer.In" {
		t.Errorf("first full_path = %q", first.Property("full_path"))
	}
	second := findFieldAccess(t, nodes, funcID, "o.In.V", "write")
	if second.Property("full_path") != "example.com/mtest.Inner.V" {
		t.Errorf("second full_path = %q", second.Property("full_path"))
	}
}
func TestFieldEmbedded(t *testing.T) {

	nodes, _ := indexFixture(t, map[string]string{
		"go.mod":	moduleGoMod,
		"main.go": `package m

type Emb struct {
	V int
}

type O2 struct {
	Emb
}

func e(o *O2) {
	o.V = 1
}
`,
	})
	funcID := "symbol:go:example.com/mtest:e"
	inner := findFieldAccess(t, nodes, funcID, "o.Emb.V", "write")
	if inner.Property("full_path") != "example.com/mtest.Emb.V" {
		t.Errorf("embedded full_path = %q", inner.Property("full_path"))
	}
	findFieldAccess(t, nodes, funcID, "o.Emb", "write")
}
func TestFieldGlobal(t *testing.T) {

	nodes, _ := indexFixture(t, map[string]string{
		"go.mod":	moduleGoMod,
		"main.go": `package m

type T struct {
	A int
}

var G T

func h() {
	G.A = 5
}
`,
	})
	funcID := "symbol:go:example.com/mtest:h"
	w := findFieldAccess(t, nodes, funcID, "G.A", "write")
	if w.Property("full_path") != "example.com/mtest.T.A" {
		t.Errorf("global full_path = %q", w.Property("full_path"))
	}

	g := nodeByID(t, nodes, "symbol:go:example.com/mtest:var.G")
	if g.Property("origin_kind") != "global" {
		t.Errorf("global origin_kind = %q", g.Property("origin_kind"))
	}
}
