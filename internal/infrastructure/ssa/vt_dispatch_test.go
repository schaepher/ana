package ssa

import (
	"strings"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

// TestValueTraceInterfaceSelfContained：继续查——value-trace 经接口
// argument 边进入候选实现（⑮ 只测了 trace-forward）。
func TestValueTraceInterfaceSelfContained(t *testing.T) {
	repo := indexFixtureRepo(t, map[string]string{
		"go.mod": "module example.com/mtest\n\ngo 1.21\n",
		"main.go": `package vtif

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
`,
	})
	// 锚点：runA 中 &Record{} 的 alloc 值（ssa_value，type=*Record）
	var allocID string
	rows, err := repo.Query(`SELECT id FROM nodes WHERE kind='ssa_value'
			AND json_extract(properties, '$.func_id') = 'symbol:go:example.com/mtest:runA'
			AND json_extract(properties, '$.type_string') = '*example.com/mtest.Record' LIMIT 1`)
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
	vrows, err := repo.GetValueTrace(domain.CanonicalID(allocID), 8, 0, false)
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if !traceHas(vrows, "(FileWriter).Write") || !traceHas(vrows, "r.FinalFee") {
		t.Errorf("value-trace 未经接口 argument 边进入候选实现，output=%v", vrows)
	}
}

// TestValueTraceDedupSelfContained：Q155 集成固化——value-trace 递归
// CTE 按 (id, dir) 去重。phi 汇聚（x = phi(a, b)，两分支 alloc 汇入）：
// 从 FinalFee.write 反向，每个节点恰好一行、深度正确，两分支都出现。
func TestValueTraceDedupSelfContained(t *testing.T) {
	repo := indexFixtureRepo(t, map[string]string{
		"go.mod": "module example.com/mtest\n\ngo 1.21\n",
		"main.go": `package vtdup

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
`,
	})
	// x 被 SSA 寄存器化（t1.FinalFee）——instance_path 用 LIKE 匹配
	var writeID string
	if err := repo.QueryRow(`SELECT id FROM nodes
			WHERE kind = 'field_access'
			  AND json_extract(properties, '$.func_id') = 'symbol:go:example.com/mtest:join'
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

// TestInterfaceDispatchIndirectWriteSelfContained：Q154 集成固化——接口
// 动态分派候选实现内的字段写回传为 wrapper/上游调用方的 indirect_write
// （实现 → wrapper → 上游逐层传播，INDIRECT_WRITE 边指向每个候选实现）。
func TestInterfaceDispatchIndirectWriteSelfContained(t *testing.T) {
	repo := indexFixtureRepo(t, map[string]string{
		"go.mod": "module example.com/mtest\n\ngo 1.21\n",
		"main.go": `package dw

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
`,
	})
	for _, impl := range []string{"(StdCalc).Calculate", "(ExpCalc).Calculate"} {
		var n int
		if err := repo.QueryRow(`SELECT COUNT(*) FROM function_field_summary
				WHERE function_id = ? AND access_kind = 'direct_write'
				  AND field_path = 'example.com/mtest.Order.FinalFee'`,
			"symbol:go:example.com/mtest:"+impl).Scan(&n); err != nil {
			t.Fatalf("direct_write 查询: %v", err)
		}
		if n != 1 {
			t.Errorf("%s direct_write FinalFee = %d, want 1", impl, n)
		}
	}
	for _, fn := range []string{"Process", "Run"} {
		var n int
		if err := repo.QueryRow(`SELECT COUNT(*) FROM function_field_summary
				WHERE function_id = ? AND access_kind = 'indirect_write'
				  AND field_path = 'example.com/mtest.Order.FinalFee'`,
			"symbol:go:example.com/mtest:"+fn).Scan(&n); err != nil {
			t.Fatalf("indirect_write 查询: %v", err)
		}
		if n != 1 {
			t.Errorf("%s indirect_write FinalFee = %d, want 1（动态分派回传）", fn, n)
		}
	}
	for _, impl := range []string{"(StdCalc).Calculate", "(ExpCalc).Calculate"} {
		var n int
		if err := repo.QueryRow(`SELECT COUNT(*) FROM edges
				WHERE source_id = 'symbol:go:example.com/mtest:Process'
				  AND target_id = ? AND kind = 'indirect_write'`,
			"symbol:go:example.com/mtest:"+impl).Scan(&n); err != nil {
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
	repo := indexFixtureRepo(t, map[string]string{
		"go.mod": "module example.com/mtest\n\ngo 1.21\n",
		"main.go": `package dyncand

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
`,
	})
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
		if strings.Contains(tgt, "(FileWriter).Write") && strings.Contains(meta, "mtest.Writer") {
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
	vrows, err := repo.GetValueTrace(domain.CanonicalID(anchor), 8, 0, false)
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if !vtCandHas(vrows) {
		t.Errorf("value-trace 未标注动态候选边:\n%v", vrows)
	}
}
