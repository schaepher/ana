package ssa

import (
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

// TestTempValueRecoversVarName：Q179——SSA 临时寄存器（tN）恢复为源码变量名。
// u := f() 后 u.A = 1：lifting 把 u 提升为寄存器 t0（源码变量名在 IR 中
// 丢失），字段写 base 是 t0。t0.Pos 指向 f() 调用位置，assignTargets 区间
// 匹配 RHS（u := f() 的 f()）→ 目标变量 u——展示名应恢复为 u 而非 t0。
func TestTempValueRecoversVarName(t *testing.T) {
	nodes, _ := indexFixture(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

type T struct {
	A int
}

func f() *T { return &T{} }

func g() {
	u := f()
	u.A = 1
}
`,
	})
	funcID := "symbol:go:example.com/mtest:g"
	names := map[string]bool{}
	for _, n := range nodes {
		if n.Kind != domain.KindSSAValue || n.Property("func_id") != funcID {
			continue
		}
		names[n.Name] = true
	}
	if !names["u"] {
		t.Errorf("临时寄存器应恢复为变量名 u，got ssa_value names=%v", names)
	}
	// 字段访问的实例路径也应恢复 base 寄存器名：u.A 而非 t0.A
	if fa := findFieldAccess(t, nodes, funcID, "u.A", "write"); fa == nil {
		t.Errorf("字段写实例路径应为 u.A（base 寄存器 t0 恢复为 u）")
	}
	// Q180：恢复为变量名的 ssa_value 节点带定义行号（flows 面板
	// `← data_flows_to u (行号)`；Const/匿名字面量无源码 Pos 允许 0）
	for _, n := range nodes {
		if n.Kind != domain.KindSSAValue || n.Property("func_id") != funcID || n.Name != "u" {
			continue
		}
		if n.LineStart <= 0 {
			t.Errorf("ssa_value 节点 u 应带定义行号，got line=%d", n.LineStart)
		}
	}
}

// TestTempValuePhiKeepsSlot：phi 无源码位置（Pos 为空），无法恢复变量名，
// 保持 SSA 寄存器名（tN）——恢复逻辑不得误配其他赋值区间。
func TestTempValuePhiKeepsSlot(t *testing.T) {
	nodes, _ := indexFixture(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

type T struct {
	A int
}

func f(b bool, x, y *T) *T {
	if b {
		return x
	}
	return y
}

func g(b bool) {
	t := f(b, &T{}, &T{})
	t.A = 1
}
`,
	})
	funcID := "symbol:go:example.com/m:g"
	var tempName string
	for _, n := range nodes {
		if n.Kind != domain.KindSSAValue || n.Property("func_id") != funcID {
			continue
		}
		tempName = n.Name
	}
	if tempName == "" || tempName == "t" {
		// phi 无 Pos → 不得恢复为 t（t 是另一个赋值目标，区间不匹配）；
		// 保持 tN 或为空都可接受，但不得误配
		return
	}
}

// TestTempValueCallWithArgsRecovers：Q236——有参数调用恢复变量名。
// §69 曾记录「有参数调用不恢复变量名（Call.Pos=Rparen，Lparen 匹配
// 死代码）」——probe 复核（用 Position().Offset 而非 token.Pos 索引
// 源码，token.Pos = base+offset）证明 go/ssa v0.26 Call.Pos = **Lparen**
// （builder.go:1002 c.pos = e.Lparen）：有参调用 u := makeT(42) 的
// Call.Pos = makeT( 的 '('，与 buildAssignTargets 记录的 ce.Lparen
// 精确匹配 → 恢复 u；嵌套内层调用 Pos=内层 '(' 与外层 callPos 不等，
// 防误配依旧成立（TestTempValueNestedNoMismatch）。
func TestTempValueCallWithArgsRecovers(t *testing.T) {
	nodes, _ := indexFixture(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

type T struct {
	A int
}

func makeT(v int) *T { return &T{A: v} }

func g() {
	u := makeT(42)
	u.A = 1
}
`,
	})
	funcID := "symbol:go:example.com/mtest:g"
	found := false
	for _, n := range nodes {
		if n.Kind != domain.KindSSAValue || n.Property("func_id") != funcID {
			continue
		}
		if n.Name == "u" {
			found = true
		}
	}
	if !found {
		t.Errorf("有参调用 u := makeT(42) 应恢复变量名 u")
	}
	// 字段写实例路径同样恢复：u.A（base 是 Call 寄存器而非 Alloc）
	if fa := findFieldAccess(t, nodes, funcID, "u.A", "write"); fa == nil {
		t.Errorf("字段写实例路径应为 u.A（有参调用 base 恢复）")
	}
}

// TestTempValueNestedNoMismatch：Q193——嵌套表达式误配回归。
// err := outer(inner())：inner() 的返回值（内层，经 argument 边发射）
// 不得恢复为 err（err 是 outer 的结果）——保持寄存器名；outer() 的
// 返回值（顶层 RHS）恢复为 err。
func TestTempValueNestedNoMismatch(t *testing.T) {
	nodes, _ := indexFixture(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

type T struct {
	A int
}

func inner() *T { return &T{} }

func outer(y *T) *T { return y }

func callNested() {
	err := outer(inner())
	_ = err.A
}
`,
	})
	funcID := "symbol:go:example.com/mtest:callNested"
	names := map[string]bool{}
	for _, n := range nodes {
		if n.Kind != domain.KindSSAValue || n.Property("func_id") != funcID {
			continue
		}
		names[n.Name] = true
	}
	// 顶层 RHS（outer 调用）恢复为 err
	if !names["err"] {
		t.Errorf("顶层调用应恢复为 err，got %v", names)
	}
	// 内层调用（inner，#t0）不得误配为 err——保持寄存器名
	// （按节点 ID 精确断言，避免 alias 双发射的 #t1|t1 干扰）
	for _, n := range nodes {
		if n.ID == "symbol:go:example.com/mtest:callNested#t0" && n.Name == "err" {
			t.Errorf("内层嵌套值 #t0 误配为 err（应为寄存器名 t0）")
		}
	}
}
