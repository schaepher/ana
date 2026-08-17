package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
)

// TestDiscoverModules：P2-3——递归扫描 go.mod（根在前）；跳过
// .git/.codeintel/vendor；module 目录内不再嵌套扫描。
func TestDiscoverModules(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/root\n\ngo 1.21\n")
	writeFile(t, filepath.Join(dir, "app", "go.mod"), "module example.com/app\n\ngo 1.21\n")
	writeFile(t, filepath.Join(dir, "vendor", "go.mod"), "module vendor.example\n")
	writeFile(t, filepath.Join(dir, ".codeintel", "go.mod"), "module hidden.example\n")
	writeFile(t, filepath.Join(dir, "lib", "sub", "go.mod"), "module example.com/lib\n\ngo 1.21\n")

	writeFile(t, filepath.Join(dir, "app", "inner", "go.mod"), "module example.com/appinner\n")

	modules, dirs, err := DiscoverModules(dir)
	if err != nil {
		t.Fatalf("DiscoverModules: %v", err)
	}
	want := []string{"example.com/root", "example.com/app", "example.com/lib"}
	if len(modules) != len(want) {
		t.Fatalf("modules = %v, want %v", modules, want)
	}
	for i := range want {
		if modules[i] != want[i] {
			t.Fatalf("modules[%d] = %s, want %s", i, modules[i], want[i])
		}
	}
	wantDirs := []string{".", "app", "lib/sub"}
	for i, d := range dirs {
		if d != wantDirs[i] {
			t.Errorf("dirs[%d] = %s, want %s", i, d, wantDirs[i])
		}
	}
}

// TestInjectChangedFiles：P1-1——runAdapters 对实现 SetChangedFiles 的适配器
// 注入变更文件（增量）；全量构建注入 nil（每次运行重置，防残留）。
func TestInjectChangedFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/e2e\n\ngo 1.21\n")
	writeFile(t, filepath.Join(dir, "main.go"), "package main\n\nfunc main() {}\n")
	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	mock := &mockAdapter{}
	orch := &Orchestrator{
		Repo:     &domain.Repository{Path: dir, Module: "example.com/e2e", Modules: []string{"example.com/e2e"}},
		RepoImpl: sqlite.NewRepo(db),
		Adapters: []domain.IndexerPort{mock},
	}

	if _, err := orch.FullBuild(context.Background()); err != nil {
		t.Fatalf("full build: %v", err)
	}
	if mock.changed != nil {
		t.Errorf("FullBuild changed = %v, want nil", mock.changed)
	}

	if _, err := orch.IncrementalBuild(context.Background(), []string{"main.go"}); err != nil {
		t.Fatalf("incremental build: %v", err)
	}
	if len(mock.changed) != 1 || mock.changed[0] != "main.go" {
		t.Errorf("IncrementalBuild changed = %v, want [main.go]", mock.changed)
	}

	if _, err := orch.FullBuild(context.Background()); err != nil {
		t.Fatalf("full build 2: %v", err)
	}
	if mock.changed != nil {
		t.Errorf("FullBuild after incremental changed = %v, want nil", mock.changed)
	}
}
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestFlushFKRetry：跨批 FK 冲突（P2）——边批先于节点批落库时外键冲突，
// flush 收集失败边不静默丢弃；构建尾部全部节点落库后重试 → 边不丢。
func TestFlushFKRetry(t *testing.T) {
	dir := t.TempDir()
	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	orch := New(&domain.Repository{Path: dir, Module: "m", Modules: []string{"m"}}, db)
	src := domain.CanonicalID("symbol:go:m:a")
	tgt := domain.CanonicalID("symbol:go:m:b")
	var mu sync.Mutex
	skipped := 0

	b1 := newBatch()
	b1.edges = []*domain.Fact{{SourceID: src, TargetID: tgt, Kind: domain.FactCalls}}
	if err := orch.flush(b1, &mu, &skipped); err != nil {
		t.Fatalf("flush edges: %v", err)
	}
	if skipped != 0 {
		t.Fatalf("skipped = %d, want 0（FK 失败待重试）", skipped)
	}

	b2 := newBatch()
	b2.nodes = []*domain.CodeEntity{
		{ID: src, Kind: domain.KindFunction, Name: "a"},
		{ID: tgt, Kind: domain.KindFunction, Name: "b"},
	}
	if err := orch.flush(b2, &mu, &skipped); err != nil {
		t.Fatalf("flush nodes: %v", err)
	}

	orch.retryFailedFK(&skipped)
	if skipped != 0 {
		t.Errorf("skipped = %d, want 0（重试后无残留）", skipped)
	}
	var cnt int
	if err := db.QueryRow(`SELECT COUNT(*) FROM edges WHERE source_id = ? AND target_id = ?`,
		string(src), string(tgt)).Scan(&cnt); err != nil || cnt != 1 {
		t.Fatalf("edge count = %d, %v; want 1（跨批 FK 边不丢）", cnt, err)
	}
}
