package orchestrator

import (
	"sync"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
	"golang.org/x/tools/go/packages"
)

// TestFullBuildAndQuery 端到端：临时 Go 模块 → 全量构建 → 校验图数据。
// 需要 scip-go 在 PATH（或 go bin）。
func TestFullBuildAndQuery(t *testing.T) {
	if _, err := exec.LookPath("scip-go"); err != nil {
		t.Skip("scip-go not found in PATH")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/e2e\n\ngo 1.21\n")
	writeFile(t, filepath.Join(dir, "main.go"), `package main

import "example.com/e2e/svc"

func main() {
	s := &svc.Service{}
	s.Handle()
}
`)
	writeFile(t, filepath.Join(dir, "svc", "svc.go"), `package svc

type Service struct{}

type Handler interface {
	Handle()
}

func (s *Service) Handle() {
	s.helper()
}

func (s *Service) helper() {}
`)
	writeFile(t, filepath.Join(dir, "svc", "svc_test.go"), `package svc

func (s *Service) TestHelper() {
	s.helper()
}
`)

	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	orch := New(&domain.Repository{Path: dir, Module: "example.com/e2e", Modules: []string{"example.com/e2e"}}, db)
	res, err := orch.FullBuild(context.Background())
	if err != nil {
		t.Fatalf("full build: %v", err)
	}
	if res.Status == domain.BuildFailed {
		t.Fatalf("build failed: %+v", res.Adapter)
	}
	if res.Nodes == 0 || res.Edges == 0 {
		t.Fatalf("empty build result: %+v", res)
	}
	t.Logf("build: %d nodes, %d edges, status=%s", res.Nodes, res.Edges, res.Status)

	repo := orch.GetRepo()

	// 方法节点存在（SCIP 符号权威）
	sym, err := repo.GetSymbol("symbol:go:example.com/e2e/svc:(Service).Handle")
	if err != nil {
		t.Fatalf("GetSymbol Handle: %v", err)
	}
	if sym.Kind != domain.KindMethod {
		t.Errorf("Handle kind = %s, want method", sym.Kind)
	}
	if sym.LineStart != 9 {
		t.Errorf("Handle line = %d, want 9", sym.LineStart)
	}

	// main 调用 (Service).Handle
	callees, err := repo.GetCallees("symbol:go:example.com/e2e:main", 1, 0.8)
	if err != nil {
		t.Fatalf("GetCallees: %v", err)
	}
	if len(callees) != 1 || callees[0].TargetID != "symbol:go:example.com/e2e/svc:(Service).Handle" {
		t.Errorf("main callees = %+v", callees)
	}

	// Handle 的调用者包含 main 与测试函数
	callers, err := repo.GetCallers("symbol:go:example.com/e2e/svc:(Service).Handle", 1, 0.8)
	if err != nil {
		t.Fatalf("GetCallers: %v", err)
	}
	seen := map[string]bool{}
	for _, f := range callers {
		seen[string(f.SourceID)] = true
	}
	if !seen["symbol:go:example.com/e2e:main"] {
		t.Errorf("Handle callers missing main: %v", callers)
	}

	// 实现关系：Service 实现 Handler
	impl, err := repo.GetSymbol("symbol:go:example.com/e2e/svc:Service")
	if err != nil {
		t.Fatalf("GetSymbol Service: %v", err)
	}
	if impl.Kind != domain.KindStruct {
		t.Errorf("Service kind = %s", impl.Kind)
	}
	// 影响分析：修改 Handle 影响谁
	impact, err := repo.GetImpact("symbol:go:example.com/e2e/svc:(Service).Handle", 2)
	if err != nil {
		t.Fatalf("GetImpact: %v", err)
	}
	if len(impact) == 0 {
		t.Error("impact should not be empty")
	}
}

// TestDiscoverModules：P2-3——递归扫描 go.mod（根在前）；跳过
// .git/.codeintel/vendor；module 目录内不再嵌套扫描。
func TestDiscoverModules(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/root\n\ngo 1.21\n")
	writeFile(t, filepath.Join(dir, "app", "go.mod"), "module example.com/app\n\ngo 1.21\n")
	writeFile(t, filepath.Join(dir, "vendor", "go.mod"), "module vendor.example\n") // 应跳过
	writeFile(t, filepath.Join(dir, ".codeintel", "go.mod"), "module hidden.example\n")
	writeFile(t, filepath.Join(dir, "lib", "sub", "go.mod"), "module example.com/lib\n\ngo 1.21\n")
	// module 目录内的嵌套 go.mod 不扫（Go 语义：嵌套 module 独立）
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

// TestFullBuildMultiModule：P2-3 端到端——双 go.mod monorepo：
// 根 module 的 main 调用子 module（app/）的函数，跨 module calls 边成立。
func TestFullBuildMultiModule(t *testing.T) {
	if _, err := exec.LookPath("scip-go"); err != nil {
		t.Skip("scip-go not found in PATH")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/root\n\ngo 1.21\n\nrequire example.com/app v0.0.0\n\nreplace example.com/app => ./app\n")
	writeFile(t, filepath.Join(dir, "main.go"), `package main

import "example.com/app"

func main() {
	app.Hello()
}
`)
	writeFile(t, filepath.Join(dir, "app", "go.mod"), "module example.com/app\n\ngo 1.21\n")
	writeFile(t, filepath.Join(dir, "app", "app.go"), `package app

func Hello() {}
`)

	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	orch := New(&domain.Repository{
		Path:       dir,
		Module:     "example.com/root",
		Modules:    []string{"example.com/root", "example.com/app"},
		ModuleDirs: []string{".", "app"},
	}, db)
	res, err := orch.FullBuild(context.Background())
	if err != nil {
		t.Fatalf("full build: %v", err)
	}
	if res.Status == domain.BuildFailed {
		t.Fatalf("build failed: %+v", res.Adapter)
	}

	repo := orch.GetRepo()
	// 跨 module 调用：root main → app.Hello
	callees, err := repo.GetCallees("symbol:go:example.com/root:main", 1, 0.8)
	if err != nil {
		t.Fatalf("GetCallees: %v", err)
	}
	found := false
	for _, f := range callees {
		if f.TargetID == "symbol:go:example.com/app:Hello" {
			found = true
		}
	}
	if !found {
		t.Errorf("main callees 缺跨 module 调用: %+v", callees)
	}
	// 子 module 符号存在（scip 独立索引）
	if _, err := repo.GetSymbol("symbol:go:example.com/app:Hello"); err != nil {
		t.Errorf("app.Hello 节点缺失: %v", err)
	}
}

// mockAdapter 记录 SetChangedFiles 注入（P1-1 AST 文件级跳过），
// Index 产出固定节点避免空构建。
type mockAdapter struct {
	changed []string
}

func (m *mockAdapter) Name() string { return "mock" }

func (m *mockAdapter) Index(_ context.Context, repo *domain.Repository, _ []*packages.Package, emit domain.EmitFunc) error {
	return emit(domain.Item{Node: &domain.CodeEntity{
		ID: "symbol:go:example.com/e2e:mock", Kind: domain.KindFunction,
		Name: "mock", FilePath: "main.go",
	}})
}

func (m *mockAdapter) SetChangedFiles(files []string) { m.changed = files }

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

	// 全量：nil（不限制）
	if _, err := orch.FullBuild(context.Background()); err != nil {
		t.Fatalf("full build: %v", err)
	}
	if mock.changed != nil {
		t.Errorf("FullBuild changed = %v, want nil", mock.changed)
	}

	// 增量：变更文件注入
	if _, err := orch.IncrementalBuild(context.Background(), []string{"main.go"}); err != nil {
		t.Fatalf("incremental build: %v", err)
	}
	if len(mock.changed) != 1 || mock.changed[0] != "main.go" {
		t.Errorf("IncrementalBuild changed = %v, want [main.go]", mock.changed)
	}

	// 再次全量：重置为 nil（防残留）
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

	// 批1：边引用尚未落库的节点 → FK 失败 → 收集进 failedEdges（不丢）
	b1 := newBatch()
	b1.edges = []*domain.Fact{{SourceID: src, TargetID: tgt, Kind: domain.FactCalls}}
	if err := orch.flush(b1, &mu, &skipped); err != nil {
		t.Fatalf("flush edges: %v", err)
	}
	if skipped != 0 {
		t.Fatalf("skipped = %d, want 0（FK 失败待重试）", skipped)
	}

	// 批2：节点落库
	b2 := newBatch()
	b2.nodes = []*domain.CodeEntity{
		{ID: src, Kind: domain.KindFunction, Name: "a"},
		{ID: tgt, Kind: domain.KindFunction, Name: "b"},
	}
	if err := orch.flush(b2, &mu, &skipped); err != nil {
		t.Fatalf("flush nodes: %v", err)
	}

	// 构建尾部重试 → 成功，边入库
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
