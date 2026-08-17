package ssa

import (
	"strings"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

// findDispatch 查找 source → target 的 dispatch_to 边（metadata 断言用）。
func findDispatch(t *testing.T, facts []*domain.Fact, source, target string) *domain.Fact {
	t.Helper()
	for _, f := range facts {
		if f.Kind == domain.FactDispatchTo && string(f.SourceID) == source && string(f.TargetID) == target {
			return f
		}
	}
	t.Fatalf("dispatch_to 边缺失: %s -> %s", source, target)
	return nil
}

// TestDispatchEdgeCandidateMeta：Q161——动态 argument/returns 边附加
// 候选元数据（interface / candidate_origin / confidence），value-trace
// 据此区分必达/候选路径（注册点命中 register 0.9，枚举兜底 enum 0.7）。
func TestDispatchEdgeCandidateMeta(t *testing.T) {
	_, facts, _ := indexFixtureFull(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

type Order struct {
	FinalFee int
}

type FeeCalculator interface {
	Calculate(o *Order)
}

type StdCalc struct{}

func (c *StdCalc) Calculate(o *Order) { o.FinalFee = 100 }

type ExpCalc struct{}

func (c *ExpCalc) Calculate(o *Order) { o.FinalFee = 200 }

// wrapper：经接口调用分派（动态 invoke，无静态 callee）
func Process(fc FeeCalculator, o *Order) {
	fc.Calculate(o)
}

// 上游：静态调用 wrapper，&StdCalc{} 是注册点（MakeInterface）
func Run() {
	Process(&StdCalc{}, &Order{})
}
`,
	})
	var stdArg, expArg *domain.Fact
	for _, f := range facts {
		if f.Kind != domain.FactArgument || f.Metadata == nil {
			continue
		}
		if f.Metadata["candidate_origin"] == nil {
			continue
		}
		if strings.Contains(string(f.TargetID), "(StdCalc).Calculate") {
			stdArg = f
		}
		if strings.Contains(string(f.TargetID), "(ExpCalc).Calculate") {
			expArg = f
		}
	}
	if stdArg == nil || expArg == nil {
		t.Fatalf("动态 argument 边未带候选元数据: stdArg=%v expArg=%v", stdArg != nil, expArg != nil)
	}

	if stdArg.Metadata["candidate_origin"] != "register" {
		t.Errorf("StdCalc candidate_origin = %v, want register", stdArg.Metadata["candidate_origin"])
	}
	if c, ok := stdArg.Metadata["confidence"].(float64); !ok || c != 0.9 {
		t.Errorf("StdCalc confidence = %v, want 0.9", stdArg.Metadata["confidence"])
	}
	if stdArg.Metadata["interface"] == nil {
		t.Error("StdCalc 动态边缺 interface 元数据")
	}

	if expArg.Metadata["candidate_origin"] != "enum" {
		t.Errorf("ExpCalc candidate_origin = %v, want enum", expArg.Metadata["candidate_origin"])
	}
	if c, ok := expArg.Metadata["confidence"].(float64); !ok || c != 0.7 {
		t.Errorf("ExpCalc confidence = %v, want 0.7", expArg.Metadata["confidence"])
	}
}

// TestDispatchEdgeCandidateMetaGoDefer：Q161 场景树——Go/Defer 形态的
// 动态调用，argument 边同样携带候选元数据（emitCall 对 Go/Defer 共用
// 动态分支）。
func TestDispatchEdgeCandidateMetaGoDefer(t *testing.T) {
	_, facts, _ := indexFixtureFull(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

type Order struct {
	FinalFee int
}

type FeeCalculator interface {
	Calculate(o *Order)
}

type StdCalc struct{}

func (c *StdCalc) Calculate(o *Order) { o.FinalFee = 100 }

func Run() {
	var fc FeeCalculator = &StdCalc{}
	go fc.Calculate(&Order{})
	defer fc.Calculate(&Order{})
}
`,
	})

	count := 0
	for _, f := range facts {
		if f.Kind != domain.FactArgument || f.Metadata == nil {
			continue
		}
		if f.Metadata["candidate_origin"] != "register" {
			continue
		}
		if strings.Contains(string(f.TargetID), "(StdCalc).Calculate") {
			if c, ok := f.Metadata["confidence"].(float64); !ok || c != 0.9 {
				t.Errorf("Go/Defer 候选边 confidence = %v, want 0.9", f.Metadata["confidence"])
			}
			count++
		}
	}
	if count < 2 {
		t.Errorf("Go/Defer 动态 argument 候选边 = %d, want ≥2（go + defer 各一）", count)
	}
}

// TestDispatchOriginsMultiImpl：Q161 场景树——多候选实现写同一字段时，
// wrapper 的 indirect_write 摘要保留全部来源（每个候选实现一条 origin，
// 不再折叠为单行"置零"分支）。
func TestDispatchOriginsMultiImpl(t *testing.T) {
	_, _, _, origins := indexFixtureFullOrigins(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

type Order struct {
	FinalFee int
}

type FeeCalculator interface {
	Calculate(o *Order)
}

type StdCalc struct{}

func (c *StdCalc) Calculate(o *Order) { o.FinalFee = 100 }

type ExpCalc struct{}

func (c *ExpCalc) Calculate(o *Order) { o.FinalFee = 200 }

// wrapper：经接口调用分派（动态 invoke，无静态 callee）
func Process(fc FeeCalculator, o *Order) {
	fc.Calculate(o)
}

func Run() {
	Process(&StdCalc{}, &Order{})
}
`,
	})
	procID := domain.CanonicalID("symbol:go:example.com/mtest:Process")
	feePath := "example.com/mtest.Order.FinalFee"

	seen := map[string]bool{}
	for _, o := range origins {
		if o.FunctionID == procID && o.FieldPath == feePath {
			seen[string(o.CalleeID)] = true
		}
	}
	if !seen["symbol:go:example.com/mtest:(StdCalc).Calculate"] {
		t.Error("origins 缺 StdCalc 来源")
	}
	if !seen["symbol:go:example.com/mtest:(ExpCalc).Calculate"] {
		t.Error("origins 缺 ExpCalc 来源（多候选实现来源被折叠）")
	}
}

// TestDispatchOriginsMultiField：Q163 回归——被调函数写多个匹配字段
// 时，调用方 indirect_write 的 origins 每个字段都保留（此前 break 只
// 记第一个匹配字段，其余字段来源为空）。
func TestDispatchOriginsMultiField(t *testing.T) {
	_, _, _, origins := indexFixtureFullOrigins(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

type Invoice struct {
	Fee        int
	SettledFee int
	Tax        int
}

// fill 写三个字段
func fill(invoice *Invoice) {
	invoice.Fee = 1
	invoice.SettledFee = 2
	invoice.Tax = 3
}

func process(invoice *Invoice) {
	fill(invoice)
}

func Run() {
	process(&Invoice{})
}
`,
	})
	procID := domain.CanonicalID("symbol:go:example.com/mtest:process")

	fields := map[string]bool{}
	for _, o := range origins {
		if o.FunctionID == procID {
			fields[o.FieldPath] = true
		}
	}
	for _, want := range []string{
		"example.com/mtest.Invoice.Fee",
		"example.com/mtest.Invoice.SettledFee",
		"example.com/mtest.Invoice.Tax",
	} {
		if !fields[want] {
			t.Errorf("process 的 origins 缺字段 %s（多字段只记首个）", want)
		}
	}
}
