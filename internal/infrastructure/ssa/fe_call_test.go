package ssa

import (
	"strings"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

// TestClosureWriteNodeUnit：已验场景单元测试化——闭包内字段写入节点
// 生成且 func_id 归外层函数（Q14 适配修复回归）。
func TestClosureWriteNodeUnit(t *testing.T) {
	nodes, _, _ := indexFixtureFull(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

type Record struct {
	FinalFee float64
}

func runA() {
	rec := &Record{}
	fn := func() {
		rec.FinalFee = 700
	}
	fn()
	_ = rec
}
`,
	})
	outerID := "symbol:go:example.com/mtest:runA"
	found := false
	for _, n := range nodes {
		if n.Kind == domain.KindFieldAccess && n.Property("full_path") == "example.com/mtest.Record.FinalFee" &&
			n.Property("access_kind") == "write" && n.Property("func_id") == outerID {
			found = true
		}
	}
	if !found {
		t.Errorf("闭包内写入节点应生成且归外层 runA")
	}
}

// TestFuncValueCallEdgeUnit：已验场景单元测试化——函数值调用
// （f := getHandler(); f(rec)）的 argument 边。
func TestFuncValueCallEdgeUnit(t *testing.T) {
	_, facts, _ := indexFixtureFull(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

type Record struct {
	FinalFee float64
}

func handler(r *Record) {
	r.FinalFee = 500
}

func getHandler() func(*Record) {
	return handler
}

func run5() {
	f := getHandler()
	f(&Record{})
}
`,
	})

	found := false
	for _, f := range facts {
		if f.Kind == domain.FactArgument && strings.Contains(string(f.TargetID), ":handler#r") {
			found = true
		}
	}
	if !found {
		t.Errorf("函数值调用未建 argument 边（→ handler#r）")
	}
}

// TestInterfaceCallEdgesUnit：已验场景单元测试化——接口动态调用
// argument + returns 边（⑮）。
func TestInterfaceCallEdgesUnit(t *testing.T) {
	_, facts, _ := indexFixtureFull(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

type Record struct {
	FinalFee float64
}

type Writer interface {
	Write(r *Record) float64
}

type FileWriter struct{}

func (w *FileWriter) Write(r *Record) float64 {
	r.FinalFee = 200
	return r.FinalFee
}

func run6() {
	var w Writer = &FileWriter{}
	fee := w.Write(&Record{})
	_ = fee
}
`,
	})
	hasArg, hasRet := false, false
	for _, f := range facts {
		if f.Kind == domain.FactArgument && strings.Contains(string(f.TargetID), "(FileWriter).Write#r") {
			hasArg = true
		}
		if f.Kind == domain.FactReturns && strings.Contains(string(f.SourceID), "(FileWriter).Write") {
			hasRet = true
		}
	}
	if !hasArg {
		t.Errorf("接口调用未建 argument 边（→ (FileWriter).Write#r）")
	}
	if !hasRet {
		t.Errorf("接口调用未建 returns 边（来自 (FileWriter).Write）")
	}
}

// TestAliasEdgeSourceIsValueNode：B1 回归——alias 边 source 应为 ssa_value
// 值节点（funcID#slot），而非函数/方法节点。此前 funcIDOfValue 返回函数
// ID，alias 边全部错挂在函数节点上（值节点看不到别名关系）。
func TestAliasEdgeSourceIsValueNode(t *testing.T) {
	_, facts := indexFixture(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

type T struct {
	A int
}

func f(x *T) int {
	y := &T{}
	y.A = x.A
	return y.A
}
`,
	})
	aliasCount := 0
	for _, f := range facts {
		if f.Kind != domain.FactAlias {
			continue
		}
		aliasCount++
		src := string(f.SourceID)
		if !strings.Contains(src, "#") {
			t.Errorf("alias source = %q, want ssa_value node (funcID#slot)", src)
		}
	}
	if aliasCount == 0 {
		t.Fatal("无 alias 边发射（fixture 未触发别名分析）")
	}
}
