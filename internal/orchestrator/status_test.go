package orchestrator

import (
	"context"
	"errors"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
	"golang.org/x/tools/go/packages"
)

// fakeAdapter 测试用适配器：固定名字与结果。
type fakeAdapter struct {
	name string
	err  error
}

func (f *fakeAdapter) Name() string { return f.name }
func (f *fakeAdapter) Index(ctx context.Context, repo *domain.Repository, _ []*packages.Package, emit domain.EmitFunc) error {
	if f.err != nil {
		return f.err
	}
	// 产出少量数据
	_ = emit(domain.Item{Node: &domain.CodeEntity{
		ID:   domain.CanonicalID("symbol:go:example.com/m:" + f.name),
		Kind: domain.KindFunction, Name: f.name, FilePath: "main.go",
	}})
	return nil
}

// newTestOrchestrator 建临时 DB + 指定适配器列表的 Orchestrator。
func newTestOrchestrator(t *testing.T, adapters []domain.IndexerPort) (*Orchestrator, *sqlite.Repo) {
	t.Helper()
	db, err := sqlite.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	o := &Orchestrator{
		Repo:     &domain.Repository{Path: t.TempDir(), Module: "example.com/m", Modules: []string{"example.com/m"}},
		Adapters: adapters,
		RepoImpl: sqlite.NewRepo(db),
	}
	return o, o.RepoImpl
}

func TestFullBuildSuccess(t *testing.T) {
	o, repo := newTestOrchestrator(t, []domain.IndexerPort{
		&fakeAdapter{name: "a"},
		&fakeAdapter{name: "b"},
	})
	res, err := o.FullBuild(context.Background())
	if err != nil {
		t.Fatalf("FullBuild: %v", err)
	}
	if res.Status != domain.BuildSuccess {
		t.Errorf("status = %s, want success (adapters: %+v)", res.Status, res.Adapter)
	}
	if res.Nodes != 2 || res.Edges != 0 {
		t.Errorf("counts = %d nodes %d edges", res.Nodes, res.Edges)
	}
	// 构建元数据已写入
	meta, err := repo.GetLatest()
	if err != nil || meta.Status != domain.BuildSuccess {
		t.Errorf("build metadata = %+v, err %v", meta, err)
	}
}

func TestFullBuildDegraded(t *testing.T) {
	// 非 scip 适配器失败 → 状态降级（已提交数据保留）
	o, repo := newTestOrchestrator(t, []domain.IndexerPort{
		&fakeAdapter{name: "a"},
		&fakeAdapter{name: "b", err: errors.New("boom")},
	})
	res, err := o.FullBuild(context.Background())
	if err != nil {
		t.Fatalf("FullBuild: %v", err)
	}
	if res.Status != domain.BuildDegraded {
		t.Errorf("status = %s, want degraded", res.Status)
	}
	// 成功的适配器数据仍在
	if res.Nodes != 1 {
		t.Errorf("nodes = %d, want 1 (data from successful adapter)", res.Nodes)
	}
	meta, _ := repo.GetLatest()
	if meta.Status != domain.BuildDegraded || meta.ErrorMsg == "" {
		t.Errorf("metadata = %+v", meta)
	}
}

func TestFullBuildScipFailureFailsBuild(t *testing.T) {
	// SCIP 适配器失败 → 整个构建失败（符号权威缺失）
	o, _ := newTestOrchestrator(t, []domain.IndexerPort{
		&fakeAdapter{name: "scip", err: errors.New("scip-go missing")},
		&fakeAdapter{name: "codegraph"},
	})
	res, err := o.FullBuild(context.Background())
	if err != nil {
		t.Fatalf("FullBuild: %v", err)
	}
	if res.Status != domain.BuildFailed {
		t.Errorf("status = %s, want failed", res.Status)
	}
}

func TestHeadCommitSHANotGit(t *testing.T) {
	if got := headCommitSHA(t.TempDir()); got != "" {
		t.Errorf("headCommitSHA on non-git dir = %q, want empty", got)
	}
}
