//go:build integration

package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
)

// TestReindexRestores：reindex 一步重建——破坏索引数据后 reindex
// 恢复完整索引（删除旧库 + 全量 init）。
func TestReindexRestores(t *testing.T) {
	if !scipGoAvailable() {
		t.Skip("scip-go not found (install: go install github.com/scip-code/scip-go/cmd/scip-go@latest)")
	}
	dir := fixtureRepo(t)

	if code := runCLI(t, "init", "--repo", dir); code != 0 {
		t.Fatalf("init exit = %d", code)
	}

	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := db.Exec("DELETE FROM nodes"); err != nil {
		t.Fatalf("破坏库失败: %v", err)
	}
	db.Close()

	if code := runCLI(t, "reindex", "--repo", dir); code != 0 {
		t.Fatalf("reindex exit = %d, want 0", code)
	}
	db2, err := sqlite.Open(dir)
	if err != nil {
		t.Fatalf("reindex 后 Open: %v", err)
	}
	defer db2.Close()
	repo := sqlite.NewRepo(db2)
	if _, err := repo.GetSymbol("symbol:go:example.com/app:main"); err != nil {
		t.Errorf("reindex 后 main 符号缺失: %v", err)
	}
	nodes, edges, err := repo.Counts()
	if err != nil || nodes == 0 || edges == 0 {
		t.Errorf("reindex 后图为空: nodes=%d edges=%d err=%v", nodes, edges, err)
	}
}

// TestIncrementalUpdate：init → 修改文件 → update → 新符号出现、
// 旧符号保留、被删除符号消失（TD.md 5.2 增量语义）。
func TestIncrementalUpdate(t *testing.T) {
	if !scipGoAvailable() {
		t.Skip("scip-go not found (install: go install github.com/scip-code/scip-go/cmd/scip-go@latest)")
	}
	dir := fixtureRepo(t)

	gitRun(t, dir, "init", "-q")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "init")
	if code := runCLI(t, "init", "--repo", dir); code != 0 {
		t.Fatalf("init exit = %d", code)
	}

	svcPath := filepath.Join(dir, "svc", "svc.go")
	data, err := os.ReadFile(svcPath)
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(string(data), "func aliasLocal() {\n\ta := &Cfg{}\n\tb := a\n\tb.Key = \"y\"\n}\n",
		"", 1)
	updated += "\nfunc newFunc() int { return 42 }\n"
	writeFile(t, svcPath, updated)

	if code := runCLI(t, "update", "--repo", dir); code != 0 {
		t.Fatalf("update exit = %d", code)
	}

	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	repo := sqlite.NewRepo(db)

	if _, err := repo.GetSymbol("symbol:go:example.com/app/svc:newFunc"); err != nil {
		t.Errorf("newFunc 应出现在增量后索引: %v", err)
	}

	if _, err := repo.GetSymbol("symbol:go:example.com/app/svc:(Service).Handle"); err != nil {
		t.Errorf("未变更符号应保留: %v", err)
	}
	if _, err := repo.GetSymbol("symbol:go:example.com/app:main"); err != nil {
		t.Errorf("main 应保留: %v", err)
	}

	if _, err := repo.GetSymbol("symbol:go:example.com/app/svc:aliasLocal"); err == nil {
		t.Error("aliasLocal 应从索引消失")
	}
	aliasFields, err := repo.GetFunctionFields("symbol:go:example.com/app/svc:aliasLocal")
	if err != nil || len(aliasFields) > 0 {
		t.Errorf("aliasLocal 摘要应清空: %v, %v", aliasFields, err)
	}

	fillMRows, err := repo.GetFunctionFields("symbol:go:example.com/app/svc:fillM")
	if err != nil || len(fillMRows) == 0 {
		t.Errorf("fillM 摘要应保留（变更文件内重新索引）: %v, %v", fillMRows, err)
	}

	runRows, err := repo.GetFunctionFields("symbol:go:example.com/app/svc:run")
	if err != nil {
		t.Fatalf("GetFunctionFields run: %v", err)
	}
	keyHit := false
	for _, s := range runRows {
		if s.AccessKind == domain.SummaryIndirectWrite && strings.Contains(s.FieldPath, "Cfg.Key") {
			keyHit = true
		}
	}
	if !keyHit {
		t.Errorf("run 的间接写摘要（跨文件闭包）应保留: %+v", runRows)
	}

	meta, err := repo.GetLatest()
	if err != nil {
		t.Fatalf("GetLatest: %v", err)
	}
	if meta.ToolName != "incremental" {
		t.Errorf("build tool_name = %s, want incremental", meta.ToolName)
	}
}
