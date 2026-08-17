package ssa

import (
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

// TestLocalObjectTraceSelfContained：⑭ 局部对象追踪——DAO 返回对象 →
// 局部变量 → helper 传参（起点须纳入与目标字段同类型的 local/phi 值）。

// TestInterfaceCallTraceSelfContained：⑮ 接口动态派发——接口方法调用
// 传参（无静态 callee）须经候选实现建立 argument 边，追踪进入实现。
func TestInterfaceCallTraceSelfContained(t *testing.T) {
	repo := indexFixtureRepo(t, map[string]string{
		"go.mod": "module example.com/mtest\n\ngo 1.21\n",
		"main.go": `package ifc

type Record struct {
	FinalFee float64
}

type Writer interface {
	Write(r *Record)
}

type FileWriter struct{}

func (w *FileWriter) Write(r *Record) {
	r.FinalFee = 200
}

func run2() {
	var w Writer = &FileWriter{}
	w.Write(&Record{})
}

func main() {}
`,
	})
	rows, err := repo.TraceForward("example.com/mtest.Record.FinalFee", domain.CanonicalID("symbol:go:example.com/mtest:run2"), 8)
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if !traceHas(rows, "r.FinalFee") {
		t.Errorf("接口调用传参未连到实现 (FileWriter).Write 的写入，output=%v", rows)
	}
}

// TestGlobalObjectTraceSelfContained：举一反三 A1——全局变量对象传参
// （var g Record; helper(&g)）trace-forward 起点（global 值来源格）。
func TestGlobalObjectTraceSelfContained(t *testing.T) {
	repo := indexFixtureRepo(t, map[string]string{
		"go.mod": "module example.com/mtest\n\ngo 1.21\n",
		"main.go": `package glb

type Record struct {
	FinalFee float64
}

var g Record

func helper2(r *Record) {
	r.FinalFee = 300
}

func run3() {
	helper2(&g)
}

func main() {}
`,
	})
	rows, err := repo.TraceForward("example.com/mtest.Record.FinalFee", domain.CanonicalID("symbol:go:example.com/mtest:run3"), 8)
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if !traceHas(rows, "r.FinalFee") {
		t.Errorf("全局对象传参未连到 helper2 写入，output=%v", rows)
	}
}

// TestPhiObjectTraceSelfContained：举一反三 A2——phi 值传参
// （if 分支各自赋值后传 helper）。

// TestFuncValueCallTraceSelfContained：举一反三 B4——函数值调用
// （f := getHandler(); f(record)——f 来自返回值，调用点无静态 callee）。

// TestInterfaceReturnTraceSelfContained：举一反三——动态调用返回值贯通：
// err := w.Write(&Record{})——value-trace 从返回值节点应连到候选实现的
// Return 值（⑮ 只建了 argument，returns 边待验证）。

// TestClosureFieldTraceSelfContained：继续查——闭包内字段写入节点生成
// （闭包字段访问归入外层函数，func_id=外层——追踪可用性验证）。
func TestClosureFieldTraceSelfContained(t *testing.T) {
	repo := indexFixtureRepo(t, map[string]string{
		"go.mod": "module example.com/mtest\n\ngo 1.21\n",
		"main.go": `package cl

type Record struct {
	FinalFee float64
}

func run8() {
	rec := &Record{}
	fn := func() {
		rec.FinalFee = 700
	}
	fn()
	_ = rec
}

func main() {}
`,
	})
	// 闭包内写入节点：func_id 应为外层 run8（归入外层函数）
	var writeID string
	rows, err := repo.Query(`SELECT id FROM nodes WHERE kind='field_access'
			AND json_extract(properties, '$.full_path') = 'example.com/mtest.Record.FinalFee'
			AND json_extract(properties, '$.access_kind') = 'write'`)
	if err != nil {
		t.Fatal(err)
	}
	if rows.Next() {
		_ = rows.Scan(&writeID)
	}
	rows.Close()
	if writeID == "" {
		t.Fatalf("闭包内字段写入节点缺失")
	}
	vrows, err := repo.GetValueTrace(domain.CanonicalID(writeID), 8, 1.0, false)
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if !traceHas(vrows, "run8") {
		t.Errorf("闭包内写入未归入外层函数，output=%v", vrows)
	}
}

// TestMapElemArgTraceSelfContained：继续查——map 元素值传参
// （m["k"] 的值传给 helper）。
func TestMapElemArgTraceSelfContained(t *testing.T) {
	repo := indexFixtureRepo(t, map[string]string{
		"go.mod": "module example.com/mtest\n\ngo 1.21\n",
		"main.go": `package me

type Record struct {
	FinalFee float64
}

func helper5(r *Record) {
	r.FinalFee = 800
}

func run9(m map[string]*Record) {
	helper5(m["k"])
}

func main() {}
`,
	})
	rows, err := repo.TraceForward("example.com/mtest.Record.FinalFee", domain.CanonicalID("symbol:go:example.com/mtest:run9"), 8)
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if !traceHas(rows, "r.FinalFee") {
		t.Errorf("map 元素传参未连到 helper5 写入，output=%v", rows)
	}
}

// TestCallbackClosureArgTraceSelfContained：继续查——callback 模式
// （apply(rec, func(r){r.FinalFee=...})——闭包字面量作为实参传入后在被
// 调函数内调用）：预期为已知限制（函数值参数跨函数无法静态解析），
// 此处验证不 panic 且不误连。

// TestTraceForwardStartFilteredSelfContained：B2 集成固化——trace-forward
// 起点须与目标字段所属结构体类型匹配；无关类型参数与包级全局变量
// （string）不得入链（此前 origin_kind IN (...) 无条件放行全部起点）。
func TestTraceForwardStartFilteredSelfContained(t *testing.T) {
	repo := indexFixtureRepo(t, map[string]string{
		"go.mod": "module example.com/mtest\n\ngo 1.21\n",
		"main.go": `package filt

var globalName = "x"

type Record struct {
	FinalFee float64
}

func A(record *Record, name string) {
	_ = name
	_ = globalName
	record.FinalFee = 1
}

func main() {}
`,
	})
	rows, err := repo.TraceForward("example.com/mtest.Record.FinalFee", domain.CanonicalID("symbol:go:example.com/mtest:A"), 8)
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if !traceHas(rows, "FinalFee") {
		t.Errorf("目标字段 FinalFee 未入链，output=%v", rows)
	}
	for _, noise := range []string{"globalName", "name"} {
		if traceHas(rows, noise) {
			t.Errorf("无关类型起点 %s 不应入链（B2 类型过滤），output=%v", noise, rows)
		}
	}
}
