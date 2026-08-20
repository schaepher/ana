package ssa

import (
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

// TestLoadValueTraceSelfContained：举一反三——Load 值起点（rec := *ptr
// 解引用赋值后传参）。
func TestLoadValueTraceSelfContained(t *testing.T) {
	repo := indexFixtureRepo(t, map[string]string{
		"go.mod": "module example.com/mtest\n\ngo 1.21\n",
		"main.go": `package ld

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
`,
	})
	rows, err := repo.TraceForward("example.com/mtest.Record.FinalFee", domain.CanonicalID("symbol:go:example.com/mtest:run7"), 8)
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if !traceHas(rows, "r.FinalFee") {
		t.Errorf("Load 值传参未连到 helper4 写入，output=%v", rows)
	}
}

// TestValueTraceContainerBoundarySelfContained：Q163 集成固化——从
// Payment 分支写点（SettledFee.write）追踪，默认（候选边剪枝）不出现
// RefundSource 实现；显式 --min-conf 0 时经候选 returns 边可达且标注
// 候选（路径累计）。
func TestValueTraceContainerBoundarySelfContained(t *testing.T) {
	repo := indexFixtureRepo(t, map[string]string{
		"go.mod": "module example.com/mtest\n\ngo 1.21\n",
		"main.go": `package vtbound

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
`,
	})
	funcID := "symbol:go:example.com/mtest:(PaymentSource).Calculate"
	writeID := fieldAccessID(t, repo, funcID, "inv.SettledFee", "write")
	if writeID == "" {
		t.Fatal("SettledFee.write 节点缺失")
	}
	rows, err := repo.GetValueTrace(domain.CanonicalID(writeID), 8, 1.0, false)
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if traceHas(rows, "RefundImpl") {
		t.Errorf("默认模式不应出现 RefundSource 实现（候选边越界）:\n%v", rows)
	}
	rows, err = repo.GetValueTrace(domain.CanonicalID(writeID), 8, 0, false)
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if !traceHas(rows, "RefundImpl") {
		t.Errorf("--min-conf 0 后应可达 RefundImpl:\n%v", rows)
	}
	if !vtCandHas(rows) {
		t.Errorf("--min-conf 0 后应标注动态候选:\n%v", rows)
	}
}

// TestClosureWriteInSummarySelfContained：继续查——闭包内写入应计入
// 外层函数的字段摘要（direct_write，funcData 归外层）。
func TestClosureWriteInSummarySelfContained(t *testing.T) {
	repo := indexFixtureRepo(t, map[string]string{
		"go.mod": "module example.com/mtest\n\ngo 1.21\n",
		"main.go": `package cs

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
`,
	})
	ffsRows, err := repo.GetFunctionFields(domain.CanonicalID("symbol:go:example.com/mtest:runA"))
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if !ffsHas(ffsRows, "example.com/mtest.Record.FinalFee") {
		t.Errorf("闭包内写入未计入外层函数摘要，output=%v", ffsRows)
	}
}
