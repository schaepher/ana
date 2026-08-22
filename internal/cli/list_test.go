package cli

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
)

// Q238 codeintel list：台账输出（短名/路径/module/状态/worktree 归属/
// workspace）+ 过滤（--worktree-of/--module/--stale/--unbuilt）+ --json。
// 状态机：已构建（head 一致）/过期（HEAD 变）/未构建（build_id 空）/
// [missing]（目录不存在）。

// seedListFixture 注册三形态条目：已构建（真实 git 仓库）、未构建、
// missing（目录不存在）。
func seedListFixture(t *testing.T) (builtDir string) {
	t.Helper()
	isolateRegistryDir(t)
	r, err := sqlite.OpenRegistry(registryDirFn())
	if err != nil {
		t.Fatalf("OpenRegistry: %v", err)
	}
	defer r.Close()
	// 已构建：真实 git 仓库（HEAD 可对比）
	if !gitAvailable() {
		t.Skip("git 不可用")
	}
	builtDir = t.TempDir()
	runGit(t, builtDir, "init", "-b", "main", ".")
	runGit(t, builtDir, "config", "user.email", "t@t")
	runGit(t, builtDir, "config", "user.name", "t")
	runGit(t, builtDir, "commit", "--allow-empty", "-m", "init")
	head := gitHead(builtDir)
	if err := r.RegisterRepo(sqlite.RegistryRepo{Path: builtDir, Module: "example.com/built",
		HeadCommit: head, BuildID: head, LastBuiltAt: "t", RegisteredAt: "t"}); err != nil {
		t.Fatal(err)
	}
	// 未构建：真实目录但 build_id 空
	unbuilt := t.TempDir()
	if err := r.RegisterRepo(sqlite.RegistryRepo{Path: unbuilt, Module: "example.com/unbuilt",
		RegisteredAt: "t"}); err != nil {
		t.Fatal(err)
	}
	// missing：目录不存在
	if err := r.RegisterRepo(sqlite.RegistryRepo{Path: "/no/such/dir/missing", Module: "example.com/missing",
		HeadCommit: "deadbeef", BuildID: "deadbeef", RegisteredAt: "t"}); err != nil {
		t.Fatal(err)
	}
	return builtDir
}

// TestListBasic：默认输出含短名/路径/module/状态/worktree 归属列。
func TestListBasic(t *testing.T) {
	seedListFixture(t)
	out := captureStdout(func() {
		if code := cmdList([]string{}); code != 0 {
			t.Errorf("list exit = %d", code)
		}
	})
	for _, want := range []string{"built", "unbuilt", "missing", "example.com/built", "已构建", "未构建", "[missing]"} {
		if !strings.Contains(out, want) {
			t.Errorf("list 输出缺 %q:\n%s", want, out)
		}
	}
}

// TestListStaleUnbuilt：--stale 只筛过期；--unbuilt 筛未构建。
func TestListStaleUnbuilt(t *testing.T) {
	built := seedListFixture(t)
	// 新 commit 使 built 过期
	runGit(t, built, "commit", "--allow-empty", "-m", "second")
	out := captureStdout(func() {
		if code := cmdList([]string{"--stale"}); code != 0 {
			t.Errorf("list --stale exit = %d", code)
		}
	})
	if !strings.Contains(out, "过期") || strings.Contains(out, "未构建") || strings.Contains(out, "已构建") {
		t.Errorf("--stale 应只含过期 built:\n%s", out)
	}
	out = captureStdout(func() {
		if code := cmdList([]string{"--unbuilt"}); code != 0 {
			t.Errorf("list --unbuilt exit = %d", code)
		}
	})
	if !strings.Contains(out, "未构建") || strings.Contains(out, "已构建") {
		t.Errorf("--unbuilt 应只含未构建:\n%s", out)
	}
}

// TestListFilterModule：--module 片段过滤。
func TestListFilterModule(t *testing.T) {
	seedListFixture(t)
	out := captureStdout(func() {
		if code := cmdList([]string{"--module", "example.com/built"}); code != 0 {
			t.Errorf("list --module exit = %d", code)
		}
	})
	if !strings.Contains(out, "example.com/built") || strings.Contains(out, "example.com/unbuilt") {
		t.Errorf("--module example.com/built 应只含 built:\n%s", out)
	}
}

// TestListJSON：--json 可解析（数组含 path/module/status 字段）。
func TestListJSON(t *testing.T) {
	seedListFixture(t)
	out := captureStdout(func() {
		if code := cmdList([]string{"--json"}); code != 0 {
			t.Errorf("list --json exit = %d", code)
		}
	})
	var arr []map[string]any
	if err := json.Unmarshal([]byte(out), &arr); err != nil {
		t.Fatalf("json: %v\n%s", err, out)
	}
	if len(arr) != 3 {
		t.Errorf("json 条数 = %d, want 3", len(arr))
	}
	statuses := map[string]bool{}
	for _, m := range arr {
		statuses[m["status"].(string)] = true
		if _, ok := m["path"]; !ok {
			t.Errorf("json 条目缺 path: %v", m)
		}
	}
	if !statuses["已构建"] || !statuses["未构建"] || !statuses["[missing]"] {
		t.Errorf("json 状态覆盖不全: %v", statuses)
	}
}

// TestListEmptyRegistry：空注册表提示。
func TestListEmptyRegistry(t *testing.T) {
	isolateRegistryDir(t)
	out := captureStdout(func() {
		if code := cmdList([]string{}); code != 0 {
			t.Errorf("list exit = %d", code)
		}
	})
	if !strings.Contains(out, "没有已注册的仓库") {
		t.Errorf("空台账提示:\n%s", out)
	}
}

// TestListWorktreeOf：--worktree-of 过滤 worktree 条目。
func TestListWorktreeOf(t *testing.T) {
	isolateRegistryDir(t)
	r, err := sqlite.OpenRegistry(registryDirFn())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	main := "/home/schaepher/Codes/ana"
	wt := "/ws/ana-feature"
	_ = r.RegisterRepo(sqlite.RegistryRepo{Path: main, Module: "m", RegisteredAt: "t"})
	_ = r.RegisterRepo(sqlite.RegistryRepo{Path: wt, Module: "m", IsWorktree: true,
		WorktreeOf: main, RegisteredAt: "t"})
	out := captureStdout(func() {
		if code := cmdList([]string{"--worktree-of", main}); code != 0 {
			t.Errorf("list --worktree-of exit = %d", code)
		}
	})
	if !strings.Contains(out, wt) || strings.Contains(out, main) {
		t.Errorf("--worktree-of 应只含 worktree 条目:\n%s", out)
	}
	// 目录不存在 → 都标 missing（--worktree-of 过滤在状态标记前）
	if !strings.Contains(out, "[missing]") {
		t.Errorf("不存在的目录应标 [missing]:\n%s", out)
	}
	// 目录名作为 worktree 目标（Q6 短名兼容）
	out2 := captureStdout(func() {
		if code := cmdList([]string{"--worktree-of", filepath.Base(main)}); code != 0 {
			t.Errorf("list --worktree-of 短名 exit = %d", code)
		}
	})
	if !strings.Contains(out2, wt) {
		t.Errorf("--worktree-of 短名应命中:\n%s", out2)
	}
}

// TestListWorktreeOfMain：worktree 条目显示归属（⊢ 主仓库短名）。
func TestListWorktreeOfMain(t *testing.T) {
	isolateRegistryDir(t)
	r, err := sqlite.OpenRegistry(registryDirFn())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	main := t.TempDir() + "/ana" // 不存在但路径可显示
	wt := main + "-feature"
	_ = r.RegisterRepo(sqlite.RegistryRepo{Path: main, Module: "m", RegisteredAt: "t"})
	_ = r.RegisterRepo(sqlite.RegistryRepo{Path: wt, Module: "m", IsWorktree: true,
		WorktreeOf: main, RegisteredAt: "t"})
	out := captureStdout(func() {
		if code := cmdList([]string{}); code != 0 {
			t.Errorf("list exit = %d", code)
		}
	})
	if !strings.Contains(out, "⊢ ana") {
		t.Errorf("worktree 条目应显示归属 ⊢ ana:\n%s", out)
	}
}
