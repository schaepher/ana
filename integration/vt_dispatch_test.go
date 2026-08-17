//go:build integration

package integration

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
)

// TestClosureWriteInSummarySelfContained：继续查——闭包内写入应计入
// 外层函数的字段摘要（direct_write，funcData 归外层）。
func TestClosureWriteInSummarySelfContained(t *testing.T) {
	if !scipGoAvailable() {
		t.Skip("scip-go not found")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/cs\n\ngo 1.21\n")
	writeFile(t, filepath.Join(dir, "main.go"), `package cs

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

func main() {}
`)
	if code := runCLI(t, "init", "--repo", dir); code != 0 {
		t.Fatalf("init exit = %d", code)
	}
	code, out := runCLIOut(t, "query", "fields", "symbol:go:example.com/cs:runA", "--repo", dir)
	if code != 0 {
		t.Fatalf("fields exit = %d", code)
	}
	if !strings.Contains(out, "example.com/cs.Record.FinalFee") {
		t.Errorf("闭包内写入未计入外层函数摘要，output=%q", out[:min(len(out), 300)])
	}
}

// TestInterfaceDispatchIndirectWriteSelfContained：Q154 集成固化——接口
// 动态分派候选实现内的字段写回传为 wrapper/上游调用方的 indirect_write
// （实现 → wrapper → 上游逐层传播，INDIRECT_WRITE 边指向每个候选实现）。
func TestInterfaceDispatchIndirectWriteSelfContained(t *testing.T) {
	if !scipGoAvailable() {
		t.Skip("scip-go not found")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/dw\n\ngo 1.21\n")
	writeFile(t, filepath.Join(dir, "main.go"), `package dw

type Order struct {
	FinalFee int
}

type FeeCalculator interface {
	Calculate(o *Order)
}

type StdCalc struct{}

func (c *StdCalc) Calculate(o *Order) { o.FinalFee = 100 }

type ExpCalc struct{}

func (c *ExpCalc) Calculate(o *Order) { o.FinalFee = 200 }

// wrapper：经接口调用分派（动态 invoke，无静态 callee）
func Process(fc FeeCalculator, o *Order) {
	fc.Calculate(o)
}

// 上游：静态调用 wrapper，间接写闭包传播到
func Run() {
	Process(&StdCalc{}, &Order{})
}

func main() { Run() }
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

	for _, impl := range []string{"(StdCalc).Calculate", "(ExpCalc).Calculate"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM function_field_summary
			WHERE function_id = ? AND access_kind = 'direct_write'
			  AND field_path = 'example.com/dw.Order.FinalFee'`,
			"symbol:go:example.com/dw:"+impl).Scan(&n); err != nil {
			t.Fatalf("direct_write 查询: %v", err)
		}
		if n != 1 {
			t.Errorf("%s direct_write FinalFee = %d, want 1", impl, n)
		}
	}

	for _, fn := range []string{"Process", "Run"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM function_field_summary
			WHERE function_id = ? AND access_kind = 'indirect_write'
			  AND field_path = 'example.com/dw.Order.FinalFee'`,
			"symbol:go:example.com/dw:"+fn).Scan(&n); err != nil {
			t.Fatalf("indirect_write 查询: %v", err)
		}
		if n != 1 {
			t.Errorf("%s indirect_write FinalFee = %d, want 1（动态分派回传）", fn, n)
		}
	}

	for _, impl := range []string{"(StdCalc).Calculate", "(ExpCalc).Calculate"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM edges
			WHERE source_id = 'symbol:go:example.com/dw:Process'
			  AND target_id = ? AND kind = 'indirect_write'`,
			"symbol:go:example.com/dw:"+impl).Scan(&n); err != nil {
			t.Fatalf("indirect_write 边查询: %v", err)
		}
		if n != 1 {
			t.Errorf("INDIRECT_WRITE 边 Process → %s = %d, want 1", impl, n)
		}
	}
	_ = repo
}

// TestDispatchCandidateMetaSelfContained：Q161 集成固化——动态接口调用
// 的 argument 边携带候选元数据（interface/candidate_origin/confidence，
// 注册点命中 register 0.9），value-trace 标注且 --min-conf 可剪枝。
func TestDispatchCandidateMetaSelfContained(t *testing.T) {
	if !scipGoAvailable() {
		t.Skip("scip-go not found")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/dyncand\n\ngo 1.21\n")
	writeFile(t, filepath.Join(dir, "main.go"), `package dyncand

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
	var w Writer = &FileWriter{} // 注册点（MakeInterface）
	w.Write(&Record{})
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
	rows, err := repo.Query(`SELECT source_id, target_id, metadata FROM edges
		WHERE kind = 'argument' AND json_extract(metadata, '$.candidate_origin') = 'register'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	metaOK := false
	for rows.Next() {
		var src, tgt, meta string
		if err := rows.Scan(&src, &tgt, &meta); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(tgt, "(FileWriter).Write") && strings.Contains(meta, "dyncand.Writer") {
			metaOK = true
		}
	}
	if !metaOK {
		t.Error("动态 argument 边缺候选元数据（interface/candidate_origin）")
	}

	rows.Close()
	var anchor string
	r2, err := repo.Query(`SELECT target_id FROM edges
		WHERE kind = 'argument' AND json_extract(metadata, '$.candidate_origin') = 'register' LIMIT 1`)
	if err != nil {
		t.Fatal(err)
	}
	for r2.Next() {
		if err := r2.Scan(&anchor); err != nil {
			t.Fatal(err)
		}
	}
	r2.Close()
	if anchor == "" {
		t.Fatal("无 register 候选 argument 边")
	}
	code, out := runCLIOut(t, "query", "value-trace", anchor, "--repo", dir, "--min-conf", "0")
	if code != 0 {
		t.Fatalf("value-trace exit = %d", code)
	}
	if !strings.Contains(out, "动态候选") {
		t.Errorf("value-trace 未标注动态候选边:\n%s", out[:min(len(out), 400)])
	}
}
