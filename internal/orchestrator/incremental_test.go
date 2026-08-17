package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
	"github.com/schaepher/codeintel/internal/infrastructure/ssa"
	"golang.org/x/tools/go/packages"
)

// richAdapter：可控产出节点/边/摘要的适配器，用于验证增量 keep 过滤的三个分支。
type richAdapter struct {
	items []domain.Item
}

func (f *richAdapter) Name() string { return "rich" }

func (f *richAdapter) Index(_ context.Context, _ *domain.Repository, _ []*packages.Package, emit domain.EmitFunc) error {
	for _, it := range f.items {
		if err := emit(it); err != nil {
			return err
		}
	}
	return nil
}

func TestNewAndGetRepo(t *testing.T) {
	db, err := sqlite.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	orch := New(&domain.Repository{Path: t.TempDir(), Module: "example.com/m", Modules: []string{"example.com/m"}}, db)
	if len(orch.Adapters) != 4 {
		t.Errorf("adapters = %d, want 4 (scip/ast/git/ssa)", len(orch.Adapters))
	}
	if orch.GetRepo() == nil {
		t.Error("GetRepo should return repo impl")
	}
}

// TestIncrementalBuildKeepsUnchangedFiles：增量构建——变更文件的旧节点被删
// （内容变化后不再产出的 stale 节点），未变更文件的节点原样保留，边/摘要
// 按端点过滤后写入，build_metadata 记录 tool_name=incremental。
func TestIncrementalBuildKeepsUnchangedFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/inc\n\ngo 1.21\n")
	writeFile(t, filepath.Join(dir, "main.go"), "package main\n\nfunc main() {}\n")
	writeFile(t, filepath.Join(dir, "other.go"), "package main\n\nfunc other() {}\n")

	funcID := domain.CanonicalID("symbol:go:example.com/inc:main")
	vID := domain.CanonicalID("symbol:go:example.com/inc:main#v2")
	// 适配器每次运行产出一致的 main.go 数据（模拟新内容）
	a := &richAdapter{items: []domain.Item{
		{Node: &domain.CodeEntity{ID: funcID, Kind: domain.KindFunction, Name: "main", FilePath: "main.go", LineStart: 2}},
		{Node: &domain.CodeEntity{ID: vID, Kind: domain.KindSSAValue, Name: "v2", FilePath: "main.go",
			Properties: map[string]any{"func_id": string(funcID)}}},
		{Fact: &domain.Fact{SourceID: funcID, TargetID: vID, Kind: domain.FactCalls, ToolSource: domain.ToolSSA, Confidence: 0.9}},
		{Summary: &domain.FunctionFieldSummary{FunctionID: funcID, AccessKind: domain.SummaryDirectWrite,
			FieldPath: "example.com/inc.T.A", InstancePath: "t.A", LineStart: 3, CodeSnippet: "t.A = v"}},
	}}
	o, repo := newTestOrchestrator(t, []domain.IndexerPort{a})
	o.Repo = &domain.Repository{Path: dir, Module: "example.com/inc", Modules: []string{"example.com/inc"}}

	// 预置：未变更文件的节点 + 变更文件中的旧内容（stale）及其出边
	otherID := domain.CanonicalID("symbol:go:example.com/inc:other")
	staleID := domain.CanonicalID("symbol:go:example.com/inc:main#stale")
	if _, err := repo.SaveBatchStats([]*domain.CodeEntity{
		{ID: otherID, Kind: domain.KindFunction, Name: "other", FilePath: "other.go", LineStart: 2},
		{ID: staleID, Kind: domain.KindSSAValue, Name: "stale", FilePath: "main.go",
			Properties: map[string]any{"func_id": string(funcID)}},
	}, []*domain.Fact{
		{SourceID: staleID, TargetID: otherID, Kind: domain.FactCalls, ToolSource: domain.ToolSSA, Confidence: 0.9},
	}, nil); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Q182：模拟"已用当前分析器全量构建过"（无 marker 时增量会保守降级全量）
	if err := ssa.SaveAnalyzerMarker(dir); err != nil {
		t.Fatalf("save analyzer marker: %v", err)
	}

	res, err := o.IncrementalBuild(context.Background(), []string{"main.go"})
	if err != nil {
		t.Fatalf("IncrementalBuild: %v", err)
	}
	if res.Status == domain.BuildFailed {
		t.Fatalf("build failed: %+v", res.Adapter)
	}
	// ① stale 旧节点被删除（内容变化后不再产出）
	if _, err := repo.GetSymbol(staleID); err == nil {
		t.Error("stale node (main.go 旧内容) should be deleted")
	}
	// ② stale 的出边随级联删除消失
	edges, err := repo.GetCallees(staleID, 1, 0.8)
	if err != nil || len(edges) != 0 {
		t.Errorf("stale out-edges = %v, %v (want none)", edges, err)
	}
	// ③ 适配器重新产出的 main.go 数据存在（节点/边/摘要）
	if n, err := repo.GetSymbol(funcID); err != nil || n.Name != "main" {
		t.Errorf("func node = %+v, %v", n, err)
	}
	if n, err := repo.GetSymbol(vID); err != nil || n.Name != "v2" {
		t.Errorf("value node = %+v, %v", n, err)
	}
	edges, err = repo.GetCallees(funcID, 1, 0.8)
	if err != nil || len(edges) != 1 || string(edges[0].TargetID) != string(vID) {
		t.Errorf("func edges = %+v, %v (want 1 -> v2)", edges, err)
	}
	sums, err := repo.GetFunctionFields(funcID)
	if err != nil || len(sums) != 1 {
		t.Errorf("summaries = %+v, %v (want 1)", sums, err)
	}
	// ④ 未变更文件 other.go 原样保留
	if _, err := repo.GetSymbol(otherID); err != nil {
		t.Errorf("other.go node should be preserved: %v", err)
	}
	// ⑤ 元数据 tool_name=incremental
	meta, err := repo.GetLatest()
	if err != nil || meta.ToolName != "incremental" {
		t.Errorf("build metadata = %+v, %v (want tool_name=incremental)", meta, err)
	}
}

// TestDeleteFilesBatching：deleteFiles 超过 400 文件时分批删除（SQLite 参数
// 上限），未在列表中的文件不受影响。
func TestDeleteFilesBatching(t *testing.T) {
	o, repo := newTestOrchestrator(t, nil)
	_ = o

	const n = 450 // > 400 触发两批
	nodes := make([]*domain.CodeEntity, 0, n+1)
	paths := make([]string, 0, n+1)
	for i := 0; i < n; i++ {
		p := "f_" + string(rune('a'+i%26)) + ".go"
		nodes = append(nodes, &domain.CodeEntity{
			ID: domain.CanonicalID("symbol:go:example.com/m:node" + string(rune('a'+i%26)) + string(rune('0'+i/26))),
			Kind: domain.KindFunction, Name: "f", FilePath: p,
		})
		paths = append(paths, p)
	}
	nodes = append(nodes, &domain.CodeEntity{
		ID: domain.CanonicalID("symbol:go:example.com/m:keep"), Kind: domain.KindFunction,
		Name: "keep", FilePath: "keep.go",
	})
	if _, err := repo.SaveBatchStats(nodes, nil, nil); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := deleteFiles(repo, paths); err != nil {
		t.Fatalf("deleteFiles: %v", err)
	}
	cnt, _, err := repo.Counts()
	if err != nil {
		t.Fatal(err)
	}
	if cnt != 1 {
		t.Errorf("remaining nodes = %d, want 1 (keep.go)", cnt)
	}
}

// TestIncrementalDegradesToFullOnAnalyzerStale：Q182 场景 2 区分——分析器
// 版本变化（codeintel 新特性/逻辑变更，未变更包也须以新逻辑重建）时，
// 增量 update 自动降级为全量重建：写库范围全局（不再按变更文件裁剪），
// build_metadata tool_name=all。场景 1（包源码变化）仍走增量（其余测试）。
func TestIncrementalDegradesToFullOnAnalyzerStale(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/inc\n\ngo 1.21\n")
	writeFile(t, filepath.Join(dir, "main.go"), "package main\n\nfunc main() {}\n")

	funcID := domain.CanonicalID("symbol:go:example.com/inc:main")
	a := &richAdapter{items: []domain.Item{
		{Node: &domain.CodeEntity{ID: funcID, Kind: domain.KindFunction, Name: "main", FilePath: "main.go", LineStart: 2}},
	}}
	o, _ := newTestOrchestrator(t, []domain.IndexerPort{a})
	o.Repo = &domain.Repository{Path: dir, Module: "example.com/inc", Modules: []string{"example.com/inc"}}

	// 全量构建：写全局 analyzer marker
	if _, err := o.FullBuild(context.Background()); err != nil {
		t.Fatalf("FullBuild: %v", err)
	}
	if got := ssa.LoadAnalyzerMarker(dir); got != ssa.AnalyzerVersionHash() {
		t.Fatalf("FullBuild 后 marker = %q, want %q", got, ssa.AnalyzerVersionHash())
	}
	// 篡改 marker 模拟分析器版本变化（新特性上线，旧 marker 是旧逻辑的）
	if err := os.WriteFile(ssa.AnalyzerMarkerPath(dir), []byte("stale-analyzer"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 增量 update：应自动降级全量（未变更包也必须以新逻辑重建）
	if _, err := o.IncrementalBuild(context.Background(), []string{"main.go"}); err != nil {
		t.Fatalf("IncrementalBuild: %v", err)
	}
	var tool string
	// build_id 是随机 UUID，须按写入序（rowid）取最新
	if err := o.RepoImpl.QueryRow("SELECT tool_name FROM build_metadata ORDER BY rowid DESC LIMIT 1").Scan(&tool); err != nil {
		t.Fatalf("query build_metadata: %v", err)
	}
	if tool != "all" {
		t.Errorf("analyzer stale 时增量应降级全量（tool_name=all），got %q", tool)
	}
	// 降级后 marker 已更新为当前分析器
	if got := ssa.LoadAnalyzerMarker(dir); got != ssa.AnalyzerVersionHash() {
		t.Errorf("降级后 marker = %q, want %q", got, ssa.AnalyzerVersionHash())
	}
}
