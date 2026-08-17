package ast

import (
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

// TestNestedArgExternalCallee：P2-2——外层 callee 是外部包函数时，
// 参数位置的嵌套调用仍建 passes_result（fmt.Errorf("%v", joinIDs(x))
// → joinIDs 有入边，unused 不误报）。
func TestNestedArgExternalCallee(t *testing.T) {
	_, facts := indexFixture(t, map[string]string{
		"go.mod": fixtureGoMod,
		"a.go": `package m

import "fmt"

func joinIDs(ids []int) []int { return ids }

func callNested() {
	_ = fmt.Errorf("%v", joinIDs([]int{1}))
}
`,
	})

	findFact(t, facts, "symbol:go:fmt:Errorf", "symbol:go:example.com/mtest:joinIDs", string(domain.FactPassesResult))
}

// TestEmbeddedPromotedMethodCalled：P2-2 固化——嵌入提升方法调用
// （a.Exec，Exec 由嵌入字段提升）建 calls 边到声明方法 (DB).Exec，
// unused 不误报（§16.2 旧盲区，Selection 解析已解决）。
func TestEmbeddedPromotedMethodCalled(t *testing.T) {
	_, facts := indexFixture(t, map[string]string{
		"go.mod": fixtureGoMod,
		"db/db.go": `package db

type DB struct{}

func (d *DB) Exec(q string) {}
`,
		"a.go": `package m

import "example.com/mtest/db"

type App struct {
	*db.DB
}

func (a *App) Run() {
	a.Exec("select 1")
}
`,
	})
	findFact(t, facts, "symbol:go:example.com/mtest:(App).Run", "symbol:go:example.com/mtest/db:(DB).Exec", string(domain.FactCalls))
}

// TestFuncValueCall：P2-1——函数值赋值盲区收敛：
// f := g; f() 建 calls 边（h→g）；方法值 fn := obj.M; fn() 建边（m→(T).M）。
func TestFuncValueCall(t *testing.T) {
	_, facts := indexFixture(t, map[string]string{
		"go.mod": fixtureGoMod,
		"a.go": `package m

type T struct{}

func (t *T) M() {}

func g() {}

func h() {
	f := g
	f()
}

func callMethodValue() {
	obj := &T{}
	fn := obj.M
	fn()
}
`,
	})
	findFact(t, facts, "symbol:go:example.com/mtest:h", "symbol:go:example.com/mtest:g", string(domain.FactCalls))
	findFact(t, facts, "symbol:go:example.com/mtest:callMethodValue", "symbol:go:example.com/mtest:(T).M", string(domain.FactCalls))
}
