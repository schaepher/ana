package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// rename 符号感知重命名（Q235-4，借鉴 GitNexus「重命名禁用查找替换」）：
// AST Ident 遍历替换——字符串/注释/import 路径天然不动；词法作用域栈
// 处理遮蔽（局部声明遮蔽包级同名符号）；声明跟随（函数+调用处/类型+
// 使用处/方法+选择器/包级变量+引用）；新名冲突报错不写文件。

// writeTemp 写临时文件返回路径。
func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestRenamePackageFunc：包级函数重命名——声明 + 调用处；字符串/
// 注释不动。
func TestRenamePackageFunc(t *testing.T) {
	src := `package m

// loadOrder 加载订单。
func loadOrder(id int64) int64 {
	// 调用 loadOrder 两次
	return loadOrder(id) + loadOrder(id)
}

func other() {
	_ = loadOrder(1)
	s := "loadOrder(2) 是字符串"
	_ = s
}
`
	p := writeTemp(t, "a.go", src)
	if _, err := renameSymbol([]string{p}, "loadOrder", "fetchOrder", "file", false, false); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, p)
	if !strings.Contains(got, "func fetchOrder(id int64) int64") {
		t.Errorf("声明未重命名:\n%s", got)
	}
	if !strings.Contains(got, "return fetchOrder(id) + fetchOrder(id)") {
		t.Errorf("调用处未重命名:\n%s", got)
	}
	if !strings.Contains(got, `"loadOrder(2) 是字符串"`) {
		t.Errorf("字符串字面量不应改动:\n%s", got)
	}
	if !strings.Contains(got, "// loadOrder 加载订单。") {
		t.Errorf("注释不应改动:\n%s", got)
	}
}

// TestRenameType：类型重命名——声明 + 使用处（含指针/切片/声明变量）。
func TestRenameType(t *testing.T) {
	src := `package m

type Item struct{ ID int64 }

func use() {
	var a Item
	var b *Item
	_ = []Item{a}
	var c Item
	_ = b
	_ = c
}
`
	p := writeTemp(t, "a.go", src)
	if _, err := renameSymbol([]string{p}, "Item", "Goods", "file", false, false); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, p)
	if strings.Contains(got, "Item") {
		t.Errorf("Item 残留（类型声明/使用处都应重命名）:\n%s", got)
	}
	if !strings.Contains(got, "type Goods struct") || !strings.Contains(got, "var a Goods") ||
		!strings.Contains(got, "*Goods") || !strings.Contains(got, "[]Goods") {
		t.Errorf("类型使用处未全部重命名:\n%s", got)
	}
}

// TestRenameMethod：方法重命名——声明 + x.m() 选择器调用。
func TestRenameMethod(t *testing.T) {
	src := `package m

type Repo struct{}

func (r *Repo) Save(id int64) {}

func use(r *Repo) {
	r.Save(1)
	r.Save(2)
}
`
	p := writeTemp(t, "a.go", src)
	if _, err := renameSymbol([]string{p}, "Save", "Store", "file", false, false); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, p)
	if !strings.Contains(got, "func (r *Repo) Store(id int64)") {
		t.Errorf("方法声明未重命名:\n%s", got)
	}
	if !strings.Contains(got, "r.Store(1)") || !strings.Contains(got, "r.Store(2)") {
		t.Errorf("方法调用处未重命名:\n%s", got)
	}
}

// TestRenameLocalVar：局部变量重命名——var/短声明/参数。
func TestRenameLocalVar(t *testing.T) {
	src := `package m

func f() {
	var count int
	count = 1
	for i := 0; i < count; i++ {
		count++
	}
	_ = count
}

func g(count int) int {
	return count
}
`
	p := writeTemp(t, "a.go", src)
	if _, err := renameSymbol([]string{p}, "count", "total", "file", false, false); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, p)
	if strings.Contains(got, "count") {
		t.Errorf("局部变量 count 残留（含参数）:\n%s", got)
	}
	if !strings.Contains(got, "var total int") || !strings.Contains(got, "func g(total int)") {
		t.Errorf("局部声明未重命名:\n%s", got)
	}
}

// TestRenameShadow：遮蔽——函数内局部声明遮蔽包级同名符号时，局部
// 作用域内引用不替换（Go 词法作用域：声明点起生效）。
func TestRenameShadow(t *testing.T) {
	src := `package m

var limit = 10

func f() {
	limit := 20 // 局部遮蔽
	_ = limit   // 局部，不替换
}

func g() int {
	return limit // 包级，替换
}
`
	p := writeTemp(t, "a.go", src)
	if _, err := renameSymbol([]string{p}, "limit", "maxLimit", "file", false, false); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, p)
	if !strings.Contains(got, "var maxLimit = 10") || !strings.Contains(got, "return maxLimit") {
		t.Errorf("包级声明/引用未重命名:\n%s", got)
	}
	if !strings.Contains(got, "limit := 20") || !strings.Contains(got, "_ = limit") {
		t.Errorf("遮蔽作用域内不应替换:\n%s", got)
	}
}

// TestRenameConflict：新名与同作用域已有声明冲突 → 报错不写文件。
func TestRenameConflict(t *testing.T) {
	src := `package m

func load() {}

func store() {}
`
	p := writeTemp(t, "a.go", src)
	_, err := renameSymbol([]string{p}, "load", "store", "file", false, false)
	if err == nil {
		t.Fatal("新名冲突应报错")
	}
	if !strings.Contains(err.Error(), "store") {
		t.Errorf("报错应含冲突名: %v", err)
	}
	if strings.Contains(readFile(t, p), "func store() {}") == false {
		t.Fatal("冲突报错不应写文件（load 不应被改）")
	}
}

// TestRenameDryRun：--dry-run 返回改动清单，不写文件。
func TestRenameDryRun(t *testing.T) {
	src := `package m

func load() { load() }
`
	p := writeTemp(t, "a.go", src)
	changed, err := renameSymbol([]string{p}, "load", "fetch", "file", false, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 1 {
		t.Fatalf("dry-run 应报告 1 个文件，got %v", changed)
	}
	if readFile(t, p) != src {
		t.Fatal("dry-run 不应写文件")
	}
}

// TestRenameScopeFile：--scope file 只改指定文件，不碰同目录其他文件。
func TestRenameScopeFile(t *testing.T) {
	dir := t.TempDir()
	p1 := filepath.Join(dir, "a.go")
	p2 := filepath.Join(dir, "b.go")
	os.WriteFile(p1, []byte("package m\n\nfunc load() {}\n"), 0o644)
	os.WriteFile(p2, []byte("package m\n\nfunc use() { load() }\n"), 0o644)
	if _, err := renameSymbol([]string{p1}, "load", "fetch", "file", false, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readFile(t, p1), "func fetch()") {
		t.Error("a.go 应重命名")
	}
	if !strings.Contains(readFile(t, p2), "load()") {
		t.Error("--scope file 不应修改 b.go")
	}
}

// TestRenameScopePkg：--scope pkg 同包全部文件（除 _test.go）。
func TestRenameScopePkg(t *testing.T) {
	dir := t.TempDir()
	p1 := filepath.Join(dir, "a.go")
	p2 := filepath.Join(dir, "b.go")
	pt := filepath.Join(dir, "a_test.go")
	os.WriteFile(p1, []byte("package m\n\nfunc load() {}\n"), 0o644)
	os.WriteFile(p2, []byte("package m\n\nfunc use() { load() }\n"), 0o644)
	os.WriteFile(pt, []byte("package m\n\nfunc TestX() { load() }\n"), 0o644)
	changed, err := renameSymbol([]string{p1}, "load", "fetch", "pkg", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 2 {
		t.Fatalf("--scope pkg 应改 2 个文件（不含 _test.go），got %v", changed)
	}
	if !strings.Contains(readFile(t, p2), "fetch()") {
		t.Error("b.go 应重命名")
	}
	if !strings.Contains(readFile(t, pt), "load()") {
		t.Error("默认不碰 _test.go")
	}
}
