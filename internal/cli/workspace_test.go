package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
)

// Q238 workspace：基于注册表驱动创建 git worktree（Q5/Q9/Q10）——
// 幂等（已有 worktree 跳过）、--repo 子集、--build 构建（默认不构建）、
// --branch 覆盖、单仓库失败继续汇总、注册 worktree_of + workspace 归属。

// seedWorkspaceMain 注册一个真实 git 主仓库（含 go.mod）。
func seedWorkspaceMain(t *testing.T) (main string) {
	t.Helper()
	isolateRegistryDir(t)
	if !gitAvailable() {
		t.Skip("git 不可用")
	}
	main = t.TempDir()
	runGit(t, main, "init", "-b", "main", ".")
	runGit(t, main, "config", "user.email", "t@t")
	runGit(t, main, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(main, "go.mod"), []byte("module example.com/wt\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(main, "main.go"), "package main\n\nfunc main() {}\n")
	runGit(t, main, "add", ".")
	runGit(t, main, "commit", "-m", "init")
	// 注册主仓库（init 语义）
	r, err := sqlite.OpenRegistry(registryDirFn())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := r.RegisterRepo(sqlite.RegistryRepo{Path: main, Module: "example.com/wt",
		HeadCommit: gitHead(main), BuildID: gitHead(main), RegisteredAt: "t"}); err != nil {
		t.Fatal(err)
	}
	return main
}

// TestWorkspaceInitBasic：workspace init → worktree 创建 + 注册
// （worktree_of + workspace 归属 + 未构建状态）。
func TestWorkspaceInitBasic(t *testing.T) {
	main := seedWorkspaceMain(t)
	ws := t.TempDir()
	out := captureStdout(func() {
		if code := cmdWorkspace([]string{"init", "--dir", ws}); code != 0 {
			t.Errorf("workspace init exit = %d", code)
		}
	})
	wtDir := filepath.Join(ws, filepath.Base(main))
	if fi, err := os.Stat(wtDir); err != nil || !fi.IsDir() {
		t.Fatalf("worktree 目录未创建: %v（输出: %s）", err, out)
	}
	if !strings.Contains(out, "已创建") || !strings.Contains(out, filepath.Base(main)) {
		t.Errorf("汇总输出缺信息:\n%s", out)
	}
	r, err := sqlite.OpenRegistry(registryDirFn())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	rp, ok, err := r.FindRepo(wtDir)
	if err != nil || !ok {
		t.Fatalf("worktree 条目未注册: ok=%v err=%v", ok, err)
	}
	if !rp.IsWorktree || rp.WorktreeOf != main || rp.Workspace != ws {
		t.Errorf("worktree 条目归属错误: %+v", rp)
	}
	if rp.BuildID != "" {
		t.Errorf("默认不应构建（Q9c）: build_id=%q", rp.BuildID)
	}
}

// TestWorkspaceInitIdempotent：重跑跳过（不重复建、不报错）。
func TestWorkspaceInitIdempotent(t *testing.T) {
	main := seedWorkspaceMain(t)
	ws := t.TempDir()
	if code := cmdWorkspace([]string{"init", "--dir", ws}); code != 0 {
		t.Fatalf("首次 init exit = %d", code)
	}
	out := captureStdout(func() {
		if code := cmdWorkspace([]string{"init", "--dir", ws}); code != 0 {
			t.Errorf("重跑 init exit = %d", code)
		}
	})
	if !strings.Contains(out, "跳过") {
		t.Errorf("重跑应显示跳过:\n%s", out)
	}
	// worktree 目录不重复（git 同一分支不能建两个同名 worktree——目录已存在即幂等）
	if _, err := os.Stat(filepath.Join(ws, filepath.Base(main))); err != nil {
		t.Errorf("worktree 目录应保持: %v", err)
	}
}

// TestWorkspaceInitSubset：--repo 子集（只处理指定的仓库）。
func TestWorkspaceInitSubset(t *testing.T) {
	main := seedWorkspaceMain(t)
	// 第二个主仓库（不应被处理）
	other := t.TempDir()
	runGit(t, other, "init", "-b", "main", ".")
	runGit(t, other, "config", "user.email", "t@t")
	runGit(t, other, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(other, "go.mod"), []byte("module example.com/other\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, other, "add", ".")
	runGit(t, other, "commit", "-m", "init")
	r, _ := sqlite.OpenRegistry(registryDirFn())
	defer r.Close()
	_ = r.RegisterRepo(sqlite.RegistryRepo{Path: other, Module: "example.com/other",
		HeadCommit: gitHead(other), BuildID: gitHead(other), RegisteredAt: "t"})

	ws := t.TempDir()
	if code := cmdWorkspace([]string{"init", "--dir", ws, "--repo", filepath.Base(main)}); code != 0 {
		t.Fatalf("workspace init 子集 exit = %d", code)
	}
	if _, err := os.Stat(filepath.Join(ws, filepath.Base(main))); err != nil {
		t.Errorf("子集应创建 main 的 worktree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws, filepath.Base(other))); err == nil {
		t.Errorf("子集不应创建 other 的 worktree")
	}
}

// TestWorkspaceInitBranch：--branch 覆盖；默认当前分支。
func TestWorkspaceInitBranch(t *testing.T) {
	main := seedWorkspaceMain(t)
	ws := t.TempDir()
	if code := cmdWorkspace([]string{"init", "--dir", ws, "--branch", "feature"}); code != 0 {
		t.Fatalf("workspace init --branch exit = %d", code)
	}
	wtDir := filepath.Join(ws, filepath.Base(main))
	// worktree 检出分支应为 feature
	out, err := os.ReadFile(filepath.Join(wtDir, ".git", "HEAD"))
	if err != nil {
		// worktree 的 HEAD 在 gitdir 指向的目录
		gd, _ := os.ReadFile(filepath.Join(wtDir, ".git"))
		head := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(gd)), "gitdir:"))
		hb, err := os.ReadFile(filepath.Join(head, "HEAD"))
		if err != nil {
			t.Fatalf("读 worktree HEAD: %v", err)
		}
		out = hb
	}
	if !strings.Contains(string(out), "feature") {
		t.Errorf("worktree 分支应为 feature，got %q", out)
	}
}

// TestWorkspaceInitFailureContinues：单仓库失败（无 git 主仓库条目）不中断，
// 汇总报告失败。
func TestWorkspaceInitFailureContinues(t *testing.T) {
	isolateRegistryDir(t)
	r, _ := sqlite.OpenRegistry(registryDirFn())
	defer r.Close()
	// 注册一个「主仓库」但其目录不存在（worktree add 会失败）
	_ = r.RegisterRepo(sqlite.RegistryRepo{Path: "/no/such/main", Module: "m", RegisteredAt: "t"})
	ws := t.TempDir()
	out := captureStdout(func() {
		if code := cmdWorkspace([]string{"init", "--dir", ws}); code == 0 {
			t.Errorf("有失败应 exit 非零（单失败继续但不静默）")
		}
	})
	if !strings.Contains(out, "失败") {
		t.Errorf("汇总应报告失败:\n%s", out)
	}
}

// TestWorkspacePrune：目录消失的条目被清理（worktree 与主仓库）。
func TestWorkspacePrune(t *testing.T) {
	main := seedWorkspaceMain(t)
	ws := t.TempDir()
	if code := cmdWorkspace([]string{"init", "--dir", ws}); code != 0 {
		t.Fatalf("init exit = %d", code)
	}
	// 删除 worktree 目录（模拟 git worktree remove）
	if err := os.RemoveAll(filepath.Join(ws, filepath.Base(main))); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(func() {
		if code := cmdWorkspace([]string{"prune"}); code != 0 {
			t.Errorf("prune exit = %d", code)
		}
	})
	if !strings.Contains(out, "已清理") {
		t.Errorf("prune 应报告清理:\n%s", out)
	}
	r, _ := sqlite.OpenRegistry(registryDirFn())
	defer r.Close()
	n, _ := r.CountRepos()
	if n != 1 {
		t.Errorf("prune 后应剩主仓库 1 条，got %d", n)
	}
}
