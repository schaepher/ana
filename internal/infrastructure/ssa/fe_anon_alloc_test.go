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

// TestFieldPathBaseTypeName：Q235-7——字段路径基址 tN → 类型短名。
// 匿名 &Inner{} 分配（无变量名）的字段访问实例路径应显示
// *mtest.Inner.Value（基址类型短名）而非 tN.Value——用户视角 tN
// 不可读；变量名恢复（var arr T）不受影响。
func TestFieldPathBaseTypeName(t *testing.T) {
	nodes, _ := indexFixture(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

type Inner struct {
	Value string
}

func g(v string) {
	r := &Inner{Value: v}
	_ = r.Value
}

func h() {
	var arr Inner
	arr.Value = "x"
	_ = arr.Value
}
`,
	})
	// g：匿名基址 → 类型短名路径
	gID := "symbol:go:example.com/mtest:g"
	got := map[string]bool{}
	for _, n := range nodes {
		if n.Kind != domain.KindFieldAccess || n.Property("func_id") != gID {
			continue
		}
		got[n.Name] = true
	}
	if !got["*mtest.Inner.Value"] {
		t.Errorf("匿名基址字段路径应显示 *mtest.Inner.Value（基址类型短名），got names=%v", got)
	}
	// h：变量名恢复优先——arr.Value 不变
	hID := "symbol:go:example.com/mtest:h"
	gotH := map[string]bool{}
	for _, n := range nodes {
		if n.Kind != domain.KindFieldAccess || n.Property("func_id") != hID {
			continue
		}
		gotH[n.Name] = true
	}
	if !gotH["arr.Value"] {
		t.Errorf("变量名恢复字段路径应保持 arr.Value，got names=%v", gotH)
	}
}

// TestPhiRecoversVarName：Q235-9——go2o 形态的匿名 phi（短声明多值 +
// 循环更新：size, lastId := 5, 0 在 for 中更新——go/ssa lifting 后
// phi 不保留变量名）——phi 的 Pos 指向源码声明位置，idents 直接反查
// 恢复 size/lastId。
func TestPhiRecoversVarName(t *testing.T) {
	nodes, _ := indexFixture(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

func loop() {
	size, lastId := 5, 0
	for {
		if size > 10 {
			break
		}
		size = size + 1
		lastId = lastId + 1
	}
	_ = size + lastId
}
`,
	})
	funcID := "symbol:go:example.com/mtest:loop"
	names := map[string]bool{}
	for _, n := range nodes {
		if n.Kind != domain.KindSSAValue || n.Property("func_id") != funcID {
			continue
		}
		names[n.Name] = true
	}
	if !names["size"] || !names["lastId"] {
		t.Errorf("匿名 phi 应恢复变量名 size/lastId（Pos 指向声明），got names=%v", names)
	}
}

// TestLiftingParamBaseRecovers：lifting 参数基址——参数多块使用被
// 提升为 phi，字段路径基址应恢复参数名 v（v.Box2.Value 而非 t0.Box2.
// Value）。
func TestLiftingParamBaseRecovers(t *testing.T) {
	nodes, _ := indexFixture(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

type Box struct{ Box2 *Box2 }
type Box2 struct{ Value int }

func handle(v *Box) int {
	if v.Box2 != nil {
		return v.Box2.Value
	}
	return 0
}
`,
	})
	funcID := "symbol:go:example.com/mtest:handle"
	names := map[string]bool{}
	for _, n := range nodes {
		if n.Kind != domain.KindSSAValue || n.Property("func_id") != funcID {
			continue
		}
		names[n.Name] = true
	}
	if !names["v.Box2.Value"] {
		t.Errorf("lifting 参数基址应恢复 v.Box2.Value，got names=%v", names)
	}
}
