package sqlite

import (
	"os"
	"path/filepath"
	"testing"
)

// Q238 全局注册表（~/.codeintel/codeintel.db）：repos 台账。
// 测试注入独立目录（不碰真实 home）。

// TestRegistryRegisterList：注册 → 列表 → 刷新 → 注销（含 worktree 级联）。
func TestRegistryRegisterList(t *testing.T) {
	dir := t.TempDir()
	r, err := OpenRegistry(dir)
	if err != nil {
		t.Fatalf("OpenRegistry: %v", err)
	}
	defer r.Close()

	main := RegistryRepo{Path: "/repos/ana", Module: "github.com/x/ana", GoModCount: 1,
		HeadCommit: "abc123", BuildID: "b1", LastBuiltAt: "2026-08-22T10:00:00Z", RegisteredAt: "t0"}
	if err := r.RegisterRepo(main); err != nil {
		t.Fatalf("RegisterRepo: %v", err)
	}
	wt := RegistryRepo{Path: "/ws/ana-feature", Module: "github.com/x/ana", IsWorktree: true,
		WorktreeOf: "/repos/ana", Workspace: "/ws", RegisteredAt: "t1"}
	if err := r.RegisterRepo(wt); err != nil {
		t.Fatalf("RegisterRepo worktree: %v", err)
	}

	repos, err := r.ListRepos()
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	if len(repos) != 2 {
		t.Fatalf("len(repos) = %d, want 2", len(repos))
	}
	// 刷新主仓库（update 语义：head/build 更新，registered 不变）
	if err := r.RefreshRepo("/repos/ana", "def456", "b2"); err != nil {
		t.Fatalf("RefreshRepo: %v", err)
	}
	repos, _ = r.ListRepos()
	for _, rp := range repos {
		if rp.Path == "/repos/ana" {
			if rp.HeadCommit != "def456" || rp.BuildID != "b2" {
				t.Errorf("刷新后 head/build 应更新: %+v", rp)
			}
			if rp.RegisteredAt != "t0" {
				t.Errorf("刷新不应改 registered_at: %q", rp.RegisteredAt)
			}
		}
	}
	// 注销主仓库 → 级联删 worktree 条目
	if err := r.UnregisterRepo("/repos/ana"); err != nil {
		t.Fatalf("UnregisterRepo: %v", err)
	}
	repos, _ = r.ListRepos()
	if len(repos) != 0 {
		t.Errorf("注销主仓库应级联删 worktree，剩余 %d 条: %+v", len(repos), repos)
	}
}

// TestRegistryAutoRecreate：库缺失时自动重建（Q12）——纯台账无副作用。
func TestRegistryAutoRecreate(t *testing.T) {
	dir := t.TempDir()
	r, err := OpenRegistry(dir)
	if err != nil {
		t.Fatalf("OpenRegistry: %v", err)
	}
	if err := r.RegisterRepo(RegistryRepo{Path: "/x", RegisteredAt: "t"}); err != nil {
		t.Fatal(err)
	}
	r.Close()
	// 删库模拟缺失 → 重新打开自动重建
	os.Remove(filepath.Join(dir, "codeintel.db"))
	r2, err := OpenRegistry(dir)
	if err != nil {
		t.Fatalf("重建 OpenRegistry: %v", err)
	}
	defer r2.Close()
	repos, _ := r2.ListRepos()
	if len(repos) != 0 {
		t.Errorf("重建后应为空台账，got %d", len(repos))
	}
	// 重建后可正常注册
	if err := r2.RegisterRepo(RegistryRepo{Path: "/y", RegisteredAt: "t2"}); err != nil {
		t.Fatalf("重建后注册: %v", err)
	}
}

// TestRegistrySchemaMigration：旧版表缺列 → 打开自动补列并保留数据（Q16
// 列变更自动重建迁移，不手动删库丢台账）。构造旧版表（缺 go_mod_count 列）。
func TestRegistrySchemaMigration(t *testing.T) {
	dir := t.TempDir()
	old := `CREATE TABLE repos (
		id INTEGER PRIMARY KEY,
		path TEXT NOT NULL UNIQUE,
		module TEXT,
		head_commit TEXT,
		build_id TEXT,
		last_built_at TEXT,
		is_worktree INTEGER NOT NULL DEFAULT 0,
		worktree_of TEXT,
		workspace TEXT,
		registered_at TEXT NOT NULL
	);
	INSERT INTO repos(path, module, registered_at) VALUES ('/old', 'm', 't0');`
	db, err := openRawSQLite(filepath.Join(dir, "codeintel.db"))
	if err != nil {
		t.Fatalf("openRawSQLite: %v", err)
	}
	if _, err := db.Exec(old); err != nil {
		t.Fatalf("造旧表: %v", err)
	}
	db.Close()

	r, err := OpenRegistry(dir)
	if err != nil {
		t.Fatalf("OpenRegistry 迁移: %v", err)
	}
	defer r.Close()
	repos, err := r.ListRepos()
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	if len(repos) != 1 || repos[0].Path != "/old" {
		t.Fatalf("迁移后应保留旧数据: %+v", repos)
	}
	if repos[0].Module != "m" || repos[0].GoModCount != 0 {
		t.Errorf("迁移后旧列值应保留、新列默认: %+v", repos[0])
	}
	// 二次打开幂等（不再迁移/报错）
	r.Close()
	r2, err := OpenRegistry(dir)
	if err != nil {
		t.Fatalf("二次 OpenRegistry: %v", err)
	}
	defer r2.Close()
	if n, _ := r2.CountRepos(); n != 1 {
		t.Errorf("二次打开后条数 = %d, want 1", n)
	}
}
