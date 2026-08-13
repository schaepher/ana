package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
)

func TestMainDispatch(t *testing.T) {
	ctx := context.Background()
	if code := Main(ctx, []string{}); code != 2 {
		t.Errorf("no args = %d, want 2", code)
	}
	if code := Main(ctx, []string{"bogus"}); code != 2 {
		t.Errorf("unknown cmd = %d, want 2", code)
	}
	if code := Main(ctx, []string{"help"}); code != 0 {
		t.Errorf("help = %d, want 0", code)
	}
	if code := Main(ctx, []string{"version"}); code != 0 {
		t.Errorf("version = %d, want 0", code)
	}
}

func TestClean(t *testing.T) {
	dir := t.TempDir()
	// 无索引目录：直接返回 0
	if code := cmdClean([]string{"--repo", dir, "--force"}); code != 0 {
		t.Errorf("clean without index = %d, want 0", code)
	}
	// 建索引目录后 force 删除
	target := filepath.Join(dir, ".codeintel")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "codeintel.db"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := cmdClean([]string{"--repo", dir, "--force"}); code != 0 {
		t.Errorf("clean = %d, want 0", code)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Error(".codeintel should be removed")
	}
}

// seedRepo 建临时仓库 + 预填一个小图（query 的 resolveRepo 要求 go.mod）。
func seedRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/m\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	r := sqlite.NewRepo(db)
	nodes := []*domain.CodeEntity{
		{ID: "symbol:go:example.com/m:main", Kind: domain.KindFunction, Name: "main", FilePath: "main.go"},
		{ID: "symbol:go:example.com/m/svc:(Svc).Run", Kind: domain.KindMethod, Name: "(Svc).Run", FilePath: "svc/svc.go"},
	}
	if _, err := r.SaveBatchStats(nodes, nil); err != nil {
		t.Fatalf("save nodes: %v", err)
	}
	if _, err := r.SaveBatchStats(nil, []*domain.Fact{{
		SourceID: "symbol:go:example.com/m:main", TargetID: "symbol:go:example.com/m/svc:(Svc).Run",
		Kind: domain.FactCalls, Confidence: 0.9,
	}}); err != nil {
		t.Fatalf("save edge: %v", err)
	}
	return dir
}

func TestQuerySymbol(t *testing.T) {
	dir := seedRepo(t)
	if code := cmdQuery([]string{"symbol", "main", "--repo", dir}); code != 0 {
		t.Errorf("query symbol main = %d, want 0", code)
	}
	// 未知符号 → 非 0
	if code := cmdQuery([]string{"symbol", "nope_nope", "--repo", dir}); code == 0 {
		t.Error("query unknown symbol should fail")
	}
}

func TestQueryCalleesCallers(t *testing.T) {
	dir := seedRepo(t)
	if code := cmdQuery([]string{"callees", "main", "--repo", dir}); code != 0 {
		t.Errorf("query callees = %d, want 0", code)
	}
	if code := cmdQuery([]string{"callers", "symbol:go:example.com/m/svc:(Svc).Run", "--repo", dir}); code != 0 {
		t.Errorf("query callers = %d, want 0", code)
	}
	// 缺符号参数 → 2
	if code := cmdQuery([]string{"callees"}); code != 2 {
		t.Errorf("callees without symbol = %d, want 2", code)
	}
	// 无子命令 → 2
	if code := cmdQuery([]string{}); code != 2 {
		t.Errorf("query without subcommand = %d, want 2", code)
	}
}

func TestQueryNoRepo(t *testing.T) {
	// 不存在的 repo 目录 → 1
	if code := cmdQuery([]string{"symbol", "main", "--repo", filepath.Join(t.TempDir(), "nope")}); code != 1 {
		t.Errorf("query with bad repo = %d, want 1", code)
	}
}
