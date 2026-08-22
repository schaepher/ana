package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
)

// Q238 注册钩子：cmdInit 成功 → 全局台账注册（路径/module/worktree 归属）；
// cmdClean 成功 → 注销。测试经 TestMain 注入注册表目录（不碰真实 home）。

func openTestRegistry(t *testing.T) *sqlite.Registry {
	t.Helper()
	r, err := sqlite.OpenRegistry(registryDirFn())
	if err != nil {
		t.Fatalf("OpenRegistry: %v", err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

// isolateRegistryDir 把注册表目录指向独立临时目录（本测试内生效）——
// cli 包其他测试（cmdInit 缺省等）也会注册，须隔离避免互相污染。
func isolateRegistryDir(t *testing.T) {
	t.Helper()
	old := registryDirFn
	dir := t.TempDir()
	registryDirFn = func() string { return dir }
	t.Cleanup(func() { registryDirFn = old })
}

// TestInitRegistersGlobal：cmdInit 成功（fixture 仓库）→ 全局台账出现条目，
// 含路径与 module；重复 init 不报错（UPSERT）。
func TestInitRegistersGlobal(t *testing.T) {
	isolateRegistryDir(t)
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "go.mod"), "module example.com/m\n\ngo 1.21\n")
	writeTestFile(t, filepath.Join(dir, "main.go"), "package main\n\nfunc main() {}\n")
	if code := cmdInit(context.Background(), []string{"--repo", dir}); code != 0 {
		t.Fatalf("cmdInit exit = %d", code)
	}
	r := openTestRegistry(t)
	repos, err := r.ListRepos()
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 {
		t.Fatalf("init 后台账应 1 条，got %d: %+v", len(repos), repos)
	}
	rp := repos[0]
	if rp.Path != dir {
		t.Errorf("注册路径 = %q, want %q", rp.Path, dir)
	}
	if rp.Module != "example.com/m" {
		t.Errorf("注册 module = %q, want example.com/m", rp.Module)
	}
	if rp.IsWorktree {
		t.Errorf("普通仓库不应标记 worktree")
	}
	// 重复 init（UPSERT 不报错、不新增）
	if code := cmdInit(context.Background(), []string{"--repo", dir}); code != 0 {
		t.Fatalf("重复 init exit = %d", code)
	}
	repos, _ = r.ListRepos()
	if len(repos) != 1 {
		t.Errorf("重复 init 后条数 = %d, want 1", len(repos))
	}
}

// TestCleanUnregistersGlobal：cmdClean 成功 → 台账注销。
func TestCleanUnregistersGlobal(t *testing.T) {
	isolateRegistryDir(t)
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "go.mod"), "module example.com/m\n\ngo 1.21\n")
	writeTestFile(t, filepath.Join(dir, "main.go"), "package main\n\nfunc main() {}\n")
	if code := cmdInit(context.Background(), []string{"--repo", dir}); code != 0 {
		t.Fatalf("cmdInit exit = %d", code)
	}
	r := openTestRegistry(t)
	if n, _ := r.CountRepos(); n != 1 {
		t.Fatalf("init 后应 1 条，got %d", n)
	}
	if code := cmdClean([]string{"--repo", dir, "--force"}); code != 0 {
		t.Fatalf("cmdClean exit = %d", code)
	}
	if n, _ := r.CountRepos(); n != 0 {
		t.Errorf("clean 后台账应空，got %d", n)
	}
}

// TestInitFailureNoRegister：init 失败（无 go.mod）不注册（Q3：失败不注册）。
func TestInitFailureNoRegister(t *testing.T) {
	isolateRegistryDir(t)
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "main.go"), "package main\n\nfunc main() {}\n")
	if code := cmdInit(context.Background(), []string{"--repo", dir}); code == 0 {
		t.Fatal("无 go.mod 的 init 应失败")
	}
	r := openTestRegistry(t)
	if n, _ := r.CountRepos(); n != 0 {
		t.Errorf("失败 init 不应注册，got %d 条", n)
	}
}

// TestRegistryDirIsolation：注册表目录确实被注入到临时目录（不写 home）。
func TestRegistryDirIsolation(t *testing.T) {
	if registryDirFn() == "" {
		t.Fatal("registryDirFn 不应为空")
	}
	home, _ := os.UserHomeDir()
	if registryDirFn() == filepath.Join(home, ".codeintel") {
		t.Error("测试期间 registryDirFn 应指向临时目录（TestMain 注入）")
	}
}
