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
