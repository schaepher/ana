package action

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

// writeSrc 写测试源码文件，返回路径。
func writeSrc(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "src.go")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestExtractConditionIf(t *testing.T) {
	p := writeSrc(t, `package main

func f(a int) {
	if a > 0 {
		x := a
	}
}
`)
	// if 头行与分支体内行 → 条件文本
	if c := ExtractCondition(p, 4); c != "a > 0" {
		t.Errorf("line 4 cond = %q, want %q", c, "a > 0")
	}
	if c := ExtractCondition(p, 5); c != "a > 0" {
		t.Errorf("line 5 cond = %q, want %q", c, "a > 0")
	}
	// 分支外行（func 头、闭合大括号）→ 空
	if c := ExtractCondition(p, 3); c != "" {
		t.Errorf("line 3 cond = %q, want empty", c)
	}
	if c := ExtractCondition(p, 6); c != "" {
		t.Errorf("line 6 cond = %q, want empty", c)
	}
}

func TestExtractConditionNestedIf(t *testing.T) {
	p := writeSrc(t, `package main

func f(a, b int) {
	if a > 0 {
		if b < 0 {
			x := a
		}
	}
}
`)
	// 最内层条件
	if c := ExtractCondition(p, 5); c != "b < 0" {
		t.Errorf("nested cond = %q, want %q", c, "b < 0")
	}
	// 仅外层分支内 → 外层条件
	if c := ExtractCondition(p, 4); c != "a > 0" {
		t.Errorf("outer cond = %q, want %q", c, "a > 0")
	}
}

// TestExtractConditionElseBranch：else 分支行的条件不是 if 条件
// （是否定），标注为条件文本会误导——保持空。
func TestExtractConditionElseBranch(t *testing.T) {
	p := writeSrc(t, `package main

func f(a int) {
	if a > 0 {
		x := a
	} else {
		y := a
	}
}
`)
	if c := ExtractCondition(p, 7); c != "" {
		t.Errorf("else branch cond = %q, want empty（else 分支不标注 if 条件）", c)
	}
	// else-if 链：内层 if 的分支正常标注
	p2 := writeSrc(t, `package main

func f(a int) {
	if a > 0 {
		x := a
	} else if a < 10 {
		y := a
	}
}
`)
	if c := ExtractCondition(p2, 7); c != "a < 10" {
		t.Errorf("else-if cond = %q, want %q", c, "a < 10")
	}
	// else-if 的外层 if 体仍标注外层条件
	if c := ExtractCondition(p2, 5); c != "a > 0" {
		t.Errorf("outer if cond = %q, want %q", c, "a > 0")
	}
}

func TestExtractConditionTypeSwitch(t *testing.T) {
	p := writeSrc(t, `package main

func f(v any) {
	switch v := v.(type) {
	case int:
		x := v
	case string:
		y := v
	}
}
`)
	if c := ExtractCondition(p, 5); c != "类型分支: int" {
		t.Errorf("case int cond = %q, want %q", c, "类型分支: int")
	}
	if c := ExtractCondition(p, 7); c != "类型分支: string" {
		t.Errorf("case string cond = %q, want %q", c, "类型分支: string")
	}
	// switch 外行 → 空
	if c := ExtractCondition(p, 9); c != "" {
		t.Errorf("line 9 cond = %q, want empty", c)
	}
}

func TestExtractConditionErrors(t *testing.T) {
	// 文件不存在
	if c := ExtractCondition(filepath.Join(t.TempDir(), "nope.go"), 1); c != "" {
		t.Errorf("missing file cond = %q, want empty", c)
	}
	// 非法 Go 文件
	p := writeSrc(t, "not go code {{{")
	if c := ExtractCondition(p, 1); c != "" {
		t.Errorf("invalid file cond = %q, want empty", c)
	}
}

// TestTraceConditions：查询期叠加——按行号解析源码 AST 标注条件。
// seedRepo 的 main 节点 LineStart=3；写入真实 main.go 使第 3 行在 if 分支内。
func TestTraceConditions(t *testing.T) {
	a, dir := seedRepo(t)
	mainPath := filepath.Join(dir, "main.go")
	if err := os.WriteFile(mainPath, []byte(`package main

func main() {
	if x > 1 {
		use(x)
	}
}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	mainID := domain.CanonicalID("symbol:go:example.com/m:main")

	rows := []*domain.TraceRow{
		{ID: mainID, Line: 4},  // if 分支内
		{ID: mainID, Line: 99}, // 分支外 → 无标注
		{ID: domain.CanonicalID("symbol:go:example.com/m:none")}, // 查不到节点
		{Line: 0}, // 无行号跳过
	}
	out, err := a.TraceConditions(rows)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != len(rows) {
		t.Fatalf("out len = %d, want %d", len(out), len(rows))
	}
	if len(out[0].Conditions) != 1 || out[0].Conditions[0] != "x > 1" {
		t.Errorf("row0 conditions = %+v, want [x > 1]", out[0].Conditions)
	}
	for i, wantEmpty := range map[int]bool{1: true, 2: true, 3: true} {
		if wantEmpty && len(out[i].Conditions) != 0 {
			t.Errorf("row%d conditions = %+v, want empty", i, out[i].Conditions)
		}
	}
	// 空输入原样返回
	if out, err := a.TraceConditions(nil); err != nil || out != nil {
		t.Errorf("nil input = %v, %v", out, err)
	}
}

// TestTraceConditionsDoesNotMutateInput：S5 回归——TraceConditions 不得修改
// 入参行（返回新切片 + 新行对象）；此前 out[i] = r 共享指针，原行的
// Conditions 字段被原地覆盖。
func TestTraceConditionsDoesNotMutateInput(t *testing.T) {
	a, dir := seedRepo(t)
	mainPath := filepath.Join(dir, "main.go")
	if err := os.WriteFile(mainPath, []byte(`package main

func main() {
	if x > 1 {
		use(x)
	}
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	mainID := domain.CanonicalID("symbol:go:example.com/m:main")
	rows := []*domain.TraceRow{
		{ID: mainID, Line: 4}, // if 分支内
	}
	out, err := a.TraceConditions(rows)
	if err != nil {
		t.Fatal(err)
	}
	// 入参行不得被修改（Conditions 保持 nil）
	if rows[0].Conditions != nil {
		t.Errorf("入参被修改: rows[0].Conditions = %+v", rows[0].Conditions)
	}
	// 返回行携带条件
	if len(out) != 1 || len(out[0].Conditions) != 1 || out[0].Conditions[0] != "x > 1" {
		t.Errorf("返回行条件 = %+v", out[0].Conditions)
	}
	// 返回行是新的对象（不是入参指针）
	if out[0] == rows[0] {
		t.Error("返回行与入参共享指针")
	}
}
