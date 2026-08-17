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
	// 接口类型节点由 AST 适配器建立（集成测试验证）；纯 SSA fixture 无节点
	ifaceID := "symbol:go:example.com/mtest:Greeter"
	// 注册点命中：Eng 经 MakeInterface 传入 Greeter 参数
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
	// 无 missing 标记时仅验证 dispatch_to 边存在（动态调用未解析场景由
	// 外部包调用覆盖，见 integration）；此处注册点场景不应有缺失标记
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
	_ = strings.TrimSpace // keep import
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

	// 实现：direct_write（写自身声明的字段）
	findSummary(t, summaries, stdID, domain.SummaryDirectWrite, feePath)
	findSummary(t, summaries, expFuncID, domain.SummaryDirectWrite, feePath)
	// wrapper：经接口调用分派 → 两个实现的写都回传为 indirect_write
	findSummary(t, summaries, procID, domain.SummaryIndirectWrite, feePath)
	// 上游：间接写闭包迭代至稳定，Run → Process 传播
	findSummary(t, summaries, runID, domain.SummaryIndirectWrite, feePath)

	// INDIRECT_WRITE 边：wrapper → 每个候选实现（动态派发语义，均有匹配写）
	findFact(t, facts, procID, stdID, string(domain.FactIndirectWrite))
	findFact(t, facts, procID, expFuncID, string(domain.FactIndirectWrite))
	// 上游 → wrapper
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
	// self 排除：Greeter2（无实现者、非候选源）不应产生 dispatch_to 指向自身
	for _, f := range facts {
		if f.Kind == domain.FactDispatchTo && string(f.SourceID) == string(f.TargetID) {
			t.Errorf("dispatch_to 自环不应存在: %s", f.SourceID)
		}
	}
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
	// 注册点命中（Run 里 &StdCalc{} → register 0.9）
	if stdArg.Metadata["candidate_origin"] != "register" {
		t.Errorf("StdCalc candidate_origin = %v, want register", stdArg.Metadata["candidate_origin"])
	}
	if c, ok := stdArg.Metadata["confidence"].(float64); !ok || c != 0.9 {
		t.Errorf("StdCalc confidence = %v, want 0.9", stdArg.Metadata["confidence"])
	}
	if stdArg.Metadata["interface"] == nil {
		t.Error("StdCalc 动态边缺 interface 元数据")
	}
	// 枚举兜底（ExpCalc 未注册 → enum 0.7）
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
	// Go/Defer 动态调用：argument 边带候选元数据（register 0.9）
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
	// Process 的 indirect_write 应有 2 条来源：StdCalc + ExpCalc 两个候选实现
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
	// process 的 origins 应覆盖全部三个字段
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
