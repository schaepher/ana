package ssa

import (
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

func TestArgumentReturnsEdges(t *testing.T) {
	nodes, facts, summaries := indexFixtureFull(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

type T struct {
	A int
}

func make(v int) T {
	return T{A: v}
}

func use(t *T) int {
	return t.A
}

func main() {
	t := make(5)
	_ = use(&t)
}
`,
	})
	mainID := "symbol:go:example.com/mtest:main"
	useID := "symbol:go:example.com/mtest:use"
	makeID := "symbol:go:example.com/mtest:make"

	arg := findFactByKindPrefix(facts, domain.FactArgument, mainID+"#t")
	if arg == nil {
		t.Fatal("argument edge main#t -> use#t not found")
	}
	if string(arg.TargetID) != useID+"#t" {
		t.Errorf("argument target = %s, want %s#t", arg.TargetID, useID)
	}

	param := nodeByID(t, nodes, useID+"#t")
	if param.Property("func_id") != useID {
		t.Errorf("param func_id = %q", param.Property("func_id"))
	}

	ret := findFactByKindPrefix(facts, domain.FactReturns, makeID+"#t")
	if ret == nil {
		t.Fatal("returns edge from make not found")
	}
	result := nodeByID(t, nodes, string(ret.TargetID))
	if result.Property("func_id") != mainID {
		t.Errorf("returns target func_id = %q, want %s", result.Property("func_id"), mainID)
	}

	findSummary(t, summaries, makeID, domain.SummaryDirectWrite, "example.com/mtest.T.A")
}
func TestPhiOperandEdges(t *testing.T) {
	_, facts, _ := indexFixtureFull(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

type T struct {
	A int
}

func phi(c bool, a, b *T) int {
	x := a
	if c {
		x = b
	}
	return x.A
}
`,
	})
	phiID := "symbol:go:example.com/mtest:phi"

	a := findFactByKindPrefix(facts, domain.FactPhiOperand, phiID+"#a")
	b := findFactByKindPrefix(facts, domain.FactPhiOperand, phiID+"#b")
	if a == nil || b == nil {
		t.Fatalf("phi_operand edges missing: a=%v b=%v", a, b)
	}
	if a.TargetID != b.TargetID {
		t.Errorf("phi operands must share target, got %s vs %s", a.TargetID, b.TargetID)
	}
}
