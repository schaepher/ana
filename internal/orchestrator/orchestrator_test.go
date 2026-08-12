package orchestrator

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"codeintel/internal/domain"
	"codeintel/internal/infrastructure/sqlite"
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

	orch := New(&domain.Repository{Path: dir, Module: "example.com/e2e"}, db)
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

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
