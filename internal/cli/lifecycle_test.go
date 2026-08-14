package cli

import (
	"strings"
	"testing"
)

// TestExportGraphLifecycle：export graph --type lifecycle 输出 mermaid
// flowchart（Q99：生命周期图聚合渲染）。
func TestExportGraphLifecycle(t *testing.T) {
	dir := seedFieldTrace(t)
	writeNode := "symbol:go:example.com/m:main#t.A.write@5"
	out := captureStdout(func() {
		if code := cmdExport([]string{"graph", "--type", "lifecycle", "--target", writeNode,
			"--repo", dir}); code != 0 {
			t.Errorf("export graph lifecycle exit = %d", code)
		}
	})
	if !strings.Contains(out, "flowchart") {
		t.Errorf("lifecycle 应输出 mermaid flowchart: %s", out[:min(len(out), 200)])
	}
	if !strings.Contains(out, "t.A") {
		t.Errorf("lifecycle 应含字段节点 t.A: %s", out[:min(len(out), 200)])
	}
	// 参数校验：缺 --target
	if code := cmdExport([]string{"graph", "--type", "lifecycle", "--repo", dir}); code == 0 {
		t.Error("lifecycle 缺 --target 应失败")
	}
}
