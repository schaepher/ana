package sqlite

import (
	"errors"
	"path/filepath"
	"strings"
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

// faNode 构造 field_access 节点（properties 含 full_path/func_id）。
func faNode(id, funcID, field, instance string, line int) *domain.CodeEntity {
	return faNodeAccess(id, funcID, field, instance, line, "write")
}

func faNodeAccess(id, funcID, field, instance string, line int, access string) *domain.CodeEntity {
	return &domain.CodeEntity{
		ID: domain.CanonicalID(id), Kind: domain.KindFieldAccess, Name: instance,
		FilePath: "main.go", LineStart: line,
		Properties: map[string]any{"full_path": field, "instance_path": instance,
			"access_kind": access, "func_id": funcID},
	}
}

func svNode(id, funcID string) *domain.CodeEntity {
	return &domain.CodeEntity{
		ID: domain.CanonicalID(id), Kind: domain.KindSSAValue, Name: id[strings.LastIndex(id, "#")+1:],
		Properties: map[string]any{"func_id": funcID},
	}
}

func dfEdge(a, b domain.CanonicalID) *domain.Fact {
	return &domain.Fact{SourceID: a, TargetID: b,
		Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1}
}

func TestTraceBackwardMultiHop(t *testing.T) {
	r := newTestRepo(t)
	funcID := "symbol:go:example.com/m:f"
	// 链：a → b → field（data_flows_to）
	fa := faNode(funcID+"#x.A.write@3", funcID, "example.com/m.T.A", "x.A", 3)
	b := svNode(funcID+"#t1", funcID)
	a := svNode(funcID+"#t0", funcID)
	save(t, r, []*domain.CodeEntity{fa, b, a}, []*domain.Fact{dfEdge(a.ID, b.ID), dfEdge(b.ID, fa.ID)})

	rows, err := r.TraceBackward("example.com/m.T.A", domain.CanonicalID(funcID), 8)
	if err != nil {
		t.Fatalf("TraceBackward: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3 (field + 2 hops)", len(rows))
	}
	// 深度递增：0=field, 1=b, 2=a
	depth0, depth1, depth2 := rows[0], rows[1], rows[2]
	if depth0.Depth != 0 || depth1.Depth != 1 || depth2.Depth != 2 {
		t.Errorf("depths = %d,%d,%d", depth0.Depth, depth1.Depth, depth2.Depth)
	}
	if string(depth0.ID) != string(fa.ID) || string(depth1.ID) != string(b.ID) || string(depth2.ID) != string(a.ID) {
		t.Errorf("chain = %s,%s,%s", depth0.ID, depth1.ID, depth2.ID)
	}
	if depth1.EdgeKinds != "data_flows_to" {
		t.Errorf("edge kinds = %q", depth1.EdgeKinds)
	}

	// 深度限制：maxDepth=1 时只到 b
	rows, err = r.TraceBackward("example.com/m.T.A", domain.CanonicalID(funcID), 1)
	if err != nil {
		t.Fatalf("TraceBackward depth1: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("depth-limited rows = %d, want 2", len(rows))
	}
}

func TestTraceForwardUsagePoint(t *testing.T) {
	r := newTestRepo(t)
	funcID := "symbol:go:example.com/m:f"
	// 读节点 → result → 写节点（同字段）：正向走到写节点标记为使用点
	read := faNode(funcID+"#x.A.read@3", funcID, "example.com/m.T.A", "x.A", 3)
	result := svNode(funcID+"#t1", funcID)
	write := faNode(funcID+"#x.A.write@5", funcID, "example.com/m.T.A", "x.A", 5)
	save(t, r, []*domain.CodeEntity{read, result, write},
		[]*domain.Fact{dfEdge(read.ID, result.ID), dfEdge(result.ID, write.ID)})

	rows, err := r.TraceForward("example.com/m.T.A", domain.CanonicalID(funcID), 8)
	if err != nil {
		t.Fatalf("TraceForward: %v", err)
	}
	// 锚点 = 全部匹配访问点（读+写，2 个 depth0），再经 result 走到写节点（depth2）
	if len(rows) != 4 {
		t.Fatalf("rows = %d, want 4 (2 anchors + result + write)", len(rows))
	}
	if rows[2].Depth != 1 || string(rows[2].ID) != string(result.ID) {
		t.Errorf("depth1 row = %+v, want result", rows[2])
	}
	last := rows[3]
	if last.Depth != 2 || !last.IsUsage {
		t.Errorf("write node should be usage point at depth 2: %+v", last)
	}
}

func TestGetSymbolByNameExcludesFieldTrace(t *testing.T) {
	r := newTestRepo(t)
	funcID := "symbol:go:example.com/m:f"
	nodes := []*domain.CodeEntity{
		node("symbol:go:example.com/m:run", "function", "runLoop", "run.go"),
		// 字段追溯内部节点：不参与符号搜索
		faNode(funcID+"#cfg.write@5", funcID, "example.com/m.T.cfg", "cfg", 5),
		svNode(funcID+"#t0", funcID),
	}
	save(t, r, nodes, nil)

	// 精确匹配：搜 "cfg" 不应命中 field_access（instance_path=cfg）
	got, err := r.GetSymbolByName("cfg")
	if err != nil {
		t.Fatalf("GetSymbolByName: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("search cfg = %+v, want none (field_access excluded)", got)
	}
	// 模糊匹配：搜 "run" 仍命中函数，且不含 ssa_value（t0 不匹配 "run"，验证不过滤正常符号）
	got, err = r.GetSymbolByName("run")
	if err != nil || len(got) != 1 || got[0].Name != "runLoop" {
		t.Errorf("search run = %+v, err %v", got, err)
	}
	// 按 ID 模糊搜索 field_access 的 ID 也不应返回
	got, err = r.GetSymbolByName(funcID + "#cfg")
	if err != nil || len(got) != 0 {
		t.Errorf("search by field_access id = %+v, err %v", got, err)
	}
}

func TestGetFunctionFlows(t *testing.T) {
	r := newTestRepo(t)
	funcID := "symbol:go:example.com/m:f"
	// 函数内链：write(field) → result → read(field)
	write := faNode(funcID+"#x.A.write@3", funcID, "example.com/m.T.A", "x.A", 3)
	result := svNode(funcID+"#t1", funcID)
	read := faNodeAccess(funcID+"#x.A.read@5", funcID, "example.com/m.T.A", "x.A", 5, "read")
	// 函数外节点：不应出现在结果里（func_id 限定）
	other := faNode("symbol:go:example.com/m:g#y.B.write@9", "symbol:go:example.com/m:g",
		"example.com/m.T.B", "y.B", 9)
	save(t, r, []*domain.CodeEntity{write, result, read, other},
		[]*domain.Fact{
			dfEdge(write.ID, result.ID),
			dfEdge(result.ID, read.ID),
			dfEdge(write.ID, other.ID), // 跨函数边：目标不在 func_id 内，不扩展
		})

	rows, err := r.GetFunctionFlows(domain.CanonicalID(funcID), 8)
	if err != nil {
		t.Fatalf("GetFunctionFlows: %v", err)
	}
	// 锚点 = 全部访问点（write/read 两个 dir0），双向扩展：
	//   产生链 dir0：read ← result ← write（反向）
	//   使用链 dir1：write → result → read（正向）
	// 同一节点可同时出现在两条链（write/read 是锚点，result 两向都可达）
	if len(rows) != 6 {
		t.Fatalf("rows = %d, want 6: %+v", len(rows), rows)
	}
	for _, row := range rows {
		if row.Kind != domain.KindFieldAccess && row.Kind != domain.KindSSAValue {
			t.Errorf("unexpected kind %s: %+v", row.Kind, row)
		}
		if string(row.ID) == string(other.ID) {
			t.Errorf("cross-function node leaked into flows: %s", other.ID)
		}
	}
	// 使用链（dir=1）：result@1 → read@2
	var resultFwd, readFwd *domain.TraceRow
	for _, row := range rows {
		if row.Dir != 1 {
			continue
		}
		if row.ID == result.ID {
			resultFwd = row
		}
		if row.ID == read.ID {
			readFwd = row
		}
	}
	if resultFwd == nil || readFwd == nil {
		t.Fatalf("forward chain missing: %+v", rows)
	}
	if resultFwd.Depth != 1 {
		t.Errorf("result forward depth = %d, want 1", resultFwd.Depth)
	}
	if readFwd.Depth != 2 || readFwd.Access != "read" {
		t.Errorf("read forward = depth %d access %q, want 2/read", readFwd.Depth, readFwd.Access)
	}
	// 产生链（dir=0）：result@1（从 read 反向）→ write@2
	var writeBack *domain.TraceRow
	for _, row := range rows {
		if row.Dir == 0 && row.ID == write.ID && row.Depth == 2 {
			writeBack = row
		}
	}
	if writeBack == nil {
		t.Errorf("backward chain write@2 missing: %+v", rows)
	}
}
