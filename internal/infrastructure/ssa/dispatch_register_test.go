package ssa

import (
	"strings"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

// TestDispatchRegisterPoint：注册点（MakeInterface）命中 → dispatch_to 边
// origin=register、confidence=0.9、register_site 记录注册位置（Q91/Q93/Q94）。
func TestDispatchRegisterPoint(t *testing.T) {
	_, facts, _ := indexFixtureFull(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

type Greeter interface {
	Hello() string
}

type Eng struct{}

func (e *Eng) Hello() string { return "hi" }

func use(g Greeter) string {
	return g.Hello()
}

func main() {
	use(&Eng{})
}
`,
	})

	ifaceID := "symbol:go:example.com/mtest:Greeter"

	edge := findDispatch(t, facts, ifaceID, "symbol:go:example.com/mtest:(Eng).Hello")
	if origin, _ := edge.Metadata["origin"].(string); origin != "register" {
		t.Errorf("dispatch origin = %v, want register", edge.Metadata["origin"])
	}
	if conf, ok := edge.Metadata["confidence"].(float64); !ok || conf != 0.9 {
		t.Errorf("dispatch confidence = %v, want 0.9", edge.Metadata["confidence"])
	}
	if method, _ := edge.Metadata["interface_method"].(string); method != "Hello" {
		t.Errorf("dispatch interface_method = %v, want Hello", edge.Metadata["interface_method"])
	}
	// 内存 facts 的 metadata 为 int；DB 反序列化为 float64
	var site int
	switch v := edge.Metadata["register_site"].(type) {
	case float64:
		site = int(v)
	case int:
		site = v
	}
	if site != 16 {
		t.Errorf("dispatch register_site = %v, want 16（main 里 use(&Eng{}) 的行）", edge.Metadata["register_site"])
	}
}

// TestDispatchEnumFallback：无注册点的实现者 → 枚举兜底 origin=enum、
// confidence=0.7（Q93）。
func TestDispatchEnumFallback(t *testing.T) {
	_, facts, _ := indexFixtureFull(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

type Greeter interface {
	Hello() string
}

type Eng struct{}

func (e *Eng) Hello() string { return "hi" }

func use(g Greeter) string {
	return g.Hello()
}

func main() {
	use(nil)
}
`,
	})
	ifaceID := "symbol:go:example.com/mtest:Greeter"
	edge := findDispatch(t, facts, ifaceID, "symbol:go:example.com/mtest:(Eng).Hello")
	if origin, _ := edge.Metadata["origin"].(string); origin != "enum" {
		t.Errorf("dispatch origin = %v, want enum", edge.Metadata["origin"])
	}
	if conf, ok := edge.Metadata["confidence"].(float64); !ok || conf != 0.7 {
		t.Errorf("dispatch confidence = %v, want 0.7", edge.Metadata["confidence"])
	}
}

// TestDispatchMissingInfo：无法确定动态类型（函数值/外部接口）→ 缺失
// 信息类别（Q93：dynamic_call_unresolved），无 dispatch_to 边或带缺失标记。
func TestDispatchMissingInfo(t *testing.T) {
	_, facts, _ := indexFixtureFull(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

type Greeter interface {
	Hello() string
}

type Eng struct{}

func (e *Eng) Hello() string { return "hi" }

func use(g Greeter) string {
	return g.Hello()
}

func main() {
	use(&Eng{})
}
`,
	})

	for _, f := range facts {
		if f.Kind == domain.FactDispatchTo {
			if missing, _ := f.Metadata["missing"].(string); missing != "" {
				t.Errorf("注册点场景不应有 missing 标记: %v", f.Metadata)
			}
		}
	}
}

// TestDispatchToEdgeCount：Eng 只有一个实现方法 → 恰好一条 dispatch_to 边
// （无重复发射）。
func TestDispatchToEdgeCount(t *testing.T) {
	_, facts, _ := indexFixtureFull(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

type Greeter interface {
	Hello() string
}

type Eng struct{}

func (e *Eng) Hello() string { return "hi" }

func use(g Greeter) string {
	return g.Hello()
}

func main() {
	use(&Eng{})
}
`,
	})
	count := 0
	for _, f := range facts {
		if f.Kind == domain.FactDispatchTo {
			count++
		}
	}
	if count != 1 {
		t.Errorf("dispatch_to 边数 = %d, want 1", count)
	}
	_ = strings.TrimSpace
}

// TestInterfaceDispatchIndirectWrite：Q154——接口动态分派候选实现内的
// 字段写须回传为 wrapper 与上游调用方的 indirect_write。此前动态分支只
// 建 argument/returns 边、未追加 funcData.calls，间接写闭包（summary.go:29
// 消费 fd.calls）无消费入口——实现对 Order.FinalFee 的写断在接口调用点。
func TestInterfaceDispatchIndirectWrite(t *testing.T) {
	_, facts, summaries := indexFixtureFull(t, map[string]string{
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

// 上游：静态调用 wrapper，闭包传播应覆盖到
func Run() {
	Process(&StdCalc{}, &Order{})
}
`,
	})
	stdID := "symbol:go:example.com/mtest:(StdCalc).Calculate"
	expFuncID := "symbol:go:example.com/mtest:(ExpCalc).Calculate"
	procID := "symbol:go:example.com/mtest:Process"
	runID := "symbol:go:example.com/mtest:Run"
	feePath := "example.com/mtest.Order.FinalFee"

	findSummary(t, summaries, stdID, domain.SummaryDirectWrite, feePath)
	findSummary(t, summaries, expFuncID, domain.SummaryDirectWrite, feePath)

	findSummary(t, summaries, procID, domain.SummaryIndirectWrite, feePath)

	findSummary(t, summaries, runID, domain.SummaryIndirectWrite, feePath)

	findFact(t, facts, procID, stdID, string(domain.FactIndirectWrite))
	findFact(t, facts, procID, expFuncID, string(domain.FactIndirectWrite))

	findFact(t, facts, runID, procID, string(domain.FactIndirectWrite))
}

// TestDispatchValueReceiverAndSelfExclusion：⑬ 猎 bug——值接收者实现
// （候选集含 (Eng).Hello）且接口自身不进入候选集（self 排除）。
func TestDispatchValueReceiverAndSelfExclusion(t *testing.T) {
	_, facts, _ := indexFixtureFull(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

type Greeter interface {
	Hello() string
}

// 值接收者实现（pointer method set 也覆盖）
type Eng struct{}

func (e Eng) Hello() string { return "hi" }

// 接口 self 排除：接口类型自身不得作为候选
type Greeter2 interface {
	Hello() string
}

func use(g Greeter) string {
	return g.Hello()
}

func main() {
	use(&Eng{})
}
`,
	})
	ifaceID := "symbol:go:example.com/mtest:Greeter"
	edge := findDispatch(t, facts, ifaceID, "symbol:go:example.com/mtest:(Eng).Hello")
	if edge == nil {
		t.Fatalf("值接收者实现应入候选集")
	}

	for _, f := range facts {
		if f.Kind == domain.FactDispatchTo && string(f.SourceID) == string(f.TargetID) {
			t.Errorf("dispatch_to 自环不应存在: %s", f.SourceID)
		}
	}
}
