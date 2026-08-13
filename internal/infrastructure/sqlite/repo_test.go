package sqlite

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

// newTestRepo 打开临时目录下的数据库并创建仓储。
func newTestRepo(t *testing.T) *Repo {
	t.Helper()
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return NewRepo(db)
}

// save 便捷写入节点 + 边。
func save(t *testing.T, r *Repo, nodes []*domain.CodeEntity, edges []*domain.Fact) {
	t.Helper()
	if _, err := r.SaveBatchStats(nodes, edges, nil); err != nil {
		t.Fatalf("SaveBatchStats: %v", err)
	}
}

func node(id, kind, name, file string) *domain.CodeEntity {
	return &domain.CodeEntity{
		ID:       domain.CanonicalID(id),
		Kind:     domain.EntityKind(kind),
		Name:     name,
		FilePath: file,
		Properties: map[string]any{
			"signature": "sig:" + name,
		},
	}
}

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
	// 不存在 → ErrNotFound
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
	// 边指向不存在的节点（外键冲突）→ 跳过并计数
	res, err := r.SaveBatchStats(nil, []*domain.Fact{{
		SourceID: "symbol:go:example.com/m:a",
		TargetID: "symbol:go:example.com/m:b",
		Kind:     domain.FactCalls,
	}}, nil)
	if err != nil {
		t.Fatalf("SaveBatchStats: %v", err)
	}
	if res.SkippedEdges != 1 {
		t.Errorf("SkippedEdges = %d, want 1", res.SkippedEdges)
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
	// 同义边合并：保留最高置信度（0.5 后再写 0.9 → 0.9）
	facts, err := r.GetCallees(a.ID, 1, 0.1)
	if err != nil {
		t.Fatalf("GetCallees: %v", err)
	}
	if len(facts) != 1 || facts[0].Confidence != 0.9 {
		t.Errorf("facts = %+v, want single edge with confidence 0.9", facts)
	}
	// 低于已有置信度的写入不降级
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
	// 精确匹配
	got, err := r.GetSymbolByName("main")
	if err != nil || len(got) != 1 || got[0].Name != "main" {
		t.Errorf("exact match = %+v, err %v", got, err)
	}
	// 模糊匹配（runLoop 包含 run）
	got, err = r.GetSymbolByName("run")
	if err != nil || len(got) == 0 {
		t.Errorf("fuzzy match = %+v, err %v", got, err)
	}
	// 无结果 → 空列表非错误
	got, err = r.GetSymbolByName("zzz_none")
	if err != nil || len(got) != 0 {
		t.Errorf("no match = %+v, err %v", got, err)
	}
}

func TestCallersCalleesDepthAndConfidence(t *testing.T) {
	r := newTestRepo(t)
	// a → b → c
	nodes := []*domain.CodeEntity{
		node("symbol:go:example.com/m:a", "function", "a", "a.go"),
		node("symbol:go:example.com/m:b", "function", "b", "b.go"),
		node("symbol:go:example.com/m:c", "function", "c", "c.go"),
	}
	save(t, r, nodes, nil)
	low := &domain.Fact{SourceID: "symbol:go:example.com/m:a", TargetID: "symbol:go:example.com/m:b", Kind: domain.FactCalls, Confidence: 0.5}
	high := &domain.Fact{SourceID: "symbol:go:example.com/m:b", TargetID: "symbol:go:example.com/m:c", Kind: domain.FactCalls, Confidence: 0.9}
	save(t, r, nil, []*domain.Fact{low, high})

	// 置信度过滤：min 0.8 时 a→b 被滤掉
	callees, err := r.GetCallees("symbol:go:example.com/m:a", 2, 0.8)
	if err != nil {
		t.Fatalf("GetCallees: %v", err)
	}
	if len(callees) != 0 {
		t.Errorf("callees with min 0.8 = %+v, want none (a->b has 0.5)", callees)
	}
	// min 0.1：深度 1 只有 b，深度 2 有 b、c
	callees, _ = r.GetCallees("symbol:go:example.com/m:a", 1, 0.1)
	if len(callees) != 1 {
		t.Errorf("depth1 callees = %+v", callees)
	}
	callees, _ = r.GetCallees("symbol:go:example.com/m:a", 2, 0.1)
	if len(callees) != 2 {
		t.Errorf("depth2 callees = %+v, want 2", callees)
	}
	// callers 方向：c 的调用者（深度 2）包含 a、b
	callers, err := r.GetCallers("symbol:go:example.com/m:c", 2, 0.1)
	if err != nil {
		t.Fatalf("GetCallers: %v", err)
	}
	if len(callers) != 2 {
		t.Errorf("callers of c = %+v, want 2", callers)
	}
}

func TestGetImpact(t *testing.T) {
	r := newTestRepo(t)
	// a → b → c（calls）+ a → d（imports）
	nodes := []*domain.CodeEntity{
		node("symbol:go:example.com/m:a", "function", "a", "a.go"),
		node("symbol:go:example.com/m:b", "function", "b", "b.go"),
		node("symbol:go:example.com/m:c", "function", "c", "c.go"),
		node("symbol:go:example.com/m:d", "package", "d", "d.go"),
	}
	save(t, r, nodes, nil)
	edges := []*domain.Fact{
		{SourceID: "symbol:go:example.com/m:a", TargetID: "symbol:go:example.com/m:b", Kind: domain.FactCalls, Confidence: 1},
		{SourceID: "symbol:go:example.com/m:b", TargetID: "symbol:go:example.com/m:c", Kind: domain.FactCalls, Confidence: 1},
		{SourceID: "symbol:go:example.com/m:a", TargetID: "symbol:go:example.com/m:d", Kind: domain.FactImports, Confidence: 1},
	}
	save(t, r, nil, edges)

	impact, err := r.GetImpact("symbol:go:example.com/m:b", 2)
	if err != nil {
		t.Fatalf("GetImpact: %v", err)
	}
	ids := map[domain.CanonicalID]bool{}
	for _, n := range impact {
		ids[n.ID] = true
	}
	if !ids["symbol:go:example.com/m:a"] || !ids["symbol:go:example.com/m:c"] {
		t.Errorf("impact of b = %v, want a and c", ids)
	}
}

func TestGetRoots(t *testing.T) {
	r := newTestRepo(t)
	nodes := []*domain.CodeEntity{
		node("symbol:go:example.com/m:main", "function", "main", "cmd/main.go"),
		// 测试包的 main（id 含 .test:main）应排除
		node("symbol:go:example.com/m.test:main", "function", "main", "main_test.go"),
		// HTTP 服务入口（serves_http 标记）
		func() *domain.CodeEntity {
			n := node("symbol:go:example.com/m/server:serve", "function", "serve", "server/server.go")
			n.Properties["serves_http"] = "true"
			return n
		}(),
		// gRPC 服务入口
		func() *domain.CodeEntity {
			n := node("symbol:go:example.com/m/grpc:serve", "function", "serve", "grpc/grpc.go")
			n.Properties["serves_grpc"] = "true"
			return n
		}(),
		// 仓库外文件（../ 前缀）应排除
		node("symbol:go:example.com/other:main", "function", "main", "../other/main.go"),
		// _test.go 文件应排除
		node("symbol:go:example.com/m/t:helper", "function", "helper", "t/helper_test.go"),
		// 普通函数（无标记）不是入口
		node("symbol:go:example.com/m/util:helper", "function", "helper", "util/helper.go"),
	}
	save(t, r, nodes, nil)

	roots, err := r.GetRoots()
	if err != nil {
		t.Fatalf("GetRoots: %v", err)
	}
	ids := map[domain.CanonicalID]bool{}
	for _, n := range roots {
		ids[n.ID] = true
	}
	if !ids["symbol:go:example.com/m:main"] {
		t.Error("roots missing main")
	}
	if !ids["symbol:go:example.com/m/server:serve"] || !ids["symbol:go:example.com/m/grpc:serve"] {
		t.Error("roots missing http/grpc entries")
	}
	if ids["symbol:go:example.com/m.test:main"] {
		t.Error("test main must be excluded")
	}
	if ids["symbol:go:example.com/other:main"] || ids["symbol:go:example.com/m/t:helper"] {
		t.Error("out-of-module / _test.go files must be excluded")
	}
	if ids["symbol:go:example.com/m/util:helper"] {
		t.Error("plain function must not be a root")
	}
}

func TestGetFrameworkStructs(t *testing.T) {
	r := newTestRepo(t)
	// struct S 的方法 Handle 未被跨文件调用 → S 是框架回调
	s := node("symbol:go:example.com/m:srv:S", "struct", "S", "srv/s.go")
	// struct T 的方法 Use 被其他文件调用 → T 不是
	t2 := node("symbol:go:example.com/m:svc:T", "struct", "T", "svc/t.go")
	mHandle := node("symbol:go:example.com/m:srv:(S).Handle", "method", "(S).Handle", "srv/s.go")
	mUse := node("symbol:go:example.com/m:svc:(T).Use", "method", "(T).Use", "svc/t.go")
	caller := node("symbol:go:example.com/m:main", "function", "main", "main.go")
	save(t, r, []*domain.CodeEntity{s, t2, mHandle, mUse, caller}, nil)
	// T.Use 被 main（其他文件）调用
	save(t, r, nil, []*domain.Fact{{
		SourceID: caller.ID, TargetID: mUse.ID, Kind: domain.FactCalls, Confidence: 0.8,
	}})

	structs, err := r.GetFrameworkStructs()
	if err != nil {
		t.Fatalf("GetFrameworkStructs: %v", err)
	}
	seen := map[domain.CanonicalID]bool{}
	for _, n := range structs {
		seen[n.ID] = true
	}
	if !seen[s.ID] {
		t.Error("S (methods not cross-file called) should be framework struct")
	}
	if seen[t2.ID] {
		t.Error("T (method called from other file) must not be framework struct")
	}
	if n := structs[0]; n.Properties["framework"] != "true" {
		t.Errorf("framework property missing: %+v", n.Properties)
	}
}

func TestExpand(t *testing.T) {
	r := newTestRepo(t)
	a := node("symbol:go:example.com/m:a", "function", "a", "a.go")
	b := node("symbol:go:example.com/m:b", "function", "b", "b.go")
	c := node("symbol:go:example.com/m:c", "function", "c", "c.go")
	save(t, r, []*domain.CodeEntity{a, b, c}, nil)
	edges := []*domain.Fact{
		{SourceID: a.ID, TargetID: b.ID, Kind: domain.FactCalls, Confidence: 0.8, Metadata: map[string]any{"line_num": 3}},
		{SourceID: c.ID, TargetID: a.ID, Kind: domain.FactCalls, Confidence: 0.8},
		{SourceID: a.ID, TargetID: c.ID, Kind: domain.FactPassesTo, Confidence: 0.8},
	}
	save(t, r, nil, edges)

	facts, neighbors, err := r.Expand(a.ID)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if len(facts) != 3 {
		t.Errorf("facts = %+v, want 3", facts)
	}
	if len(neighbors) != 2 {
		t.Errorf("neighbors = %+v, want 2 (dedup)", neighbors)
	}
	// 邻居集合去重且不含自身
	ids := map[domain.CanonicalID]bool{}
	for _, n := range neighbors {
		if n.ID == a.ID {
			t.Error("neighbors must not include self")
		}
		ids[n.ID] = true
	}
	if !ids[b.ID] || !ids[c.ID] {
		t.Error("neighbors should include b and c")
	}
	// 无邻居的孤立节点 → 空结果
	facts, neighbors, err = r.Expand(node("x", "function", "x", "x.go").ID)
	if err != nil || len(facts) != 0 {
		t.Errorf("expand missing node = %+v, %v", facts, err)
	}
	// 限制 kind：未知 kind 的边不返回（如 data_flows_to 不在列表）
	save(t, r, nil, []*domain.Fact{{SourceID: a.ID, TargetID: b.ID, Kind: domain.FactDataFlowsTo, Confidence: 0.7}})
	facts, _, _ = r.Expand(a.ID)
	for _, f := range facts {
		if f.Kind == domain.FactDataFlowsTo {
			t.Error("Expand must not return data_flows_to edges")
		}
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
	// 空库 → ErrNotFound
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
	// 后写覆盖（INSERT OR REPLACE）
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
	// 级联：以 a 为端点的边也被删除
	callees, err := r.GetCallees(b.ID, 1, 0.1)
	if err != nil {
		t.Fatalf("GetCallees: %v", err)
	}
	if len(callees) != 0 {
		t.Errorf("cascade: callees of b = %+v, want none", callees)
	}
	// b 保留
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
	// 模拟旧版本库
	if _, err := db.Exec("PRAGMA user_version = 99"); err != nil {
		t.Fatalf("set user_version: %v", err)
	}
	db.Close()

	if _, err := Open(dir); err == nil {
		t.Error("Open should fail on schema version mismatch")
	}
	// RepoPath 返回
	if db2, _ := Open(t.TempDir()); db2.RepoPath() != filepath.Clean(t.TempDir()) {
		// 不深究，只保证调用不 panic
		db2.Close()
	}
}
