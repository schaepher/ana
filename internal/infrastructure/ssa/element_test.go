package ssa

import (
	"strings"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

// findFieldByPath 按 full_path 查找字段/元素访问节点。
func findFieldByPath(t *testing.T, nodes []*domain.CodeEntity, funcID, fullPath string) *domain.CodeEntity {
	t.Helper()
	for _, n := range nodes {
		if n.Kind == domain.KindFieldAccess && n.Property("func_id") == funcID &&
			n.Property("full_path") == fullPath {
			return n
		}
	}
	t.Fatalf("field/element not found: func=%s full_path=%s", funcID, fullPath)
	return nil
}

func TestElementMapConstKey(t *testing.T) {
	// m["a"] 读写：full_path 带引号（Q1/Q5）；常量 key 敏感
	nodes, facts := indexFixture(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

func f() {
	m := map[string]int{}
	m["a"] = 1
	_ = m["a"]
}
`,
	})
	funcID := "symbol:go:example.com/mtest:f"
	w := findFieldByPath(t, nodes, funcID, `m["a"]`)
	if w.Property("access_kind") != "write" {
		t.Errorf("write access = %q", w.Property("access_kind"))
	}
	r := findFieldAccess(t, nodes, funcID, `m["a"]`, "read")
	if w.ID == r.ID {
		t.Errorf("read/write 应独立节点")
	}
	// 数据流：写值 → 写节点；读节点 → 结果
	if out := factsFrom(facts, string(r.ID)); len(out) != 1 || out[0].Kind != domain.FactDataFlowsTo {
		t.Errorf("read element edges = %+v", out)
	}
}

func TestElementSliceIndex(t *testing.T) {
	// s[0] 写读（IndexAddr+Store / Lookup）
	nodes, _ := indexFixture(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

func f() {
	s := make([]int, 3)
	s[0] = 1
	_ = s[0]
}
`,
	})
	funcID := "symbol:go:example.com/mtest:f"
	findFieldByPath(t, nodes, funcID, `s[0]`) // write
	findFieldAccess(t, nodes, funcID, `s[0]`, "read")
}

func TestElementVariableKeyFallback(t *testing.T) {
	// 变量 key → [key] 回退容器级（Q1）
	nodes, _ := indexFixture(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

func f(k string) {
	m := map[string]int{}
	m[k] = 1
}
`,
	})
	findFieldByPath(t, nodes, "symbol:go:example.com/mtest:f", `m[key]`)
}

func TestElementRangeAndChan(t *testing.T) {
	// range 迭代 = 读（[*]）；channel 收发 = 写/读元素（[send]/[recv]）
	nodes, _ := indexFixture(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

func f(m map[string]int, ch chan int) {
	for k, v := range m {
		_ = k
		_ = v
	}
	ch <- 1
	_ = <-ch
	for v := range ch {
		_ = v
	}
}
`,
	})
	funcID := "symbol:go:example.com/mtest:f"
	findFieldByPath(t, nodes, funcID, `m[*]`)
	w := findFieldByPath(t, nodes, funcID, `ch[send]`)
	if w.Property("access_kind") != "write" {
		t.Errorf("send access = %q, want write", w.Property("access_kind"))
	}
	r := findFieldByPath(t, nodes, funcID, `ch[recv]`)
	if r.Property("access_kind") != "read" {
		t.Errorf("recv access = %q, want read", r.Property("access_kind"))
	}
	// range channel 在 SSA 中降级为接收循环（UnOp ARROW），同样产出 [recv]
	findFieldByPath(t, nodes, funcID, `ch[recv]`)
}

func TestElementNamedContainer(t *testing.T) {
	// named map 容器：full_path 用类型限定路径 + 元素记号（Q5）
	nodes, _ := indexFixture(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

type M map[string]int

func f() {
	m := M{}
	m["a"] = 1
}
`,
	})
	findFieldByPath(t, nodes, "symbol:go:example.com/mtest:f", `example.com/mtest.M["a"]`)
}

func TestElementFieldContainer(t *testing.T) {
	// 容器是结构体字段：full_path = 字段路径 + 元素记号
	nodes, _ := indexFixture(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

type T struct {
	M map[string]int
}

func f(t *T) {
	t.M["a"] = 1
}
`,
	})
	findFieldByPath(t, nodes, "symbol:go:example.com/mtest:f", `example.com/mtest.T.M["a"]`)
}

func TestIndirectWriteCallSite(t *testing.T) {
	// 调用点级回连（Q90）：run 调 fillParam 写实参 c.Key——INDIRECT_WRITE
	// 边 metadata 携带调用点行号与实参变量名（run:10 fillParam(c)）
	_, facts, _ := indexFixtureFull(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

type Cfg struct {
	Key string
}

func fillParam(c *Cfg) {
	c.Key = "x"
}

func run(c *Cfg) {
	fillParam(c)
}
`,
	})
	runID := "symbol:go:example.com/mtest:run"
	fillID := "symbol:go:example.com/mtest:fillParam"
	for _, f := range facts {
		if f.Kind != domain.FactIndirectWrite || string(f.SourceID) != runID || string(f.TargetID) != fillID {
			continue
		}
		// 内存 facts 的 metadata 为 int；DB 反序列化为 float64
		var line int
		switch v := f.Metadata["call_line"].(type) {
		case float64:
			line = int(v)
		case int:
			line = v
		}
		if line != 12 {
			t.Errorf("INDIRECT_WRITE call_line = %v, want 12（run 调 fillParam 的行）", f.Metadata["call_line"])
		}
		args, _ := f.Metadata["call_args"].(string)
		if !strings.Contains(args, "c") {
			t.Errorf("INDIRECT_WRITE call_args = %q, want 含实参 c", args)
		}
		return
	}
	t.Fatal("INDIRECT_WRITE 边缺失（run → fillParam）")
}

func TestElementIndirectWrite(t *testing.T) {
	// 元素间接写（Q7a-② 别名命中）：fillM 写实参容器元素 → 调用者间接写
	_, _, summaries := indexFixtureFull(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

type M map[string]int

func fillM(m M) {
	m["a"] = 1
}

func run() {
	m := M{}
	fillM(m)
}
`,
	})
	findSummary(t, summaries, "symbol:go:example.com/mtest:run",
		domain.SummaryIndirectWrite, `example.com/mtest.M["a"]`)
}
