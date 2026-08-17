//go:build integration

package integration

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
)

// TestLocalObjectTraceSelfContained：⑭ 局部对象追踪——DAO 返回对象 →
// 局部变量 → helper 传参（起点须纳入与目标字段同类型的 local/phi 值）。
func TestLocalObjectTraceSelfContained(t *testing.T) {
	if !scipGoAvailable() {
		t.Skip("scip-go not found")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/loc\n\ngo 1.21\n")
	writeFile(t, filepath.Join(dir, "main.go"), `package loc

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
`)
	if code := runCLI(t, "init", "--repo", dir); code != 0 {
		t.Fatalf("init exit = %d", code)
	}
	code, out := runCLIOut(t, "query", "trace-forward", "example.com/loc.Record.FinalFee",
		"--func", "symbol:go:example.com/loc:run", "--repo", dir)
	if code != 0 {
		t.Fatalf("trace-forward exit = %d", code)
	}
	if !strings.Contains(out, "r.FinalFee") {
		t.Errorf("局部对象（DAO 返回）传参未连到 helper 写入，output=%q", out[:min(len(out), 400)])
	}
}

// TestInterfaceCallTraceSelfContained：⑮ 接口动态派发——接口方法调用
// 传参（无静态 callee）须经候选实现建立 argument 边，追踪进入实现。
func TestInterfaceCallTraceSelfContained(t *testing.T) {
	if !scipGoAvailable() {
		t.Skip("scip-go not found")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/ifc\n\ngo 1.21\n")
	writeFile(t, filepath.Join(dir, "main.go"), `package ifc

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
`)
	if code := runCLI(t, "init", "--repo", dir); code != 0 {
		t.Fatalf("init exit = %d", code)
	}
	code, out := runCLIOut(t, "query", "trace-forward", "example.com/ifc.Record.FinalFee",
		"--func", "symbol:go:example.com/ifc:run2", "--repo", dir)
	if code != 0 {
		t.Fatalf("trace-forward exit = %d", code)
	}
	if !strings.Contains(out, "r.FinalFee") {
		t.Errorf("接口调用传参未连到实现 (FileWriter).Write 的写入，output=%q", out[:min(len(out), 400)])
	}
}

// TestGlobalObjectTraceSelfContained：举一反三 A1——全局变量对象传参
// （var g Record; helper(&g)）trace-forward 起点（global 值来源格）。
func TestGlobalObjectTraceSelfContained(t *testing.T) {
	if !scipGoAvailable() {
		t.Skip("scip-go not found")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/glb\n\ngo 1.21\n")
	writeFile(t, filepath.Join(dir, "main.go"), `package glb

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
`)
	if code := runCLI(t, "init", "--repo", dir); code != 0 {
		t.Fatalf("init exit = %d", code)
	}
	code, out := runCLIOut(t, "query", "trace-forward", "example.com/glb.Record.FinalFee",
		"--func", "symbol:go:example.com/glb:run3", "--repo", dir)
	if code != 0 {
		t.Fatalf("trace-forward exit = %d", code)
	}
	if !strings.Contains(out, "r.FinalFee") {
		t.Errorf("全局对象传参未连到 helper2 写入，output=%q", out[:min(len(out), 400)])
	}
}

// TestPhiObjectTraceSelfContained：举一反三 A2——phi 值传参
// （if 分支各自赋值后传 helper）。
func TestPhiObjectTraceSelfContained(t *testing.T) {
	if !scipGoAvailable() {
		t.Skip("scip-go not found")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/phi\n\ngo 1.21\n")
	writeFile(t, filepath.Join(dir, "main.go"), `package phi

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
`)
	if code := runCLI(t, "init", "--repo", dir); code != 0 {
		t.Fatalf("init exit = %d", code)
	}
	code, out := runCLIOut(t, "query", "trace-forward", "example.com/phi.Record.FinalFee",
		"--func", "symbol:go:example.com/phi:run4", "--repo", dir)
	if code != 0 {
		t.Fatalf("trace-forward exit = %d", code)
	}
	if !strings.Contains(out, "r.FinalFee") {
		t.Errorf("phi 值传参未连到 helper3 写入，output=%q", out[:min(len(out), 400)])
	}
}

// TestFuncValueCallTraceSelfContained：举一反三 B4——函数值调用
// （f := getHandler(); f(record)——f 来自返回值，调用点无静态 callee）。
func TestFuncValueCallTraceSelfContained(t *testing.T) {
	if !scipGoAvailable() {
		t.Skip("scip-go not found")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/fv\n\ngo 1.21\n")
	writeFile(t, filepath.Join(dir, "main.go"), `package fv

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
`)
	if code := runCLI(t, "init", "--repo", dir); code != 0 {
		t.Fatalf("init exit = %d", code)
	}
	code, out := runCLIOut(t, "query", "trace-forward", "example.com/fv.Record.FinalFee",
		"--func", "symbol:go:example.com/fv:run5", "--repo", dir)
	if code != 0 {
		t.Fatalf("trace-forward exit = %d", code)
	}
	if !strings.Contains(out, "r.FinalFee") {
		t.Errorf("函数值调用未连到 handler 写入，output=%q", out[:min(len(out), 400)])
	}
}

// TestInterfaceReturnTraceSelfContained：举一反三——动态调用返回值贯通：
// err := w.Write(&Record{})——value-trace 从返回值节点应连到候选实现的
// Return 值（⑮ 只建了 argument，returns 边待验证）。
func TestInterfaceReturnTraceSelfContained(t *testing.T) {
	if !scipGoAvailable() {
		t.Skip("scip-go not found")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/ifr\n\ngo 1.21\n")
	writeFile(t, filepath.Join(dir, "main.go"), `package ifr

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
`)
	if code := runCLI(t, "init", "--repo", dir); code != 0 {
		t.Fatalf("init exit = %d", code)
	}
	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := sqlite.NewRepo(db)
	// 返回值节点：run6 中动态调用的结果（SSA 对只写不读的 err 重命名为
	// tN——经 returns 入边定位）
	var retID string
	rows, err := repo.Query(`SELECT target_id FROM edges WHERE kind='returns'
		AND target_id LIKE 'symbol:go:example.com/ifr:run6#%' LIMIT 1`)
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
	code, out := runCLIOut(t, "query", "value-trace", retID, "--repo", dir, "--min-conf", "0")
	if code != 0 {
		t.Fatalf("value-trace exit = %d", code)
	}
	if !strings.Contains(out, "(FileWriter).Write") {
		t.Errorf("返回值未连到候选实现 (FileWriter).Write，output=%q", out[:min(len(out), 300)])
	}
}
