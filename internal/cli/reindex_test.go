package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCmdReindexNoRepoDefaultsToCwd：Q237——reindex 缺 --repo 不再报错，
// 默认当前工作目录（chdir 到有 go.mod 的 fixture 后缺省跑，应命中 fixture
// 而非报 --repo is required exit 2）。
func TestCmdReindexNoRepoDefaultsToCwd(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "go.mod"), "module example.com/m\n\ngo 1.21\n")
	writeTestFile(t, filepath.Join(dir, "main.go"), "package main\n\nfunc main() {}\n")
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(func() {
		if code := cmdReindex(context.Background(), []string{}); code == 2 {
			t.Errorf("reindex 缺 --repo 不应再报 --repo is required（exit 2）")
		}
	})
	if !strings.Contains(out, "重建索引: "+dir) {
		t.Errorf("reindex 缺 --repo 应默认当前目录（输出 %q，期望含 %q）", out, "重建索引: "+dir)
	}
}

// TestCmdReindexKeepsDBFile：reindex 不删数据库文件（配置表入库后
// 仍保留）——cmdReindex 只打印提示后走 cmdInit（FullBuild 清图数据
// 表，文件与 build_metadata 保留）。
func TestCmdReindexKeepsDBFile(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "go.mod"), "module example.com/m\n\ngo 1.21\n")
	idx := filepath.Join(dir, ".codeintel")
	if err := os.MkdirAll(idx, 0o755); err != nil {
		t.Fatal(err)
	}
	// 模拟已有库文件（含将来配置表所在库）
	if err := os.WriteFile(filepath.Join(idx, "codeintel.db"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	// cmdReindex 到 cmdInit 的 go.work 检查路径（无 scip-go 的轻量失败点）——
	// 断言：删除逻辑不再存在（库文件不应因 reindex 被删）
	if code := cmdReindex(nil, []string{"--repo", dir}); code == 0 {
		t.Log("reindex 全量构建成功（scip-go 可用场景）")
	}
	if _, err := os.Stat(filepath.Join(idx, "codeintel.db")); err != nil {
		t.Fatalf("reindex 不应删除库文件（配置表保留）: %v", err)
	}
}
