package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
)

// Q238 --repo 注册表解析（Q14 四步：文件系统 → 路径后缀 → 目录名 →
// module 名；多命中报候选；未命中原样返回）。

func seedRegistryRefs(t *testing.T) {
	t.Helper()
	isolateRegistryDir(t)
	r, err := sqlite.OpenRegistry(registryDirFn())
	if err != nil {
		t.Fatalf("OpenRegistry: %v", err)
	}
	defer r.Close()
	for _, rp := range []sqlite.RegistryRepo{
		{Path: "/home/schaepher/Codes/go2o", Module: "github.com/xx/go2o", RegisteredAt: "t"},
		{Path: "/home/schaepher/Codes/ana", Module: "github.com/schaepher/codeintel", RegisteredAt: "t"},
		{Path: "/ws/ana-feature", Module: "github.com/schaepher/codeintel", IsWorktree: true,
			WorktreeOf: "/home/schaepher/Codes/ana", RegisteredAt: "t"},
	} {
		if err := r.RegisterRepo(rp); err != nil {
			t.Fatal(err)
		}
	}
}

// TestResolveRepoRefFilesystemFirst：文件系统存在 → 原样返回（路径优先）。
func TestResolveRepoRefFilesystemFirst(t *testing.T) {
	seedRegistryRefs(t)
	dir := t.TempDir() // 真实存在的目录
	if got := ResolveRepoRef(dir); got != dir {
		t.Errorf("存在路径应原样返回，got %q", got)
	}
	// 存在路径与注册表短名同名时，路径优先（chdir 使相对路径 go2o 存在）
	parent := t.TempDir()
	dup := filepath.Join(parent, "go2o")
	if err := os.MkdirAll(dup, 0o755); err != nil {
		t.Fatal(err)
	}
	old, _ := os.Getwd()
	defer os.Chdir(old)
	if err := os.Chdir(parent); err != nil {
		t.Fatal(err)
	}
	if got := ResolveRepoRef("go2o"); got != "go2o" {
		t.Errorf("路径存在应优先于注册表短名（原样返回相对路径），got %q", got)
	}
}

// TestResolveRepoRefDirName：目录名匹配（唯一）。
func TestResolveRepoRefDirName(t *testing.T) {
	seedRegistryRefs(t)
	if got := ResolveRepoRef("go2o"); got != "/home/schaepher/Codes/go2o" {
		t.Errorf("目录名 go2o → %q, want /home/schaepher/Codes/go2o", got)
	}
}

// TestResolveRepoRefPathSuffix：绝对路径后缀匹配（/Codes/go2o → 完整路径）。
func TestResolveRepoRefPathSuffix(t *testing.T) {
	seedRegistryRefs(t)
	if got := ResolveRepoRef("/Codes/go2o"); got != "/home/schaepher/Codes/go2o" {
		t.Errorf("后缀 /Codes/go2o → %q, want /home/schaepher/Codes/go2o", got)
	}
}

// TestResolveRepoRefModule：module 名精确匹配。
func TestResolveRepoRefModule(t *testing.T) {
	seedRegistryRefs(t)
	if got := ResolveRepoRef("github.com/xx/go2o"); got != "/home/schaepher/Codes/go2o" {
		t.Errorf("module → %q, want /home/schaepher/Codes/go2o", got)
	}
}

// TestResolveRepoRefMultiple：多命中（module 相同的主仓库 + worktree）
// → 打印候选、返回空（不静默取第一个）。
func TestResolveRepoRefMultiple(t *testing.T) {
	seedRegistryRefs(t)
	out := captureStderr(func() {
		if got := ResolveRepoRef("github.com/schaepher/codeintel"); got != "" {
			t.Errorf("多命中应返回空，got %q", got)
		}
	})
	if !strings.Contains(out, "ana") || !strings.Contains(out, "ana-feature") {
		t.Errorf("多命中应打印候选列表:\n%s", out)
	}
}

// TestResolveRepoRefNotFound：未命中 → 原样返回（调用方报原路径错误）。
func TestResolveRepoRefNotFound(t *testing.T) {
	seedRegistryRefs(t)
	if got := ResolveRepoRef("/no/such/path"); got != "/no/such/path" {
		t.Errorf("未命中应原样返回，got %q", got)
	}
}

// TestPrintRepoHint：注册表非空 → 引导提示；空注册表 → 无提示。
func TestPrintRepoHint(t *testing.T) {
	isolateRegistryDir(t)
	out := captureStderr(printRepoHint)
	if strings.Contains(out, "已注册") {
		t.Errorf("空注册表不应有引导:\n%s", out)
	}
	seedRegistryRefs(t)
	out = captureStderr(printRepoHint)
	for _, want := range []string{"已注册", "codeintel list", "--repo"} {
		if !strings.Contains(out, want) {
			t.Errorf("引导缺 %q:\n%s", want, out)
		}
	}
}
