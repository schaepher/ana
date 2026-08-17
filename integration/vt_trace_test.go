//go:build integration

package integration

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
)

// TestLoadValueTraceSelfContained：举一反三——Load 值起点（rec := *ptr
// 解引用赋值后传参）。
func TestLoadValueTraceSelfContained(t *testing.T) {
	if !scipGoAvailable() {
		t.Skip("scip-go not found")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/ld\n\ngo 1.21\n")
	writeFile(t, filepath.Join(dir, "main.go"), `package ld

type Record struct {
	FinalFee float64
}

func helper4(r *Record) {
	r.FinalFee = 600
}

func run7() {
	ptr := &Record{}
	rec := *ptr
	helper4(&rec)
}

func main() {}
`)
	if code := runCLI(t, "init", "--repo", dir); code != 0 {
		t.Fatalf("init exit = %d", code)
	}
	code, out := runCLIOut(t, "query", "trace-forward", "example.com/ld.Record.FinalFee",
		"--func", "symbol:go:example.com/ld:run7", "--repo", dir)
	if code != 0 {
		t.Fatalf("trace-forward exit = %d", code)
	}
	if !strings.Contains(out, "r.FinalFee") {
		t.Errorf("Load 值传参未连到 helper4 写入，output=%q", out[:min(len(out), 400)])
	}
}

// TestValueTraceInterfaceSelfContained：继续查——value-trace 经接口
// argument 边进入候选实现（⑮ 只测了 trace-forward）。
func TestValueTraceInterfaceSelfContained(t *testing.T) {
	if !scipGoAvailable() {
		t.Skip("scip-go not found")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/vtif\n\ngo 1.21\n")
	writeFile(t, filepath.Join(dir, "main.go"), `package vtif

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

func runA() {
	var w Writer = &FileWriter{}
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
	// 锚点：runA 中 &Record{} 的 alloc 值（ssa_value，type=*Record）
	var allocID string
	rows, err := repo.Query(`SELECT id FROM nodes WHERE kind='ssa_value'
		AND json_extract(properties, '$.func_id') = 'symbol:go:example.com/vtif:runA'
		AND json_extract(properties, '$.type_string') = '*example.com/vtif.Record' LIMIT 1`)
	if err != nil {
		t.Fatal(err)
	}
	if rows.Next() {
		_ = rows.Scan(&allocID)
	}
	rows.Close()
	if allocID == "" {
		t.Fatalf("runA alloc 节点缺失")
	}
	code, out := runCLIOut(t, "query", "value-trace", allocID, "--repo", dir, "--min-conf", "0")
	if code != 0 {
		t.Fatalf("value-trace exit = %d", code)
	}
	if !strings.Contains(out, "(FileWriter).Write") || !strings.Contains(out, "r.FinalFee") {
		t.Errorf("value-trace 未经接口 argument 边进入候选实现，output=%q", out[:min(len(out), 400)])
	}
}

// TestValueTraceDedupSelfContained：Q155 集成固化——value-trace 递归
// CTE 按 (id, dir) 去重。phi 汇聚（x = phi(a, b)，两分支 alloc 汇入）：
// 从 FinalFee.write 反向，每个节点恰好一行、深度正确，两分支都出现。
func TestValueTraceDedupSelfContained(t *testing.T) {
	if !scipGoAvailable() {
		t.Skip("scip-go not found")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/vtdup\n\ngo 1.21\n")
	writeFile(t, filepath.Join(dir, "main.go"), `package vtdup

type Rec struct {
	FinalFee float64
}

// phi 汇聚：x = phi(a, b)，两分支 alloc 写入同一字段
func join(flag bool) {
	var x *Rec
	if flag {
		x = &Rec{}
	} else {
		x = &Rec{}
	}
	x.FinalFee = 5
}

func main() { join(true) }
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
	// x 被 SSA 寄存器化（t1.FinalFee）——instance_path 用 LIKE 匹配
	var writeID string
	if err := db.QueryRow(`SELECT id FROM nodes
		WHERE kind = 'field_access'
		  AND json_extract(properties, '$.func_id') = 'symbol:go:example.com/vtdup:join'
		  AND json_extract(properties, '$.instance_path') LIKE '%.FinalFee'
		  AND json_extract(properties, '$.access_kind') = 'write'
		LIMIT 1`).Scan(&writeID); err != nil {
		t.Fatalf("x.FinalFee.write 节点缺失: %v", err)
	}
	rows, err := repo.GetValueTrace(domain.CanonicalID(writeID), 8, 0, false)
	if err != nil {
		t.Fatalf("GetValueTrace: %v", err)
	}

	seen := map[string]int{}
	for _, row := range rows {
		key := string(row.ID) + "|" + string(rune('0'+row.Dir))
		seen[key]++
	}
	for key, n := range seen {
		if n != 1 {
			t.Errorf("节点重复: %s 出现 %d 次", key, n)
		}
	}

	depthOf := map[string]int{}
	for _, row := range rows {
		depthOf[string(row.ID)] = row.Depth
	}
	if d, ok := depthOf[writeID]; !ok || d != 0 {
		t.Errorf("锚点 write depth = %d, want 0", d)
	}
	countAt := func(d int) int {
		n := 0
		for _, v := range depthOf {
			if v == d {
				n++
			}
		}
		return n
	}
	if n := countAt(1); n != 2 {
		t.Errorf("depth1 节点数 = %d, want 2（phi 值 t1 + 常量 5）", n)
	}
	if n := countAt(2); n != 2 {
		t.Errorf("depth2 节点数 = %d, want 2（两分支 alloc 汇聚入 phi）", n)
	}

	phiSeen := false
	for _, row := range rows {
		if strings.Contains(string(row.ID), "#t1") {
			phiSeen = true
		}
	}
	if !phiSeen {
		t.Errorf("phi 汇聚值 t1 未出现在反向链")
	}
}

// TestValueTraceContainerBoundarySelfContained：Q163 集成固化——从
// Payment 分支写点（SettledFee.write）追踪，默认（候选边剪枝）不出现
// RefundSource 实现；显式 --min-conf 0 时经候选 returns 边可达且标注
// 候选（路径累计）。
func TestValueTraceContainerBoundarySelfContained(t *testing.T) {
	if !scipGoAvailable() {
		t.Skip("scip-go not found")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/vtbound\n\ngo 1.21\n")
	writeFile(t, filepath.Join(dir, "main.go"), `package vtbound

type Invoice struct {
	SettledFee int64
}

type FeeSource interface {
	Calculate(inv *Invoice)
}

type RefundSource interface {
	Build() *Invoice
}

type PaymentSource struct{}

func (p *PaymentSource) Calculate(inv *Invoice) {
	inv.SettledFee = 100
}

type RefundImpl struct{}

func (r *RefundImpl) Build() *Invoice {
	return &Invoice{}
}

// 容器 inv 来自 RefundSource 实现（候选 returns 边）——从 Payment
// 分支写点反向追踪经容器可到 RefundSource，Q163 默认应剪枝
func Process() {
	var rs RefundSource = &RefundImpl{}
	inv := rs.Build()
	var fs FeeSource = &PaymentSource{}
	fs.Calculate(inv)
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
	funcID := "symbol:go:example.com/vtbound:(PaymentSource).Calculate"
	writeID := fieldAccessID(t, repo, funcID, "inv.SettledFee", "write")
	if writeID == "" {
		t.Fatal("SettledFee.write 节点缺失")
	}

	code, out := runCLIOut(t, "query", "value-trace", writeID, "--repo", dir, "--max-depth", "8")
	if code != 0 {
		t.Fatalf("value-trace exit = %d", code)
	}
	if strings.Contains(out, "RefundImpl") {
		t.Errorf("默认模式不应出现 RefundSource 实现（候选边越界）:\n%s", out[:min(len(out), 400)])
	}

	code, out = runCLIOut(t, "query", "value-trace", writeID, "--repo", dir, "--max-depth", "8", "--min-conf", "0")
	if code != 0 {
		t.Fatalf("value-trace --min-conf exit = %d", code)
	}
	if !strings.Contains(out, "RefundImpl") {
		t.Errorf("--min-conf 0 后应可达 RefundImpl:\n%s", out[:min(len(out), 400)])
	}
	if !strings.Contains(out, "动态候选") {
		t.Errorf("--min-conf 0 后应标注动态候选:\n%s", out[:min(len(out), 400)])
	}
}
