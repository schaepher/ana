//go:build integration

package integration

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
)

// TestClosureFieldTraceSelfContained：继续查——闭包内字段写入节点生成
// （闭包字段访问归入外层函数，func_id=外层——追踪可用性验证）。
func TestClosureFieldTraceSelfContained(t *testing.T) {
	if !scipGoAvailable() {
		t.Skip("scip-go not found")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/cl\n\ngo 1.21\n")
	writeFile(t, filepath.Join(dir, "main.go"), `package cl

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
	// 闭包内写入节点：func_id 应为外层 run8（归入外层函数）
	var writeID string
	rows, err := repo.Query(`SELECT id FROM nodes WHERE kind='field_access'
		AND json_extract(properties, '$.full_path') = 'example.com/cl.Record.FinalFee'
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

	code, out := runCLIOut(t, "query", "value-trace", writeID, "--repo", dir)
	if code != 0 {
		t.Fatalf("value-trace exit = %d", code)
	}
	if !strings.Contains(out, "run8") {
		t.Errorf("闭包内写入未归入外层函数，output=%q", out[:min(len(out), 300)])
	}
}

// TestMapElemArgTraceSelfContained：继续查——map 元素值传参
// （m["k"] 的值传给 helper）。
func TestMapElemArgTraceSelfContained(t *testing.T) {
	if !scipGoAvailable() {
		t.Skip("scip-go not found")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/me\n\ngo 1.21\n")
	writeFile(t, filepath.Join(dir, "main.go"), `package me

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
`)
	if code := runCLI(t, "init", "--repo", dir); code != 0 {
		t.Fatalf("init exit = %d", code)
	}
	code, out := runCLIOut(t, "query", "trace-forward", "example.com/me.Record.FinalFee",
		"--func", "symbol:go:example.com/me:run9", "--repo", dir)
	if code != 0 {
		t.Fatalf("trace-forward exit = %d", code)
	}
	if !strings.Contains(out, "r.FinalFee") {
		t.Errorf("map 元素传参未连到 helper5 写入，output=%q", out[:min(len(out), 400)])
	}
}

// TestCallbackClosureArgTraceSelfContained：继续查——callback 模式
// （apply(rec, func(r){r.FinalFee=...})——闭包字面量作为实参传入后在被
// 调函数内调用）：预期为已知限制（函数值参数跨函数无法静态解析），
// 此处验证不 panic 且不误连。
func TestCallbackClosureArgTraceSelfContained(t *testing.T) {
	if !scipGoAvailable() {
		t.Skip("scip-go not found")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/cb\n\ngo 1.21\n")
	writeFile(t, filepath.Join(dir, "main.go"), `package cb

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
`)
	if code := runCLI(t, "init", "--repo", dir); code != 0 {
		t.Fatalf("init exit = %d", code)
	}
	code, _ := runCLIOut(t, "query", "trace-forward", "example.com/cb.Record.FinalFee",
		"--func", "symbol:go:example.com/cb:runB", "--repo", dir)
	if code != 0 {
		t.Fatalf("trace-forward exit = %d", code)
	}

	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := sqlite.NewRepo(db)
	var cnt int
	rows, err := repo.Query(`SELECT count(*) FROM nodes WHERE kind='field_access'
		AND json_extract(properties, '$.full_path') = 'example.com/cb.Record.FinalFee'
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

// TestTraceForwardStartFilteredSelfContained：B2 集成固化——trace-forward
// 起点须与目标字段所属结构体类型匹配；无关类型参数与包级全局变量
// （string）不得入链（此前 origin_kind IN (...) 无条件放行全部起点）。
func TestTraceForwardStartFilteredSelfContained(t *testing.T) {
	if !scipGoAvailable() {
		t.Skip("scip-go not found")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/filt\n\ngo 1.21\n")
	writeFile(t, filepath.Join(dir, "main.go"), `package filt

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
`)
	if code := runCLI(t, "init", "--repo", dir); code != 0 {
		t.Fatalf("init exit = %d", code)
	}
	code, out := runCLIOut(t, "query", "trace-forward", "example.com/filt.Record.FinalFee",
		"--func", "symbol:go:example.com/filt:A", "--repo", dir)
	if code != 0 {
		t.Fatalf("trace-forward exit = %d", code)
	}
	if !strings.Contains(out, "FinalFee") {
		t.Errorf("目标字段 FinalFee 未入链，output=%q", out[:min(len(out), 300)])
	}
	for _, noise := range []string{"globalName", "name"} {
		if strings.Contains(out, noise) {
			t.Errorf("无关类型起点 %s 不应入链（B2 类型过滤），output=%q", noise, out[:min(len(out), 300)])
		}
	}
}
