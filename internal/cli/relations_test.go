package cli

import (
	"testing"
)

// TestRelationsMermaidDeterministic：mermaid 输出确定性（Q243）——
// 列/子图顺序不得随 map 遍历随机。连续 10 次输出必须一致。
func TestRelationsMermaidDeterministic(t *testing.T) {
	dir := seedTablePathFixture(t)
	run := func() string {
		return captureStdout(func() {
			if code := cmdQuery([]string{"relations", "table_a", "--repo", dir, "--format", "mermaid"}); code != 0 {
				t.Errorf("relations exit = %d", code)
			}
		})
	}
	first := run()
	for i := 0; i < 10; i++ {
		if out := run(); out != first {
			t.Fatalf("单表 mermaid 输出不稳定（第 %d 次与首次不同）", i)
		}
	}
}

// TestRelationsAllMermaidDeterministic：--all mermaid 同样确定性。
func TestRelationsAllMermaidDeterministic(t *testing.T) {
	dir := seedTablePathFixture(t)
	run := func() string {
		return captureStdout(func() {
			if code := cmdQuery([]string{"relations", "--all", "--repo", dir, "--format", "mermaid"}); code != 0 {
				t.Errorf("relations --all exit = %d", code)
			}
		})
	}
	first := run()
	for i := 0; i < 10; i++ {
		if out := run(); out != first {
			t.Fatalf("--all mermaid 输出不稳定（第 %d 次与首次不同）", i)
		}
	}
}
