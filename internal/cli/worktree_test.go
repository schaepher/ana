package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Q238 worktree 检测：.git 目录 = 主仓库；.git 为 gitdir 指针文件 =
// worktree（解析出主仓库）；无 .git = 非 git 仓库（注册仍可，主仓库空）。
// 用真实 git 构造 worktree fixture（codeintel update 本就依赖 git）。

// gitAvailable 检查 git 命令可用（跳过不可用环境）。
func gitAvailable() bool {
	_, err := exec.LookPath("git")
	return err == nil
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1", "HOME="+t.TempDir(), // 隔离用户全局配置
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// TestDetectWorktree：主仓库 / worktree / 非 git 三形态。
func TestDetectWorktree(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git 不可用")
	}
	root := t.TempDir()
	main := filepath.Join(root, "main")
	wtDir := filepath.Join(root, "wt")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, main, "init", "-b", "main", ".")
	runGit(t, main, "config", "user.email", "t@t")
	runGit(t, main, "config", "user.name", "t")
	runGit(t, main, "commit", "--allow-empty", "-m", "init")
	runGit(t, main, "worktree", "add", wtDir, "-b", "feat")

	// 主仓库：.git 是目录
	ok, of := detectWorktree(main)
	if ok || of != "" {
		t.Errorf("主仓库 detectWorktree = (%v, %q), want (false, \"\")", ok, of)
	}
	// worktree：.git 是 gitdir 指针文件 → 主仓库
	ok, of = detectWorktree(wtDir)
	if !ok {
		t.Errorf("worktree 应检测为 is_worktree=true")
	}
	if of != main {
		t.Errorf("worktree 主仓库 = %q, want %q", of, main)
	}
	// 非 git 目录
	plain := filepath.Join(root, "plain")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	ok, of = detectWorktree(plain)
	if ok || of != "" {
		t.Errorf("非 git 目录 detectWorktree = (%v, %q), want (false, \"\")", ok, of)
	}
}

// TestDetectWorktreeGitDirPointer：手工构造 gitdir 指针文件（不依赖
// git worktree add 的目录名约定）。
func TestDetectWorktreeGitDirPointer(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git 不可用")
	}
	root := t.TempDir()
	main := filepath.Join(root, "main")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, main, "init", "-b", "main", ".")
	runGit(t, main, "config", "user.email", "t@t")
	runGit(t, main, "config", "user.name", "t")
	runGit(t, main, "commit", "--allow-empty", "-m", "init")

	// 手工造 worktree 形态：.git 文件内容 gitdir: <main>/.git/worktrees/foo
	wtDir := filepath.Join(root, "custom-wt")
	if err := os.MkdirAll(filepath.Join(wtDir), 0o755); err != nil {
		t.Fatal(err)
	}
	gitdir := filepath.Join(main, ".git", "worktrees", "foo")
	if err := os.MkdirAll(gitdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wtDir, ".git"),
		[]byte("gitdir: "+gitdir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ok, of := detectWorktree(wtDir)
	if !ok || of != main {
		t.Errorf("gitdir 指针形态 detectWorktree = (%v, %q), want (true, %q)", ok, of, main)
	}
}
