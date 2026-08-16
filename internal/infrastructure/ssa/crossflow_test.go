package ssa

import (
	"strings"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

// findFact 按 (source, target, kind) 查找边。
func findFact(t *testing.T, facts []*domain.Fact, source, target, kind string) *domain.Fact {
	t.Helper()
	for _, f := range facts {
		if string(f.SourceID) == source && string(f.TargetID) == target && string(f.Kind) == kind {
			return f
		}
	}
	t.Fatalf("fact not found: %s -> %s [%s]", source, target, kind)
	return nil
}

// findFactByKindPrefix 按 kind 与 source ID 前缀查找边（SSA 临时名 tN 不稳定）。
func findFactByKindPrefix(facts []*domain.Fact, kind domain.FactKind, srcPrefix string) *domain.Fact {
	for _, f := range facts {
		if f.Kind == kind && strings.HasPrefix(string(f.SourceID), srcPrefix) {
			return f
		}
	}
	return nil
}

func TestArgumentReturnsEdges(t *testing.T) {
	nodes, facts, summaries := indexFixtureFull(t, map[string]string{
		"go.mod":  moduleGoMod,
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

	// argument 边：main 的局部 t（Alloc）→ use 的形参 t
	arg := findFactByKindPrefix(facts, domain.FactArgument, mainID+"#t")
	if arg == nil {
		t.Fatal("argument edge main#t -> use#t not found")
	}
	if string(arg.TargetID) != useID+"#t" {
		t.Errorf("argument target = %s, want %s#t", arg.TargetID, useID)
	}
	// 形参节点归属 use 函数
	param := nodeByID(t, nodes, useID+"#t")
	if param.Property("func_id") != useID {
		t.Errorf("param func_id = %q", param.Property("func_id"))
	}

	// returns 边：make 的返回值（make 命名空间的临时值）→ main 的调用结果
	ret := findFactByKindPrefix(facts, domain.FactReturns, makeID+"#t")
	if ret == nil {
		t.Fatal("returns edge from make not found")
	}
	result := nodeByID(t, nodes, string(ret.TargetID))
	if result.Property("func_id") != mainID {
		t.Errorf("returns target func_id = %q, want %s", result.Property("func_id"), mainID)
	}
	// make 的返回值也是 make 的写条目来源（T{A: v} → direct_write）
	findSummary(t, summaries, makeID, domain.SummaryDirectWrite, "example.com/mtest.T.A")
}

func TestPhiOperandEdges(t *testing.T) {
	_, facts, _ := indexFixtureFull(t, map[string]string{
		"go.mod":  moduleGoMod,
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
	// phi_operand：分支输入 a / b → Phi 节点
	a := findFactByKindPrefix(facts, domain.FactPhiOperand, phiID+"#a")
	b := findFactByKindPrefix(facts, domain.FactPhiOperand, phiID+"#b")
	if a == nil || b == nil {
		t.Fatalf("phi_operand edges missing: a=%v b=%v", a, b)
	}
	if a.TargetID != b.TargetID {
		t.Errorf("phi operands must share target, got %s vs %s", a.TargetID, b.TargetID)
	}
}

func TestSummaryDirectAndIndirect(t *testing.T) {
	_, facts, summaries := indexFixtureFull(t, map[string]string{
		"go.mod":  moduleGoMod,
		"main.go": `package m

type T struct {
	A int
	B string
}

func fill(t *T, v int) {
	t.A = v
	t.B = "x"
}

func inner(t *T) {
	fill(t, 2)
}

func outer(t *T) {
	inner(t)
}
`,
	})
	fillID := "symbol:go:example.com/mtest:fill"
	innerID := "symbol:go:example.com/mtest:inner"
	outerID := "symbol:go:example.com/mtest:outer"

	// fill：direct_write T.A / T.B
	findSummary(t, summaries, fillID, domain.SummaryDirectWrite, "example.com/mtest.T.A")
	findSummary(t, summaries, fillID, domain.SummaryDirectWrite, "example.com/mtest.T.B")
	// 间接写闭包：outer → inner → fill，类型匹配（*T 实参）传播
	findSummary(t, summaries, innerID, domain.SummaryIndirectWrite, "example.com/mtest.T.A")
	findSummary(t, summaries, innerID, domain.SummaryIndirectWrite, "example.com/mtest.T.B")
	findSummary(t, summaries, outerID, domain.SummaryIndirectWrite, "example.com/mtest.T.A")

	// INDIRECT_WRITE 边：inner → fill、outer → inner
	findFact(t, facts, innerID, fillID, string(domain.FactIndirectWrite))
	findFact(t, facts, outerID, innerID, string(domain.FactIndirectWrite))
}

func TestSummaryTypeMismatchNoIndirect(t *testing.T) {
	// 实参类型与被调函数写字段的声明类型不匹配 → 无间接写
	_, _, summaries := indexFixtureFull(t, map[string]string{
		"go.mod":  moduleGoMod,
		"main.go": `package m

type T struct {
	A int
}

type U struct {
	B int
}

func fillT(t *T) {
	t.A = 1
}

func call(u *U) {
	fillT(nil)
}
`,
	})
	callID := "symbol:go:example.com/mtest:call"
	for _, s := range summaries {
		if string(s.FunctionID) == callID && s.AccessKind == domain.SummaryIndirectWrite {
			t.Errorf("call must not have indirect writes, got %+v", s)
		}
	}
}

// findSummary 按（函数, access_kind, field_path）查找摘要行。
func findSummary(t *testing.T, summaries []*domain.FunctionFieldSummary,
	funcID, accessKind, fieldPath string) *domain.FunctionFieldSummary {
	t.Helper()
	for _, s := range summaries {
		if string(s.FunctionID) == funcID && s.AccessKind == accessKind && s.FieldPath == fieldPath {
			return s
		}
	}
	t.Fatalf("summary not found: %s %s %s", funcID, accessKind, fieldPath)
	return nil
}

// TestIndirectWriteExcludedDeepChain：S1 回归——跨函数参数 may 传播
// （a→b→c 三层）须稳定生效：c 写自己内部对象（与实参无别名）时，
// a 的调用点应被别名排除（无间接写）。此前 mayOfDepth 缓存 paramMay
// 引用，参数首次 nil→新建后缓存失效，传播可能过早停滞（结果依赖
// 调用点处理顺序，不稳定）。
func TestIndirectWriteExcludedDeepChain(t *testing.T) {
	_, _, summaries := indexFixtureFull(t, map[string]string{
		"go.mod":  moduleGoMod,
		"main.go": `package m

type T struct {
	A int
}

// c 写自己内部对象 inner（与实参 x 无别名）
func c(x *T) {
	var inner T
	inner.A = 1
	_ = x
}

func b(x *T) {
	c(x)
}

func a() {
	var t T
	b(&t)
}
`,
	})
	funcA := "symbol:go:example.com/mtest:a"
	for _, s := range summaries {
		if s.FunctionID == domain.CanonicalID(funcA) && s.AccessKind == domain.SummaryIndirectWrite {
			t.Errorf("a 不应有间接写（c 写内部对象，别名排除应生效）: %s", s.FieldPath)
		}
	}
}

// TestIndirectWriteNestedOwner：Q157——嵌套对象字段传播。实现写
// Order.FinalFee，wrapper 实参是 *OrderModel（含 Order 嵌套字段）——
// 类型匹配须沿嵌套字段展开 owner 链（OrderModel → Order），wrapper
// 的 indirect_write 才能含 Order.FinalFee（现状：只比较实参结构体
// OrderModel 与字段所属 Order，不匹配）。
func TestIndirectWriteNestedOwner(t *testing.T) {
	_, _, summaries := indexFixtureFull(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

type Order struct {
	FinalFee float64
}

type OrderModel struct {
	Order Order
	Name  string
}

type FeeCalculator interface {
	Calculate(o *OrderModel)
}

type StdCalc struct{}

// 实现写嵌套字段 o.Order.FinalFee——fieldPath 属主类型是 Order
func (c *StdCalc) Calculate(o *OrderModel) { o.Order.FinalFee = 100 }

// wrapper：实参 *OrderModel（含 Order 嵌套字段）——类型匹配须沿嵌套
// 字段展开 owner 链（OrderModel → Order）才命中实现写
func Process(fc FeeCalculator, m *OrderModel) {
	fc.Calculate(m)
}
`,
	})
	calcID := "symbol:go:example.com/mtest:(StdCalc).Calculate"
	procID := "symbol:go:example.com/mtest:Process"
	// 实现：direct_write（嵌套字段写，属主类型 Order）
	findSummary(t, summaries, calcID, domain.SummaryDirectWrite, "example.com/mtest.Order.FinalFee")
	// wrapper：经接口动态调用间接写——实参 *OrderModel 须展开匹配 Order
	findSummary(t, summaries, procID, domain.SummaryIndirectWrite, "example.com/mtest.Order.FinalFee")
}

// TestIndirectWriteCallLinePerCall：Q157——callLine/callArg 按调用点
// 粒度。同一函数两处调用 fill（不同行）写同一字段：INDIRECT_WRITE 边
// 各带自己的 call_line（现状：按字段去重后复用首次保存的调用点，
// 两条边都指向首处调用）。
func TestIndirectWriteCallLinePerCall(t *testing.T) {
	_, facts, _ := indexFixtureFull(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

type T struct {
	A int
}

func fill(t *T) {
	t.A = 1
}

func two() {
	fill(&T{})
	fill(&T{})
}
`,
	})
	twoID := "symbol:go:example.com/mtest:two"
	fillID := "symbol:go:example.com/mtest:fill"
	lines := map[int]bool{}
	for _, f := range facts {
		if string(f.SourceID) == twoID && string(f.TargetID) == fillID &&
			string(f.Kind) == string(domain.FactIndirectWrite) {
			if cl, ok := f.Metadata["call_line"].(float64); ok {
				lines[int(cl)] = true
			} else if cl, ok := f.Metadata["call_line"].(int); ok {
				lines[cl] = true
			}
		}
	}
	if len(lines) != 2 {
		t.Errorf("INDIRECT_WRITE 边应带两个不同调用点 call_line，got %v", lines)
	}
}
