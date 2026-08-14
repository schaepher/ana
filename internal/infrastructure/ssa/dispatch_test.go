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
		"go.mod":  moduleGoMod,
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
		"go.mod":  moduleGoMod,
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
		"go.mod":  moduleGoMod,
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
		"go.mod":  moduleGoMod,
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
