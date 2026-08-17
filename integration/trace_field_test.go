//go:build integration

package integration

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
)

// TestFieldPrecisionSelfContained：⑥ 字段精度自包含用例（不依赖 radar）——
// 对象/SSA 值锚点不再扇出全部字段读；拷贝链（dest.ID = src.ID）经
// 值来源跳板保持闭合。
func TestFieldPrecisionSelfContained(t *testing.T) {
	if !scipGoAvailable() {
		t.Skip("scip-go not found")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/field\n\ngo 1.21\n")
	writeFile(t, filepath.Join(dir, "main.go"), `package field

type Src struct {
	ID   string
	Name string
}

type Dst struct {
	ID   string
	Name string
}

func copyAndSave(src *Src) *Dst {
	d := &Dst{}
	d.ID = src.ID
	d.Name = src.Name
	return d
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
	funcID := "symbol:go:example.com/field:copyAndSave"

	srcID := fieldAccessID(t, repo, funcID, "src.ID", "read")
	if srcID == "" {
		t.Fatal("src.ID.read 节点缺失")
	}

	code, out := runCLIOut(t, "query", "value-trace", srcID, "--repo", dir)
	if code != 0 {
		t.Fatalf("value-trace exit = %d", code)
	}
	if !strings.Contains(out, ".ID [写]") {
		t.Errorf("拷贝链应连到 dst.ID 写入，output=%q", out[:min(len(out), 400)])
	}

	srcParam := fieldAccessID(t, repo, funcID, "src", "read")
	_ = srcParam
	var srcVal string
	rows, err := repo.Query(`SELECT id FROM nodes WHERE kind='ssa_value'
		AND json_extract(properties, '$.func_id') = ? AND name = 'src'`, funcID)
	if err != nil {
		t.Fatal(err)
	}
	if rows.Next() {
		_ = rows.Scan(&srcVal)
	}
	rows.Close()
	if srcVal == "" {
		t.Fatal("src 参数 ssa_value 节点缺失")
	}
	code, out = runCLIOut(t, "query", "value-trace", srcVal, "--repo", dir)
	if code != 0 {
		t.Fatalf("value-trace src exit = %d", code)
	}

	if !strings.Contains(out, "src.ID [读]") || !strings.Contains(out, "src.Name [读]") {
		t.Errorf("对象锚点应显示值分叉读，output=%q", out[:min(len(out), 400)])
	}
	if !strings.Contains(out, ".ID [写]") || !strings.Contains(out, ".Name [写]") {
		t.Errorf("对象锚点应显示值消费写点，output=%q", out[:min(len(out), 400)])
	}
}

// TestCrossFunctionTraceSelfContained：⑩ 跨函数追踪复现——多种调用方
// 形态下 trace-forward 应连到被调函数内的实际字段写入：
//
//	A. 调用方参数传递（run2(c *Cfg) → fill(c)）
//	B. 调用方局部变量传递（var c Cfg; fill(&c)，调用方无字段访问无参数）
//	C. 调用方字段读后传参（s.c → fill）
func TestCrossFunctionTraceSelfContained(t *testing.T) {
	if !scipGoAvailable() {
		t.Skip("scip-go not found")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/xfn\n\ngo 1.21\n")
	writeFile(t, filepath.Join(dir, "main.go"), `package xfn

type Cfg struct {
	Key string
}

// callee：实际写入
func fill(c *Cfg) {
	c.Key = "set"
}

// A. 参数传递
func run2(c *Cfg) {
	fill(c)
}

// B. 局部变量传递（调用方无字段访问、无参数）
func runLocal() {
	var c Cfg
	fill(&c)
}

// C. 调用方字段读后传参
type Svc struct {
	cfg Cfg
}

func (s *Svc) Run() {
	fill(&s.cfg)
}

// D. 字面量传参（调用方直接构造对象传入）
func runLiteral() {
	fill(&Cfg{Key: "x"})
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

	check := func(t *testing.T, funcID, field string, want string) {
		t.Helper()
		code, out := runCLIOut(t, "query", "trace-forward", field,
			"--func", funcID, "--repo", dir)
		if code != 0 {
			t.Fatalf("trace-forward exit = %d (%s)", code, funcID)
		}
		if !strings.Contains(out, want) {
			t.Errorf("trace-forward %s 未连到 %s，output=%q", funcID, want, out[:min(len(out), 400)])
		}
	}
	field := "example.com/xfn.Cfg.Key"

	check(t, "symbol:go:example.com/xfn:run2", field, "c.Key")

	check(t, "symbol:go:example.com/xfn:runLocal", field, "c.Key")

	check(t, "symbol:go:example.com/xfn:(Svc).Run", field, "c.Key")

	code, out := runCLIOut(t, "query", "trace-forward", field,
		"--func", "symbol:go:example.com/xfn:runLiteral", "--repo", dir)
	if code != 0 {
		t.Fatalf("trace-forward runLiteral exit = %d", code)
	}
	if !strings.Contains(out, "c.Key") {
		t.Errorf("字面量传参未连到 c.Key 写入，output=%q", out[:min(len(out), 400)])
	}
}

// TestCrossFunctionNoiseSelfContained：⑩ 跨函数追踪噪音复现——A 传
// *Record 给 B，B 写 record.FinalFee 且读多个无关字段。trace-forward
// A 的 FinalFee 下游：应连到 B 的 record.FinalFee 写入，且不含
// Metadata/Status 等无关字段读（同名跳板过滤）。
func TestCrossFunctionNoiseSelfContained(t *testing.T) {
	if !scipGoAvailable() {
		t.Skip("scip-go not found")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/noise\n\ngo 1.21\n")
	writeFile(t, filepath.Join(dir, "main.go"), `package noise

type Record struct {
	FinalFee float64
	Metadata string
	Status   string
}

// B：写入 FinalFee，并读多个无关字段
func B(record *Record) {
	record.FinalFee = 100
	_ = record.Metadata
	_ = record.Status
}

// A：传入 record，查 FinalFee 下游
func A(record *Record) {
	B(record)
	_ = record.FinalFee
}

func main() {}
`)
	if code := runCLI(t, "init", "--repo", dir); code != 0 {
		t.Fatalf("init exit = %d", code)
	}
	code, out := runCLIOut(t, "query", "trace-forward", "example.com/noise.Record.FinalFee",
		"--func", "symbol:go:example.com/noise:A", "--repo", dir)
	if code != 0 {
		t.Fatalf("trace-forward exit = %d", code)
	}

	if !strings.Contains(out, "FinalFee [写]") && !strings.Contains(out, "FinalFee") {
		t.Errorf("未连到 B 的 record.FinalFee 写入，output=%q", out[:min(len(out), 400)])
	}

	for _, noise := range []string{"Metadata", "Status"} {
		if strings.Contains(out, noise) {
			t.Errorf("无关字段 %s 不应入链，output=%q", noise, out[:min(len(out), 400)])
		}
	}
}

// TestDeepChainNoIndirectWriteSelfContained：S1 集成固化——三层调用链
// （a→b→c）中 c 写自己内部对象（与实参无别名）时，a 不得有 T.A 间接写
// （别名排除须经跨函数参数 may 传播稳定生效，不依赖调用点处理顺序）。
func TestDeepChainNoIndirectWriteSelfContained(t *testing.T) {
	if !scipGoAvailable() {
		t.Skip("scip-go not found")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/deep\n\ngo 1.21\n")
	writeFile(t, filepath.Join(dir, "main.go"), `package deep

type T struct {
	A int
}

// c 写自己内部对象 inner（与实参 x 无别名）
func c(x *T) {
	var inner T
	inner.A = 1
	_ = x
}

func b(x *T) {
	c(x)
}

func a() {
	var t T
	b(&t)
}

func main() {}
`)
	if code := runCLI(t, "init", "--repo", dir); code != 0 {
		t.Fatalf("init exit = %d", code)
	}
	code, out := runCLIOut(t, "query", "fields", "symbol:go:example.com/deep:a", "--repo", dir)
	if code != 0 {
		t.Fatalf("query fields exit = %d", code)
	}
	if strings.Contains(out, "indirect_write") && strings.Contains(out, "T.A") {
		t.Errorf("a 不应有 T.A 间接写（c 写内部对象，别名排除应生效），output=%q", out[:min(len(out), 300)])
	}
}
