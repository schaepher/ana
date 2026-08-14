package cli

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitRun 在指定目录执行 git（注入 user 配置供 commit 使用）。
func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestDetectChangedGoFiles(t *testing.T) {
	dir := t.TempDir()
	gitRun(t, dir, "init", "-q")
	// 未跟踪新文件
	writeTestFile(t, filepath.Join(dir, "a.go"), "package a\n")
	changed, err := detectChangedGoFiles(dir)
	if err != nil || len(changed) != 1 || changed[0] != "a.go" {
		t.Fatalf("untracked detect = %v, %v", changed, err)
	}
	// 提交后修改：diff HEAD 检测
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "init")
	writeTestFile(t, filepath.Join(dir, "a.go"), "package a\n\nfunc F() {}\n")
	changed, err = detectChangedGoFiles(dir)
	if err != nil || len(changed) != 1 || changed[0] != "a.go" {
		t.Fatalf("modified detect = %v, %v", changed, err)
	}
	// 删除文件：diff HEAD 仍列出
	if err := os.Remove(filepath.Join(dir, "a.go")); err != nil {
		t.Fatal(err)
	}
	changed, err = detectChangedGoFiles(dir)
	if err != nil || len(changed) != 1 || changed[0] != "a.go" {
		t.Fatalf("deleted detect = %v, %v", changed, err)
	}
	// 非 .go 文件不纳入
	writeTestFile(t, filepath.Join(dir, "notes.md"), "x")
	changed, _ = detectChangedGoFiles(dir)
	for _, f := range changed {
		if strings.HasSuffix(f, ".md") {
			t.Errorf("非 go 文件不应纳入: %v", changed)
		}
	}
	// 无变更 → 空
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "cleanup")
	changed, err = detectChangedGoFiles(dir)
	if err != nil || len(changed) != 0 {
		t.Fatalf("clean detect = %v, %v", changed, err)
	}
}

// TestCmdUpdateNoChanges：update 成功路径的轻量分支——git 仓库无变更
// .go 文件时直接提示已最新（exit 0），不触发索引构建（无 scip-go 依赖）。
func TestCmdUpdateNoChanges(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "go.mod"), "module example.com/m\n\ngo 1.21\n")
	writeTestFile(t, filepath.Join(dir, "a.go"), "package a\n")
	gitRun(t, dir, "init", "-q")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "init")

	out := captureStdout(func() {
		if code := cmdUpdate(nil, []string{"--repo", dir}); code != 0 {
			t.Errorf("update no-change exit = %d", code)
		}
	})
	if !strings.Contains(out, "无变更") {
		t.Errorf("no-change stdout = %q, want 无变更提示", out)
	}
}

// TestCmdUpdateGoModChanged：go.mod 变更影响 module 范围——提示全量 init
// 而非增量（exit 1）。
func TestCmdUpdateGoModChanged(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "go.mod"), "module example.com/m\n\ngo 1.21\n")
	gitRun(t, dir, "init", "-q")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "init")
	writeTestFile(t, filepath.Join(dir, "go.mod"), "module example.com/m\n\ngo 1.21\n\n// bump\n")

	old := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	code := cmdUpdate(nil, []string{"--repo", dir})
	w.Close()
	os.Stderr = old
	var buf strings.Builder
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}
	if code != 1 || !strings.Contains(buf.String(), "codeintel init") {
		t.Errorf("go.mod changed = %d, stderr=%q（应提示全量 init）", code, buf.String())
	}
}

func TestCmdUpdateNoGit(t *testing.T) {
	// 非 git 仓库：增量更新报错提示
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "go.mod"), "module example.com/m\n\ngo 1.21\n")
	old := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	code := cmdUpdate(nil, []string{"--repo", dir})
	w.Close()
	os.Stderr = old
	var buf strings.Builder
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}
	if code != 1 || !strings.Contains(buf.String(), "git") {
		t.Errorf("update without git = %d, stderr=%q", code, buf.String())
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
