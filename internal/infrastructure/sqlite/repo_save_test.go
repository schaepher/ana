package sqlite

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

func TestSaveBatchAndGetSymbol(t *testing.T) {
	r := newTestRepo(t)
	n := node("symbol:go:example.com/m:main", "function", "main", "main.go")
	n.LineStart = 5
	save(t, r, []*domain.CodeEntity{n}, nil)

	got, err := r.GetSymbol(n.ID)
	if err != nil {
		t.Fatalf("GetSymbol: %v", err)
	}
	if got.Kind != domain.KindFunction || got.LineStart != 5 {
		t.Errorf("GetSymbol = %+v", got)
	}
	if got.Signature() != "sig:main" {
		t.Errorf("Signature = %q", got.Signature())
	}

	if _, err := r.GetSymbol("symbol:go:example.com/m:nope"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("missing symbol err = %v, want ErrNotFound", err)
	}
}
func TestSaveBatchEmpty(t *testing.T) {
	r := newTestRepo(t)
	res, err := r.SaveBatchStats(nil, nil, nil)
	if err != nil {
		t.Fatalf("SaveBatchStats(nil): %v", err)
	}
	if res.SkippedEdges != 0 {
		t.Errorf("SkippedEdges = %d", res.SkippedEdges)
	}
}
func TestSaveBatchFKSkip(t *testing.T) {
	r := newTestRepo(t)

	res, err := r.SaveBatchStats(nil, []*domain.Fact{{
		SourceID: "symbol:go:example.com/m:a",
		TargetID: "symbol:go:example.com/m:b",
		Kind:     domain.FactCalls,
	}}, nil)
	if err != nil {
		t.Fatalf("SaveBatchStats: %v", err)
	}
	if res.SkippedEdges != 0 {
		t.Errorf("SkippedEdges = %d, want 0（FK 失败待重试，非最终跳过）", res.SkippedEdges)
	}
	if len(res.FailedEdges) != 1 {
		t.Fatalf("FailedEdges = %d, want 1", len(res.FailedEdges))
	}
}

// TestSaveBatchFKRetry：FK 失败边在节点落库后重试成功（P2——跨批丢边
// 修复：并发构建时边批先于节点批落库 → FK 冲突 → 原实现静默跳过）。
func TestSaveBatchFKRetry(t *testing.T) {
	r := newTestRepo(t)
	src := domain.CanonicalID("symbol:go:example.com/m:a")
	tgt := domain.CanonicalID("symbol:go:example.com/m:b")
	edge := &domain.Fact{SourceID: src, TargetID: tgt, Kind: domain.FactCalls}

	res, err := r.SaveBatchStats(nil, []*domain.Fact{edge}, nil)
	if err != nil || len(res.FailedEdges) != 1 {
		t.Fatalf("first: %v %+v", err, res)
	}

	if _, err := r.SaveBatchStats([]*domain.CodeEntity{
		{ID: src, Kind: domain.KindFunction, Name: "a"},
		{ID: tgt, Kind: domain.KindFunction, Name: "b"},
	}, nil, nil); err != nil {
		t.Fatalf("save nodes: %v", err)
	}

	res2, err := r.SaveBatchStats(nil, res.FailedEdges, nil)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if len(res2.FailedEdges) != 0 || res2.SkippedEdges != 0 {
		t.Fatalf("retry 残留: %+v", res2)
	}
	// 边已入库
	var cnt int
	if err := r.QueryRow(`SELECT COUNT(*) FROM edges WHERE source_id = ? AND target_id = ?`,
		string(src), string(tgt)).Scan(&cnt); err != nil || cnt != 1 {
		t.Fatalf("edge count = %d, %v; want 1", cnt, err)
	}
}
func TestSaveBatchConfidenceUpdate(t *testing.T) {
	r := newTestRepo(t)
	a := node("symbol:go:example.com/m:a", "function", "a", "a.go")
	b := node("symbol:go:example.com/m:b", "function", "b", "b.go")
	save(t, r, []*domain.CodeEntity{a, b}, nil)
	edge := func(c float64) *domain.Fact {
		return &domain.Fact{SourceID: a.ID, TargetID: b.ID, Kind: domain.FactCalls, Confidence: c, ToolSource: domain.ToolCodeGraph}
	}
	save(t, r, nil, []*domain.Fact{edge(0.5)})
	save(t, r, nil, []*domain.Fact{edge(0.9)})

	facts, err := r.GetCallees(a.ID, 1, 0.1)
	if err != nil {
		t.Fatalf("GetCallees: %v", err)
	}
	if len(facts) != 1 || facts[0].Confidence != 0.9 {
		t.Errorf("facts = %+v, want single edge with confidence 0.9", facts)
	}

	save(t, r, nil, []*domain.Fact{edge(0.3)})
	facts, _ = r.GetCallees(a.ID, 1, 0.1)
	if facts[0].Confidence != 0.9 {
		t.Errorf("confidence downgraded to %v", facts[0].Confidence)
	}
}
func TestGetSymbolByName(t *testing.T) {
	r := newTestRepo(t)
	save(t, r, []*domain.CodeEntity{
		node("symbol:go:example.com/m:main", "function", "main", "main.go"),
		node("symbol:go:example.com/m:run", "function", "runLoop", "run.go"),
	}, nil)

	got, err := r.GetSymbolByName("main")
	if err != nil || len(got) != 1 || got[0].Name != "main" {
		t.Errorf("exact match = %+v, err %v", got, err)
	}

	got, err = r.GetSymbolByName("run")
	if err != nil || len(got) == 0 {
		t.Errorf("fuzzy match = %+v, err %v", got, err)
	}

	got, err = r.GetSymbolByName("zzz_none")
	if err != nil || len(got) != 0 {
		t.Errorf("no match = %+v, err %v", got, err)
	}
}
func TestCounts(t *testing.T) {
	r := newTestRepo(t)
	nodes, edges, err := r.Counts()
	if err != nil {
		t.Fatalf("Counts: %v", err)
	}
	if nodes != 0 || edges != 0 {
		t.Errorf("empty db counts = %d/%d", nodes, edges)
	}
	save(t, r, []*domain.CodeEntity{node("a", "function", "a", "a.go"), node("b", "function", "b", "b.go")}, nil)
	save(t, r, nil, []*domain.Fact{{SourceID: "a", TargetID: "b", Kind: domain.FactCalls, Confidence: 0.8}})
	nodes, edges, _ = r.Counts()
	if nodes != 2 || edges != 1 {
		t.Errorf("counts = %d/%d, want 2/1", nodes, edges)
	}
}
func TestSaveAndGetLatest(t *testing.T) {
	r := newTestRepo(t)

	if _, err := r.GetLatest(); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("empty GetLatest err = %v, want ErrNotFound", err)
	}
	meta := &domain.BuildMeta{
		BuildID: "b1", CommitSHA: "abc123", ToolName: "all",
		Status: domain.BuildSuccess, DurationMs: 100, ErrorMsg: "",
	}
	if err := r.Save(meta); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := r.GetLatest()
	if err != nil {
		t.Fatalf("GetLatest: %v", err)
	}
	if got.BuildID != "b1" || got.Status != domain.BuildSuccess || got.CommitSHA != "abc123" {
		t.Errorf("GetLatest = %+v", got)
	}

	meta2 := &domain.BuildMeta{BuildID: "b2", CommitSHA: "def456", ToolName: "incremental", Status: domain.BuildDegraded, DurationMs: 5, ErrorMsg: "git: fail"}
	if err := r.Save(meta2); err != nil {
		t.Fatalf("Save meta2: %v", err)
	}
	got, _ = r.GetLatest()
	if got.BuildID != "b2" || got.Status != domain.BuildDegraded {
		t.Errorf("GetLatest after replace = %+v", got)
	}
}
func TestDeleteByFileCascade(t *testing.T) {
	r := newTestRepo(t)
	a := node("symbol:go:example.com/m:a", "function", "a", "a.go")
	b := node("symbol:go:example.com/m:b", "function", "b", "b.go")
	save(t, r, []*domain.CodeEntity{a, b}, nil)
	save(t, r, nil, []*domain.Fact{{SourceID: a.ID, TargetID: b.ID, Kind: domain.FactCalls, Confidence: 0.8}})

	if err := r.DeleteByFile("a.go"); err != nil {
		t.Fatalf("DeleteByFile: %v", err)
	}
	if _, err := r.GetSymbol(a.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("node a should be deleted, err = %v", err)
	}

	callees, err := r.GetCallees(b.ID, 1, 0.1)
	if err != nil {
		t.Fatalf("GetCallees: %v", err)
	}
	if len(callees) != 0 {
		t.Errorf("cascade: callees of b = %+v, want none", callees)
	}

	if _, err := r.GetSymbol(b.ID); err != nil {
		t.Errorf("node b should remain: %v", err)
	}
}
func TestOpenSchemaVersionMismatch(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	if _, err := db.Exec("PRAGMA user_version = 99"); err != nil {
		t.Fatalf("set user_version: %v", err)
	}
	db.Close()

	if _, err := Open(dir); err == nil {
		t.Error("Open should fail on schema version mismatch")
	}

	if db2, _ := Open(t.TempDir()); db2.RepoPath() != filepath.Clean(t.TempDir()) {

		db2.Close()
	}
}
func TestGetSymbolByNameExcludesFieldTrace(t *testing.T) {
	r := newTestRepo(t)
	funcID := "symbol:go:example.com/m:f"
	nodes := []*domain.CodeEntity{
		node("symbol:go:example.com/m:run", "function", "runLoop", "run.go"),

		faNode(funcID+"#cfg.write@5", funcID, "example.com/m.T.cfg", "cfg", 5),
		svNode(funcID+"#t0", funcID),
	}
	save(t, r, nodes, nil)

	got, err := r.GetSymbolByName("cfg")
	if err != nil {
		t.Fatalf("GetSymbolByName: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("search cfg = %+v, want none (field_access excluded)", got)
	}

	got, err = r.GetSymbolByName("run")
	if err != nil || len(got) != 1 || got[0].Name != "runLoop" {
		t.Errorf("search run = %+v, err %v", got, err)
	}

	got, err = r.GetSymbolByName(funcID + "#cfg")
	if err != nil || len(got) != 0 {
		t.Errorf("search by field_access id = %+v, err %v", got, err)
	}
}

// Q215 陈旧摘要覆盖：INSERT OR IGNORE 在 UNIQUE 冲突时保留旧行——函数
// 修改后（行号/代码片段变化）fields 展示陈旧数据。改为 OR REPLACE
// 覆盖；行残留（函数删除）由 FK ON DELETE CASCADE 保证（go2o 实测
// 0 残留）。
