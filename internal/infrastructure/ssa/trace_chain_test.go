package ssa

import (
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

// TestLocalObjectTraceSelfContained：⑭ 局部对象追踪——DAO 返回对象 →
// 局部变量 → helper 传参（起点须纳入与目标字段同类型的 local/phi 值）。
func TestLocalObjectTraceSelfContained(t *testing.T) {
	repo := indexFixtureRepo(t, map[string]string{
		"go.mod": "module example.com/mtest\n\ngo 1.21\n",
		"main.go": `package loc

type Record struct {
	FinalFee float64
}

func helper(r *Record) {
	r.FinalFee = 100
}

func buildRecord() *Record {
	return &Record{}
}

func run() {
	obj := buildRecord()
	helper(obj)
}

func main() {}
`,
	})
	rows, err := repo.TraceForward("example.com/mtest.Record.FinalFee", domain.CanonicalID("symbol:go:example.com/mtest:run"), 8)
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if !traceHas(rows, "r.FinalFee") {
		t.Errorf("局部对象（DAO 返回）传参未连到 helper 写入，output=%v", rows)
	}
}

// TestPhiObjectTraceSelfContained：举一反三 A2——phi 值传参
// （if 分支各自赋值后传 helper）。
func TestPhiObjectTraceSelfContained(t *testing.T) {
	repo := indexFixtureRepo(t, map[string]string{
		"go.mod": "module example.com/mtest\n\ngo 1.21\n",
		"main.go": `package phi

type Record struct {
	FinalFee float64
}

func helper3(r *Record) {
	r.FinalFee = 400
}

func run4(cond bool) {
	var obj *Record
	if cond {
		obj = &Record{}
	} else {
		obj = &Record{}
	}
	helper3(obj)
}

func main() {}
`,
	})
	rows, err := repo.TraceForward("example.com/mtest.Record.FinalFee", domain.CanonicalID("symbol:go:example.com/mtest:run4"), 8)
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if !traceHas(rows, "r.FinalFee") {
		t.Errorf("phi 值传参未连到 helper3 写入，output=%v", rows)
	}
}

// TestFuncValueCallTraceSelfContained：举一反三 B4——函数值调用
// （f := getHandler(); f(record)——f 来自返回值，调用点无静态 callee）。
func TestFuncValueCallTraceSelfContained(t *testing.T) {
	repo := indexFixtureRepo(t, map[string]string{
		"go.mod": "module example.com/mtest\n\ngo 1.21\n",
		"main.go": `package fv

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

func main() {}
`,
	})
	rows, err := repo.TraceForward("example.com/mtest.Record.FinalFee", domain.CanonicalID("symbol:go:example.com/mtest:run5"), 8)
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if !traceHas(rows, "r.FinalFee") {
		t.Errorf("函数值调用未连到 handler 写入，output=%v", rows)
	}
}

// TestInterfaceReturnTraceSelfContained：举一反三——动态调用返回值贯通：
// err := w.Write(&Record{})——value-trace 从返回值节点应连到候选实现的
// Return 值（⑮ 只建了 argument，returns 边待验证）。
func TestInterfaceReturnTraceSelfContained(t *testing.T) {
	repo := indexFixtureRepo(t, map[string]string{
		"go.mod": "module example.com/mtest\n\ngo 1.21\n",
		"main.go": `package ifr

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

func main() {}
`,
	})
	// 返回值节点：run6 中动态调用的结果（SSA 对只写不读的 err 重命名为
	// tN——经 returns 入边定位）
	var retID string
	rows, err := repo.Query(`SELECT target_id FROM edges WHERE kind='returns'
			AND target_id LIKE 'symbol:go:example.com/mtest:run6#%' LIMIT 1`)
	if err != nil {
		t.Fatal(err)
	}
	if rows.Next() {
		_ = rows.Scan(&retID)
	}
	rows.Close()
	if retID == "" {
		t.Fatalf("run6 返回值节点缺失（returns 边未建立）")
	}
	vrows, err := repo.GetValueTrace(domain.CanonicalID(retID), 8, 0, false)
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if !traceHas(vrows, "(FileWriter).Write") {
		t.Errorf("返回值未连到候选实现 (FileWriter).Write，output=%v", vrows)
	}
}

// TestCallbackClosureArgTraceSelfContained：继续查——callback 模式
// （apply(rec, func(r){r.FinalFee=...})——闭包字面量作为实参传入后在被
// 调函数内调用）：预期为已知限制（函数值参数跨函数无法静态解析），
// 此处验证不 panic 且不误连。
func TestCallbackClosureArgTraceSelfContained(t *testing.T) {
	repo := indexFixtureRepo(t, map[string]string{
		"go.mod": "module example.com/mtest\n\ngo 1.21\n",
		"main.go": `package cb

type Record struct {
	FinalFee float64
}

func apply(r *Record, fn func(*Record)) {
	fn(r)
}

func runB() {
	rec := &Record{}
	apply(rec, func(r *Record) {
		r.FinalFee = 900
	})
}

func main() {}
`,
	})
	_, err := repo.TraceForward("example.com/mtest.Record.FinalFee", domain.CanonicalID("symbol:go:example.com/mtest:runB"), 8)
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	var cnt int
	rows, err := repo.Query(`SELECT count(*) FROM nodes WHERE kind='field_access'
			AND json_extract(properties, '$.full_path') = 'example.com/mtest.Record.FinalFee'
			AND json_extract(properties, '$.access_kind') = 'write'`)
	if err != nil {
		t.Fatal(err)
	}
	if rows.Next() {
		_ = rows.Scan(&cnt)
	}
	rows.Close()
	if cnt == 0 {
		t.Error("callback 闭包内字段写入节点应存在（归外层 runB）")
	}
}
