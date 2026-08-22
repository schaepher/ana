package ssa

import (
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

// TestTempValueAnonymousAllocTypeName：Q235-6——匿名对象分配（&T{} /
// make）无源码变量名（go/ssa 的 Alloc.Pos 指向复合字面量 '{' 或 make
// 关键字，非变量 Ident），tN 回退为类型短名（保留末段包名与 * / []
// 形态：*example.com/mtest.Inner → *mtest.Inner、[]int → []int）——
// value-trace 展示可读。make 分配不得误恢复为 make 关键字（idents
// 命中的是预声明标识符）。
func TestTempValueAnonymousAllocTypeName(t *testing.T) {
	nodes, _ := indexFixture(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

type Inner struct {
	Value string
}

type Outer struct {
	Inner Inner
}

func f() *Outer {
	return &Outer{Inner: Inner{Value: "x"}}
}

func g(v string) {
	r := &Inner{Value: v}
	_ = r
	s := make([]int, 3)
	_ = s
}
`,
	})
	// g：匿名 &Inner{} 分配 → 类型短名（保留包名 mtest）
	gID := "symbol:go:example.com/mtest:g"
	got := map[string]bool{}
	for _, n := range nodes {
		if n.Kind != domain.KindSSAValue || n.Property("func_id") != gID {
			continue
		}
		got[n.Name] = true
	}
	if !got["*mtest.Inner"] {
		t.Errorf("匿名 &Inner{} 分配应显示类型短名 *mtest.Inner（保留末段包名与 * 形态），got names=%v", got)
	}
	if got["make"] {
		t.Errorf("make 分配不得误恢复为 make 关键字，got names=%v", got)
	}
	// f：匿名 &Outer{} 分配 → mtest.Outer
	fID := "symbol:go:example.com/mtest:f"
	gotF := map[string]bool{}
	for _, n := range nodes {
		if n.Kind != domain.KindSSAValue || n.Property("func_id") != fID {
			continue
		}
		gotF[n.Name] = true
	}
	if !gotF["*mtest.Outer"] {
		t.Errorf("匿名 &Outer{} 分配应显示 *mtest.Outer（保留 * 形态），got names=%v", gotF)
	}
}

// TestTempValueAllocKeepsVarName：取址变量声明（Pos 指向变量 Ident）
// 仍恢复源码变量名——类型短名回退不覆盖变量名恢复（arr 等）。
func TestTempValueAllocKeepsVarName(t *testing.T) {
	nodes, _ := indexFixture(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

type T struct{ A int }

func h() {
	var arr T
	arr.A = 1
	_ = arr.A
}
`,
	})
	funcID := "symbol:go:example.com/mtest:h"
	names := map[string]bool{}
	for _, n := range nodes {
		if n.Kind != domain.KindSSAValue || n.Property("func_id") != funcID {
			continue
		}
		names[n.Name] = true
	}
	if !names["arr"] {
		t.Errorf("取址变量声明应恢复变量名 arr，got names=%v", names)
	}
}
