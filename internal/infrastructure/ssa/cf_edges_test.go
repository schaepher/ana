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
	// Q178：argument 边 target 是签名参数节点（#param.t），与
	// emitSignatureNodes 一致——value-trace 可经参数节点回连调用点实参
	if string(arg.TargetID) != useID+"#param.t" {
		t.Errorf("argument target = %s, want %s#param.t", arg.TargetID, useID)
	}

	param := nodeByID(t, nodes, useID+"#param.t")
	if param.Property("func_id") != useID {
		t.Errorf("param func_id = %q", param.Property("func_id"))
	}

	// Q235-7：make 返回值节点从 #t 变 #*mtest.T（匿名 T{A:v} 分配类型
	// 短名 + 行号消歧）——更可读，前缀匹配（@行号不参与）
	ret := findFactByKindPrefix(facts, domain.FactReturns, makeID+"#*mtest.T")
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

	// Q178：operand 是参数 → 源是签名参数节点 #param.a / #param.b
	a := findFactByKindPrefix(facts, domain.FactPhiOperand, phiID+"#param.a")
	b := findFactByKindPrefix(facts, domain.FactPhiOperand, phiID+"#param.b")
	if a == nil || b == nil {
		t.Fatalf("phi_operand edges missing: a=%v b=%v", a, b)
	}
	if a.TargetID != b.TargetID {
		t.Errorf("phi operands must share target, got %s vs %s", a.TargetID, b.TargetID)
	}
}
