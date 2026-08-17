package ssa

import (
	"strings"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

const moduleGoMod = `
module example.com/mtest

go 1.26
`

// findFieldAccess 按 (函数, 实例路径, access_kind) 查找字段访问节点。
func findFieldAccess(t *testing.T, nodes []*domain.CodeEntity, funcID, instance, access string) *domain.CodeEntity {
	t.Helper()
	for _, n := range nodes {
		if n.Kind != domain.KindFieldAccess {
			continue
		}
		if n.Property("func_id") != funcID {
			continue
		}
		if n.Property("instance_path") != instance || n.Property("access_kind") != access {
			continue
		}
		return n
	}
	t.Fatalf("field_access not found: func=%s instance=%s access=%s", funcID, instance, access)
	return nil
}

// findSSAValue 按 (函数, slot 前缀) 查找 ssa_value 节点（slot 用前缀匹配，SSA 临时名不稳定）。
func findSSAValue(t *testing.T, nodes []*domain.CodeEntity, funcID, slotPrefix string) *domain.CodeEntity {
	t.Helper()
	for _, n := range nodes {
		if n.Kind != domain.KindSSAValue {
			continue
		}
		if n.Property("func_id") != funcID {
			continue
		}
		if strings.HasPrefix(n.Name, slotPrefix) {
			return n
		}
	}
	t.Fatalf("ssa_value not found: func=%s slot~%s", funcID, slotPrefix)
	return nil
}

// factsFrom 取所有 source 为该节点的边。
func factsFrom(facts []*domain.Fact, id string) []*domain.Fact {
	var out []*domain.Fact
	for _, f := range facts {
		if string(f.SourceID) == id {
			out = append(out, f)
		}
	}
	return out
}

func TestFieldReadWrite(t *testing.T) {
	nodes, facts := indexFixture(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

type T struct {
	A int
	B string
}

func f(x *T, v int) int {
	x.A = v
	return x.A
}
`,
	})
	funcID := "symbol:go:example.com/mtest:f"

	// 写节点：instance_path / full_path / access_kind / func_id / 代码片段
	w := findFieldAccess(t, nodes, funcID, "x.A", "write")
	if w.Property("full_path") != "example.com/mtest.T.A" {
		t.Errorf("write full_path = %q", w.Property("full_path"))
	}
	if w.FilePath != "main.go" {
		t.Errorf("write file = %q", w.FilePath)
	}
	if !strings.Contains(w.Property("code_snippet"), "x.A = v") {
		t.Errorf("write snippet = %q", w.Property("code_snippet"))
	}

	// 读节点
	findFieldAccess(t, nodes, funcID, "x.A", "read")

	// data_flows_to 方向（go/ssa v0.26 表示：读也经 FieldAddr+UnOp(MUL)）：
	//   FieldAddr 写：基地址 x → 写节点；Store: 写入值 v → 写节点
	//   FieldAddr 读：基地址 x → 读节点；读节点 → 解引用结果 ssa_value
	r := findFieldAccess(t, nodes, funcID, "x.A", "read")
	base := findSSAValue(t, nodes, funcID, "x")
	val := findSSAValue(t, nodes, funcID, "v")

	baseEdges := factsFrom(facts, string(base.ID))
	if len(baseEdges) != 2 {
		t.Errorf("base edges = %+v, want 2 (write+read)", baseEdges)
	}
	targets := map[string]bool{}
	for _, f := range baseEdges {
		if f.Kind != domain.FactDataFlowsTo {
			t.Errorf("base edge kind = %s", f.Kind)
		}
		targets[string(f.TargetID)] = true
	}
	if !targets[string(w.ID)] || !targets[string(r.ID)] {
		t.Errorf("base must reach write and read nodes, got %v", targets)
	}
	if f := factsFrom(facts, string(val.ID)); len(f) != 1 || string(f[0].TargetID) != string(w.ID) {
		t.Errorf("val->write edges = %+v", f)
	}
	out := factsFrom(facts, string(r.ID))
	if len(out) != 1 || out[0].Kind != domain.FactDataFlowsTo {
		t.Fatalf("read->result edges = %+v", out)
	}
	target := nodeByID(t, nodes, string(out[0].TargetID))
	if target.Kind != domain.KindSSAValue {
		t.Errorf("read edge target kind = %s, want ssa_value", target.Kind)
	}
	if target.Property("func_id") != funcID {
		t.Errorf("result func_id = %q", target.Property("func_id"))
	}
}

func TestFieldCompoundReadWrite(t *testing.T) {
	// x.A = x.A + 1：同一位置生成 read/write 两个独立节点（ID 以 access 消歧）
	nodes, _ := indexFixture(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

type T struct {
	A int
}

func g(x *T) {
	x.A = x.A + 1
}
`,
	})
	funcID := "symbol:go:example.com/mtest:g"
	read := findFieldAccess(t, nodes, funcID, "x.A", "read")
	write := findFieldAccess(t, nodes, funcID, "x.A", "write")
	if read.ID == write.ID {
		t.Errorf("read/write nodes must be distinct, both = %s", read.ID)
	}
	if read.LineStart != write.LineStart {
		t.Errorf("read line %d != write line %d", read.LineStart, write.LineStart)
	}
}

func TestFieldNested(t *testing.T) {
	// 嵌套字段：o.In.V —— 每层访问独立节点，full_path 用声明类型
	nodes, _ := indexFixture(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

type Inner struct {
	V int
}

type Outer struct {
	In Inner
}

func n(o *Outer) {
	o.In.V = 7
}
`,
	})
	funcID := "symbol:go:example.com/mtest:n"
	first := findFieldAccess(t, nodes, funcID, "o.In", "write")
	if first.Property("full_path") != "example.com/mtest.Outer.In" {
		t.Errorf("first full_path = %q", first.Property("full_path"))
	}
	second := findFieldAccess(t, nodes, funcID, "o.In.V", "write")
	if second.Property("full_path") != "example.com/mtest.Inner.V" {
		t.Errorf("second full_path = %q", second.Property("full_path"))
	}
}

func TestFieldEmbedded(t *testing.T) {
	// 嵌入字段：SSA 降级为两层访问（o.Emb 与 o.Emb.V）；
	// full_path 用声明类型（Emb.V），instance_path 为 SSA 链（Q25 源码形式近似）
	nodes, _ := indexFixture(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

type Emb struct {
	V int
}

type O2 struct {
	Emb
}

func e(o *O2) {
	o.V = 1
}
`,
	})
	funcID := "symbol:go:example.com/mtest:e"
	inner := findFieldAccess(t, nodes, funcID, "o.Emb.V", "write")
	if inner.Property("full_path") != "example.com/mtest.Emb.V" {
		t.Errorf("embedded full_path = %q", inner.Property("full_path"))
	}
	findFieldAccess(t, nodes, funcID, "o.Emb", "write")
}

func TestFieldGlobal(t *testing.T) {
	// 全局变量：基地址 ssa_value origin_kind=global
	nodes, _ := indexFixture(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

type T struct {
	A int
}

var G T

func h() {
	G.A = 5
}
`,
	})
	funcID := "symbol:go:example.com/mtest:h"
	w := findFieldAccess(t, nodes, funcID, "G.A", "write")
	if w.Property("full_path") != "example.com/mtest.T.A" {
		t.Errorf("global full_path = %q", w.Property("full_path"))
	}
	// 全局变量节点跨函数共享（Q98 溯源锚点）：ID 与函数命名空间无关
	g := nodeByID(t, nodes, "symbol:go:example.com/mtest:var.G")
	if g.Property("origin_kind") != "global" {
		t.Errorf("global origin_kind = %q", g.Property("origin_kind"))
	}
}

func TestFieldShadowingDisambiguated(t *testing.T) {
	// shadowing：两个作用域的同名 x 各自访问字段 → 两个独立写节点
	// （同一实例路径 x.A，行号消歧），instance_path 均还原为 x.A
	nodes, _ := indexFixture(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

type T struct {
	A int
}

func s() {
	x := T{}
	{
		x := T{}
		x.A = 1
	}
	x.A = 2
}
`,
	})
	funcID := "symbol:go:example.com/mtest:s"
	var ids []string
	for _, n := range nodes {
		if n.Kind == domain.KindFieldAccess && n.Property("func_id") == funcID &&
			n.Property("instance_path") == "x.A" && n.Property("access_kind") == "write" {
			ids = append(ids, string(n.ID))
		}
	}
	if len(ids) != 2 {
		t.Fatalf("shadowed x.A writes = %d, want 2: %v", len(ids), ids)
	}
	if ids[0] == ids[1] {
		t.Errorf("shadowed accesses must be distinct, both = %s", ids[0])
	}
}

// nodeByID 按 ID 查找节点。
func nodeByID(t *testing.T, nodes []*domain.CodeEntity, id string) *domain.CodeEntity {
	t.Helper()
	for _, n := range nodes {
		if string(n.ID) == id {
			return n
		}
	}
	t.Fatalf("node not found: %s", id)
	return nil
}

func TestFieldNestedReadPropagates(t *testing.T) {
	// 嵌套字段链 m.cfg.APIKey 读：内层 m.cfg 的用途从外层传播——
	// 读链上的中间层是 read，不是"无用途默认 write"（误报写会污染
	// 间接写摘要：newLLM 只读 cfg 却出现 direct_write Manager.cfg）
	nodes, facts := indexFixture(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

type Config struct {
	APIKey string
}

type Manager struct {
	cfg Config
}

func newLLM(m *Manager) {
	if m.cfg.APIKey == "" {
		return
	}
	_ = m.cfg.APIKey
}
`,
	})
	funcID := "symbol:go:example.com/mtest:newLLM"
	// 内层 m.cfg：read 节点（跟随外层读），不产生 write 节点
	findFieldAccess(t, nodes, funcID, "m.cfg", "read")
	ids := []string{}
	for _, n := range nodes {
		if n.Kind == domain.KindFieldAccess && n.Property("func_id") == funcID &&
			n.Property("instance_path") == "m.cfg" && n.Property("access_kind") == "write" {
			ids = append(ids, string(n.ID))
		}
	}
	if len(ids) != 0 {
		t.Errorf("m.cfg write nodes = %v, want none（读链中间层不应标写）", ids)
	}
	// 最外层 m.cfg.APIKey：read
	findFieldAccess(t, nodes, funcID, "m.cfg.APIKey", "read")
	// 数据流链完整（基值 → 内层字段节点 → 外层字段节点）：
	// 内层 m.cfg 节点的入边来自参数 m，出边指向外层 m.cfg.APIKey
	inner := findFieldAccess(t, nodes, funcID, "m.cfg", "read")
	outer := findFieldAccess(t, nodes, funcID, "m.cfg.APIKey", "read")
	paramM := findSSAValue(t, nodes, funcID, "m")
	for _, f := range factsFrom(facts, string(paramM.ID)) {
		if f.TargetID == inner.ID {
			goto innerLinked
		}
	}
	t.Error("参数 m → 内层 m.cfg 边缺失")
innerLinked:
	for _, f := range factsFrom(facts, string(inner.ID)) {
		if f.TargetID == outer.ID {
			return
		}
	}
	t.Error("内层 m.cfg → 外层 m.cfg.APIKey 边缺失（平行 ssa_value 链应已合并）")
}

func TestElementLiteralInitFiltered(t *testing.T) {
	// []T{...} 字面量 lifting（*[N]T 数组，无源码位置）：字面量初始化
	// 不是元素访问，不产节点（opts[0] 噪音）
	nodes, _ := indexFixture(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

type Option struct{ V int }

func f() {
	opts := []Option{{V: 1}, {V: 2}}
	_ = opts
}
`,
	})
	funcID := "symbol:go:example.com/mtest:f"
	for _, n := range nodes {
		if n.Kind == domain.KindFieldAccess && n.Property("func_id") == funcID &&
			strings.Contains(n.Property("instance_path"), "[") {
			t.Errorf("字面量初始化不应产元素节点: %v", n.Property("instance_path"))
		}
	}
}

func TestElementArrayVarKept(t *testing.T) {
	// 真数组变量 a[0] = 1（有源码位置）：保留元素访问
	nodes, _ := indexFixture(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

func f() {
	var a [3]int
	a[0] = 1
	_ = a[0]
}
`,
	})
	funcID := "symbol:go:example.com/mtest:f"
	findFieldByPath(t, nodes, funcID, `a[0]`) // write
	findFieldAccess(t, nodes, funcID, `a[0]`, "read")
}

func TestFieldAnonymousStructFallback(t *testing.T) {
	// 匿名 struct：静态类型无稳定身份 → full_path 回退源码字面量路径（§6.1）
	nodes, _ := indexFixture(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

func f() {
	x := struct{ A int }{}
	x.A = 1
}
`,
	})
	funcID := "symbol:go:example.com/mtest:f"
	var fa *domain.CodeEntity
	for _, n := range nodes {
		if n.Kind == domain.KindFieldAccess && n.Property("func_id") == funcID {
			fa = n
		}
	}
	if fa == nil {
		t.Fatal("anonymous struct field access node missing (fallback 应产出节点)")
	}
	if fa.Property("full_path") != fa.Property("instance_path") {
		t.Errorf("fallback full_path = %q, want 回退为 instance_path %q",
			fa.Property("full_path"), fa.Property("instance_path"))
	}
	if fa.Property("access_kind") != "write" {
		t.Errorf("access = %q", fa.Property("access_kind"))
	}
}

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
	// run5 → handler 的 argument 边（实参 → handler#r）
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

// TestAnonymousStructFieldAccessHasLine：B3 回归——匿名 struct（range 元素
// 等）的字段访问须有行号与文件（fieldInfo 的匿名分支曾提前 return，
// line_start=0 导致 CLI 无定位、前端无锚点）。
func TestAnonymousStructFieldAccessHasLine(t *testing.T) {
	nodes, _ := indexFixture(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

type Conf struct {
	Items []struct {
		Key string
	}
}

func f(c Conf) {
	for _, s := range c.Items {
		_ = s.Key
	}
}
`,
	})
	funcID := "symbol:go:example.com/mtest:f"
	var keyNode *domain.CodeEntity
	for _, n := range nodes {
		if n.Kind != domain.KindFieldAccess || n.Property("func_id") != funcID {
			continue
		}
		if n.Property("instance_path") == "s.Key" && n.Property("access_kind") == "read" {
			keyNode = n
			break
		}
	}
	if keyNode == nil {
		t.Fatal("s.Key 读节点未生成（匿名 struct 字段访问丢失）")
	}
	if keyNode.LineStart <= 0 {
		t.Errorf("匿名 struct 字段访问 line_start = %d, want > 0", keyNode.LineStart)
	}
	if keyNode.FilePath != "main.go" {
		t.Errorf("匿名 struct 字段访问 file = %q, want main.go", keyNode.FilePath)
	}
}
