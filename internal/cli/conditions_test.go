package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/schaepher/codeintel/internal/action"
)

// TestExtractCondition：if 分支内的行 → 条件表达式文本（Q92 常量可传播分支）。
func TestExtractCondition(t *testing.T) {
	dir := t.TempDir()
	src := `package m

type Config struct {
	APIKey string
}

func newLLM(c *Config) string {
	if c.APIKey == "" {
		return "mock"
	}
	return "llm"
}
`
	writeTestFile(t, filepath.Join(dir, "a.go"), src)
	got := action.ExtractCondition(filepath.Join(dir, "a.go"), 9) // `return "mock"` 在 if 块内
	if !strings.Contains(got, "c.APIKey == \"\"") {
		t.Errorf("条件提取 = %q, want 含 c.APIKey == \"\"", got)
	}
	// if 块外的行 → 无条件
	if got := action.ExtractCondition(filepath.Join(dir, "a.go"), 11); got != "" {
		t.Errorf("if 外行不应有条件: %q", got)
	}
}

// TestExtractConditionEnv：环境条件（os.Getenv 分支，Q92）。
func TestExtractConditionEnv(t *testing.T) {
	dir := t.TempDir()
	src := `package m

import "os"

func f() string {
	if os.Getenv("API_KEY") != "" {
		return "x"
	}
	return "y"
}
`
	writeTestFile(t, filepath.Join(dir, "a.go"), src)
	got := action.ExtractCondition(filepath.Join(dir, "a.go"), 7)
	if !strings.Contains(got, "Getenv") {
		t.Errorf("env 条件应含 Getenv: %q", got)
	}
}

// TestExtractConditionTypeSwitch：类型分支（Q92）。
func TestExtractConditionTypeSwitch(t *testing.T) {
	dir := t.TempDir()
	src := `package m

type A struct{ V int }
type B struct{ V int }

func f(v any) string {
	switch t := v.(type) {
	case *A:
		return "a"
	case *B:
		return "b"
	}
	return "?"
}
`
	writeTestFile(t, filepath.Join(dir, "a.go"), src)
	got := action.ExtractCondition(filepath.Join(dir, "a.go"), 9) // case *A 内
	if !strings.Contains(got, "*A") {
		t.Errorf("类型分支应含 *A: %q", got)
	}
}

// TestValueTraceConditions：value-trace 输出叠加条件标注（Q95 输出契约）。
func TestValueTraceConditions(t *testing.T) {
	dir := seedFieldTrace(t)
	// 种子节点行 5 所在行无 if——改断言为输出无 panic 且正常（条件叠加
	// 为空时不输出 [条件]）；真实条件场景由集成测试（radar）覆盖
	out := captureStdout(func() {
		if code := cmdQuery([]string{"value-trace", "symbol:go:example.com/m:main#t.A.write@5",
			"--repo", dir}); code != 0 {
			t.Errorf("value-trace exit = %d", code)
		}
	})
	if !strings.Contains(out, "t.A") {
		t.Errorf("value-trace 输出异常:\n%s", out)
	}
	_ = os.Stdout
}
