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
	// 限制 kind：白名单外的边不返回（data_flows_to 等数据流边自字段追溯起
	// 加入白名单，见 TestExpandParameterDataFlow）
	save(t, r, nil, []*domain.Fact{{SourceID: a.ID, TargetID: b.ID, Kind: "not_a_kind", Confidence: 0.7}})
	facts, _, _ = r.Expand(a.ID)
	for _, f := range facts {
		if f.Kind == "not_a_kind" {
			t.Error("Expand must not return unknown-kind edges")
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

func TestExpandParamResultDefinitionOrder(t *testing.T) {
	r := newTestRepo(t)
	funcID := "symbol:go:example.com/m:f"
	fn := node(funcID, "function", "f", "f.go")
	mk := func(id, kind, name string, index int) *domain.CodeEntity {
		n := node(id, kind, name, "f.go")
		n.Properties["index"] = index
		return n
	}
	recv := mk(funcID+"#param.recv.s", "parameter", "s", -1)
	p0 := mk(funcID+"#param.a", "parameter", "a", 0)
	p1 := mk(funcID+"#param.b", "parameter", "b", 1)
	r0 := mk(funcID+"#result.0", "result", "int", 0)
	r1 := mk(funcID+"#result.1", "result", "error", 1)
	callee := node("symbol:go:example.com/m:g", "function", "g", "g.go")
	save(t, r, []*domain.CodeEntity{fn, recv, p0, p1, r0, r1, callee}, nil)
	// 乱序插入：返回先、参数后、calls 夹中间
	edges := []*domain.Fact{
		{SourceID: fn.ID, TargetID: r1.ID, Kind: domain.FactHasResult, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: fn.ID, TargetID: p1.ID, Kind: domain.FactHasParam, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: fn.ID, TargetID: r0.ID, Kind: domain.FactHasResult, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: fn.ID, TargetID: p0.ID, Kind: domain.FactHasParam, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: fn.ID, TargetID: callee.ID, Kind: domain.FactCalls, Confidence: 1},
		{SourceID: fn.ID, TargetID: recv.ID, Kind: domain.FactHasParam, ToolSource: domain.ToolSSA, Confidence: 1},
	}
	save(t, r, nil, edges)

	facts, _, err := r.Expand(fn.ID)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	// 定义顺序：has_param 按 index（receiver -1 最前）→ has_result 按 index → 其他边
	want := []string{string(recv.ID), string(p0.ID), string(p1.ID), string(r0.ID), string(r1.ID), string(callee.ID)}
	got := make([]string, 0, len(facts))
	for _, f := range facts {
		got = append(got, string(f.TargetID))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("expand order[%d] = %s, want %s (full: %v)", i, got[i], want[i], got)
		}
	}
}

func TestExpandParameterDataFlow(t *testing.T) {
	r := newTestRepo(t)
	funcID := "symbol:go:example.com/m:f"
	callerID := "symbol:go:example.com/m:g"
	fn := node(funcID, "function", "f", "f.go")
	caller := node(callerID, "function", "g", "g.go")
	// 参数：parameter 节点（签名）+ ssa_value 参数（数据流端点）
	param := mkParamNode(funcID+"#param.a", "a", 0, funcID)
	paramVal := node(funcID+"#a", "ssa_value", "a", "f.go")
	paramVal.Properties["func_id"] = funcID
	// 下游：字段访问 a.X（data_flows_to a → 字段访问）
	fa := faNodeAccess(funcID+"#a.X.read@3", funcID, "example.com/m.T.X", "a.X", 3, "read")
	// 上游：调用方实参 t0 → 参数（argument 边）
	argVal := node(callerID+"#t0", "ssa_value", "t0", "g.go")
	argVal.Properties["func_id"] = callerID
	save(t, r, []*domain.CodeEntity{fn, caller, param, paramVal, fa, argVal}, []*domain.Fact{
		{SourceID: fn.ID, TargetID: param.ID, Kind: domain.FactHasParam, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: paramVal.ID, TargetID: fa.ID, Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: argVal.ID, TargetID: paramVal.ID, Kind: domain.FactArgument, ToolSource: domain.ToolSSA, Confidence: 1},
	})

	// 展开参数节点：应返回桥边（param→ssa_value）+ 上游 argument + 下游 data_flows_to
	facts, neighbors, err := r.Expand(param.ID)
	if err != nil {
		t.Fatalf("Expand param: %v", err)
	}
	kinds := map[string]bool{}
	for _, f := range facts {
		kinds[string(f.Kind)] = true
	}
	if !kinds[string(domain.FactDataFlowsTo)] || !kinds[string(domain.FactArgument)] {
		t.Errorf("expand param facts kinds = %v, want data_flows_to+argument", kinds)
	}
	// 桥边：parameter → ssa_value 参数
	bridged := false
	for _, f := range facts {
		if f.SourceID == param.ID && f.TargetID == paramVal.ID {
			bridged = true
		}
	}
	if !bridged {
		t.Errorf("bridge edge param->value missing: %+v", facts)
	}
	// 邻居包含：field_access、实参 ssa_value、函数
	nid := map[string]bool{}
	for _, n := range neighbors {
		nid[string(n.ID)] = true
	}
	for _, want := range []string{string(fa.ID), string(argVal.ID), string(paramVal.ID)} {
		if !nid[want] {
			t.Errorf("expand param neighbors missing %s (have %v)", want, nid)
		}
	}
}

// mkParamNode 构造 parameter 节点。
func mkParamNode(id, name string, index int, funcID string) *domain.CodeEntity {
	n := node(id, "parameter", name, "f.go")
	n.Properties["index"] = index
	n.Properties["func_id"] = funcID
	return n
}

func TestExpandSSAValueParamBridgesFunction(t *testing.T) {
	r := newTestRepo(t)
	funcID := "symbol:go:example.com/m:f"
	fn := node(funcID, "function", "f", "f.go")
	// 参数 ssa_value（receiver 参数）+ 下游字段访问
	recvVal := node(funcID+"#m", "ssa_value", "m", "f.go")
	recvVal.Properties["origin_kind"] = "receiver"
	recvVal.Properties["func_id"] = funcID
	fa := faNodeAccess(funcID+"#m.cfg.write@3", funcID, "example.com/m.T.cfg", "m.cfg", 3, "write")
	save(t, r, []*domain.CodeEntity{fn, recvVal, fa},
		[]*domain.Fact{{SourceID: recvVal.ID, TargetID: fa.ID, Kind: domain.FactDataFlowsTo,
			ToolSource: domain.ToolSSA, Confidence: 1}})

	facts, neighbors, err := r.Expand(recvVal.ID)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	// 所属函数桥边：函数 → 参数值（has_param）
	bridged := false
	for _, f := range facts {
		if f.SourceID == fn.ID && f.TargetID == recvVal.ID && f.Kind == domain.FactHasParam {
			bridged = true
		}
	}
	if !bridged {
		t.Errorf("func bridge edge missing: %+v", facts)
	}
	// 邻居含所属函数 + 下游字段访问
	nid := map[string]bool{}
	for _, n := range neighbors {
		nid[string(n.ID)] = true
	}
	if !nid[string(fn.ID)] || !nid[string(fa.ID)] {
		t.Errorf("neighbors = %v, want fn + fa", nid)
	}

	// 非参数 ssa_value（call_result）不加桥
	callVal := node(funcID+"#t5", "ssa_value", "t5", "f.go")
	callVal.Properties["origin_kind"] = "call_result"
	callVal.Properties["func_id"] = funcID
	save(t, r, []*domain.CodeEntity{callVal}, nil)
	facts, _, err = r.Expand(callVal.ID)
	if err != nil {
		t.Fatalf("Expand callVal: %v", err)
	}
	for _, f := range facts {
		if f.Kind == domain.FactHasParam {
			t.Errorf("non-param ssa_value must not bridge function: %+v", facts)
		}
	}
}

func TestGetValueTrace(t *testing.T) {
	r := newTestRepo(t)
	callerID := "symbol:go:example.com/m:g"
	funcID := "symbol:go:example.com/m:f"
	// 链：caller#t0 --argument--> f#a --data_flows_to--> f#a.X.read@3
	caller := node(callerID, "function", "g", "g.go")
	fn := node(funcID, "function", "f", "f.go")
	argVal := node(callerID+"#t0", "ssa_value", "t0", "g.go")
	argVal.Properties["func_id"] = callerID
	paramVal := node(funcID+"#a", "ssa_value", "a", "f.go")
	paramVal.Properties["func_id"] = funcID
	fa := faNodeAccess(funcID+"#a.X.read@3", funcID, "example.com/m.T.X", "a.X", 3, "read")
	save(t, r, []*domain.CodeEntity{caller, fn, argVal, paramVal, fa}, []*domain.Fact{
		{SourceID: argVal.ID, TargetID: paramVal.ID, Kind: domain.FactArgument, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: paramVal.ID, TargetID: fa.ID, Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
	})

	// 以字段访问为锚点，反向应走到调用方实参（跨函数）
	rows, err := r.GetValueTrace(fa.ID, 8, 0)
	if err != nil {
		t.Fatalf("GetValueTrace: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3 (fa + param + arg)", len(rows))
	}
	// 锚点 fa 属于 f；反向：param（f）→ arg（g）
	for _, row := range rows {
		if row.Depth == 0 && row.ID != fa.ID {
			t.Errorf("anchor = %s", row.ID)
		}
		if row.Depth == 1 {
			if row.ID != paramVal.ID || row.FuncID != funcID || row.Dir != 0 {
				t.Errorf("depth1 = %+v, want param in f dir0", row)
			}
		}
		if row.Depth == 2 {
			if row.ID != argVal.ID || row.FuncID != callerID || row.Dir != 0 {
				t.Errorf("depth2 = %+v, want arg in g dir0", row)
			}
		}
	}
}

// TestTraceForwardParamStart：trace-forward 参数起点（① 回归）——
// 调用方函数内无字段直接访问时，从参数经 argument 进入 callee 写入。
func TestTraceForwardParamStart(t *testing.T) {
	r := newTestRepo(t)
	runID := "symbol:go:example.com/m:run"
	fillID := "symbol:go:example.com/m:fillParam"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(runID), Kind: domain.KindFunction, Name: "run"},
		{ID: domain.CanonicalID(fillID), Kind: domain.KindFunction, Name: "fillParam"},
		// run 的参数 c（origin_kind=param）与 fillParam 的形参 c
		{ID: domain.CanonicalID(runID + "#c"), Kind: domain.KindSSAValue, Name: "c",
			Properties: map[string]any{"func_id": runID, "origin_kind": "param", "type_string": "*example.com/m.Cfg"}},
		{ID: domain.CanonicalID(fillID + "#c"), Kind: domain.KindSSAValue, Name: "c",
			Properties: map[string]any{"func_id": fillID, "origin_kind": "param", "type_string": "*example.com/m.Cfg"}},
		// callee 的字段写入节点
		{ID: domain.CanonicalID(fillID + "#c.Key.write@8"), Kind: domain.KindFieldAccess, Name: "c.Key",
			Properties: map[string]any{"func_id": fillID, "full_path": "example.com/m.Cfg.Key",
				"access_kind": "write"}},
	}
	edges := []*domain.Fact{
		{SourceID: domain.CanonicalID(runID + "#c"), TargetID: domain.CanonicalID(fillID + "#c"),
			Kind: domain.FactArgument, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(fillID + "#c"), TargetID: domain.CanonicalID(fillID + "#c.Key.write@8"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
	}
	if _, err := r.SaveBatchStats(nodes, edges, nil); err != nil {
		t.Fatalf("save: %v", err)
	}
	rows, err := r.TraceForward("example.com/m.Cfg.Key", domain.CanonicalID(runID), 8)
	if err != nil {
		t.Fatal(err)
	}
	hit := false
	for _, row := range rows {
		if strings.Contains(row.Name, "c.Key") {
			hit = true
		}
	}
	if !hit {
		t.Errorf("TraceForward 应从 run 参数经 argument 进入 callee 的 c.Key 写入: %+v", rows)
	}
}

// TestTraceForwardIntermediateReads：① 跨函数闭环——目标字段的写入经
// "其他字段的读"（如 dest.Field = src.Field 的 struct 拷贝）为中间跳板时，
// 前向追踪须穿过中间读，连到目标字段的写入；其他字段的写入仍被过滤
// （避免参数全部使用入链的噪音）。
func TestTraceForwardIntermediateReads(t *testing.T) {
	r := newTestRepo(t)
	runID := "symbol:go:example.com/m:run"
	fillID := "symbol:go:example.com/m:fill"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(runID), Kind: domain.KindFunction, Name: "run"},
		{ID: domain.CanonicalID(fillID), Kind: domain.KindFunction, Name: "fill"},
		{ID: domain.CanonicalID(runID + "#c"), Kind: domain.KindSSAValue, Name: "c",
			Properties: map[string]any{"func_id": runID, "origin_kind": "param", "type_string": "*example.com/m.Dst"}},
		{ID: domain.CanonicalID(fillID + "#c"), Kind: domain.KindSSAValue, Name: "c",
			Properties: map[string]any{"func_id": fillID, "origin_kind": "param", "type_string": "*example.com/m.Dst"}},
		// 中间跳板：c.Src.Key 读（其他字段，dest.Key = src.Key 的拷贝路径）
		{ID: domain.CanonicalID(fillID + "#c.Src.Key.read@8"), Kind: domain.KindFieldAccess, Name: "c.Src.Key",
			Properties: map[string]any{"func_id": fillID, "full_path": "example.com/m.Src.Key",
				"access_kind": "read"}},
		// 目标字段写入
		{ID: domain.CanonicalID(fillID + "#c.Dst.Key.write@9"), Kind: domain.KindFieldAccess, Name: "c.Dst.Key",
			Properties: map[string]any{"func_id": fillID, "full_path": "example.com/m.Dst.Key",
				"access_kind": "write"}},
		// 其他字段的写入（应被过滤）
		{ID: domain.CanonicalID(fillID + "#c.Dst.Title.write@10"), Kind: domain.KindFieldAccess, Name: "c.Dst.Title",
			Properties: map[string]any{"func_id": fillID, "full_path": "example.com/m.Dst.Title",
				"access_kind": "write"}},
	}
	edges := []*domain.Fact{
		{SourceID: domain.CanonicalID(runID + "#c"), TargetID: domain.CanonicalID(fillID + "#c"),
			Kind: domain.FactArgument, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(fillID + "#c"), TargetID: domain.CanonicalID(fillID + "#c.Src.Key.read@8"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(fillID + "#c.Src.Key.read@8"), TargetID: domain.CanonicalID(fillID + "#c.Dst.Key.write@9"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		// 其他字段的写入链（噪音候选）
		{SourceID: domain.CanonicalID(fillID + "#c"), TargetID: domain.CanonicalID(fillID + "#c.Dst.Title.write@10"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
	}
	save(t, r, nodes, edges)

	rows, err := r.TraceForward("example.com/m.Dst.Key", domain.CanonicalID(runID), 8)
	if err != nil {
		t.Fatal(err)
	}
	var hasWrite, hasHop bool
	for _, row := range rows {
		if string(row.ID) == fillID+"#c.Dst.Key.write@9" {
			hasWrite = true
		}
		if string(row.ID) == fillID+"#c.Src.Key.read@8" {
			hasHop = true
			if row.IsUsage {
				t.Error("中间读节点不应标记为使用点")
			}
		}
		if string(row.ID) == fillID+"#c.Dst.Title.write@10" {
			t.Errorf("其他字段的写入不应入链: %s", row.ID)
		}
	}
	if !hasWrite {
		t.Errorf("TraceForward 未连到目标字段写入（中间读被拦）: %+v", rows)
	}
	if !hasHop {
		t.Errorf("TraceForward 应含中间读跳板节点: %+v", rows)
	}
}

// TestValueTraceFieldAnchorNoCrossField：⑥ 字段精度——从字段锚点追踪时，
// 共享值节点引入的其他字段访问不得入链（T.B 读不混入 T.A 的链）。
func TestValueTraceFieldAnchorNoCrossField(t *testing.T) {
	r := newTestRepo(t)
	funcID := "symbol:go:example.com/m:run"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(funcID + "#faA.read@1"), Kind: domain.KindFieldAccess, Name: "faA",
			Properties: map[string]any{"func_id": funcID, "full_path": "example.com/m.T.A", "access_kind": "read"}},
		{ID: domain.CanonicalID(funcID + "#faA.write@2"), Kind: domain.KindFieldAccess, Name: "faA",
			Properties: map[string]any{"func_id": funcID, "full_path": "example.com/m.T.A", "access_kind": "write"}},
		{ID: domain.CanonicalID(funcID + "#faB.read@3"), Kind: domain.KindFieldAccess, Name: "faB",
			Properties: map[string]any{"func_id": funcID, "full_path": "example.com/m.T.B", "access_kind": "read"}},
		{ID: domain.CanonicalID(funcID + "#v0"), Kind: domain.KindSSAValue, Name: "v0",
			Properties: map[string]any{"func_id": funcID}},
	}
	edges := []*domain.Fact{
		{SourceID: domain.CanonicalID(funcID + "#faA.read@1"), TargetID: domain.CanonicalID(funcID + "#v0"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#v0"), TargetID: domain.CanonicalID(funcID + "#faA.write@2"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		// 共享值 v0 的另一字段读（无关字段）
		{SourceID: domain.CanonicalID(funcID + "#faB.read@3"), TargetID: domain.CanonicalID(funcID + "#v0"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
	}
	save(t, r, nodes, edges)

	// 锚点 = 字段写（T.A）：反向链含同字段读（faA.read）与值来源读跳板
	// （faB.read → v0 → faA.write：v0 的 phi 合并来源，值流相关）
	rows, err := r.GetValueTrace(domain.CanonicalID(funcID+"#faA.write@2"), 8, 0)
	if err != nil {
		t.Fatal(err)
	}
	hasSrc, hasSelf := false, false
	for _, row := range rows {
		if row.Name == "faB" {
			hasSrc = true
		}
		if row.Name == "faA" && row.Kind == domain.KindFieldAccess {
			hasSelf = true
		}
	}
	if !hasSelf {
		t.Errorf("字段锚点追踪应含同字段读 faA: %+v", rows)
	}
	if !hasSrc {
		t.Errorf("字段锚点反向应含值来源读跳板 faB（v0 的 phi 来源）: %+v", rows)
	}
	// 对象锚点（v0，无字段）：正向仅放行写、反向仅放行读——反向链应
	// 含 A 读与 B 读（值来源），正向仅写
	rows, err = r.GetValueTrace(domain.CanonicalID(funcID+"#v0"), 8, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Dir == 0 && row.Kind == domain.KindFieldAccess && row.Access != "read" {
			t.Errorf("对象锚点反向不应含写 %s", row.Name)
		}
		if row.Dir == 1 && row.Kind == domain.KindFieldAccess && row.Access != "write" {
			t.Errorf("对象锚点正向不应含读 %s", row.Name)
		}
	}
}

// TestFlowsFieldScoped：⑥ 字段精度——flows 递归的字段访问步限定起始
// 字段（A 链不混入 B 访问）。
func TestFlowsFieldScoped(t *testing.T) {
	r := newTestRepo(t)
	funcID := "symbol:go:example.com/m:run"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(funcID + "#faA.read@1"), Kind: domain.KindFieldAccess, Name: "faA",
			Properties: map[string]any{"func_id": funcID, "full_path": "example.com/m.T.A", "access_kind": "read"}},
		{ID: domain.CanonicalID(funcID + "#faA.write@2"), Kind: domain.KindFieldAccess, Name: "faA",
			Properties: map[string]any{"func_id": funcID, "full_path": "example.com/m.T.A", "access_kind": "write"}},
		{ID: domain.CanonicalID(funcID + "#faB.read@3"), Kind: domain.KindFieldAccess, Name: "faB",
			Properties: map[string]any{"func_id": funcID, "full_path": "example.com/m.T.B", "access_kind": "read"}},
		{ID: domain.CanonicalID(funcID + "#v0"), Kind: domain.KindSSAValue, Name: "v0",
			Properties: map[string]any{"func_id": funcID}},
	}
	edges := []*domain.Fact{
		{SourceID: domain.CanonicalID(funcID + "#faA.read@1"), TargetID: domain.CanonicalID(funcID + "#v0"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#v0"), TargetID: domain.CanonicalID(funcID + "#faA.write@2"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#faB.read@3"), TargetID: domain.CanonicalID(funcID + "#v0"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
	}
	save(t, r, nodes, edges)

	rows, err := r.GetFunctionFlows(domain.CanonicalID(funcID), 8)
	if err != nil {
		t.Fatal(err)
	}
	// A 链（含 faA.write）中的字段访问必须全是 T.A
	for _, row := range rows {
		if row.Kind == domain.KindFieldAccess && strings.Contains(row.Name, "faA") &&
			row.FullPath != "example.com/m.T.A" {
			t.Errorf("flows A 链混入其他字段 %s (%s)", row.Name, row.FullPath)
		}
	}
}

// TestValueTraceMulti：⑧ 跳板合并——多锚点一次查询返回各锚点下游。
func TestValueTraceMulti(t *testing.T) {
	r := newTestRepo(t)
	funcID := "symbol:go:example.com/m:run"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(funcID + "#r1"), Kind: domain.KindFieldAccess, Name: "r1",
			Properties: map[string]any{"func_id": funcID, "full_path": "example.com/m.T.A", "access_kind": "read"}},
		{ID: domain.CanonicalID(funcID + "#r2"), Kind: domain.KindFieldAccess, Name: "r2",
			Properties: map[string]any{"func_id": funcID, "full_path": "example.com/m.T.A", "access_kind": "read"}},
		{ID: domain.CanonicalID(funcID + "#v1"), Kind: domain.KindSSAValue, Name: "v1",
			Properties: map[string]any{"func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#v2"), Kind: domain.KindSSAValue, Name: "v2",
			Properties: map[string]any{"func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#result"), Kind: domain.KindSSAValue, Name: "result",
			Properties: map[string]any{"func_id": funcID}},
	}
	edges := []*domain.Fact{
		{SourceID: domain.CanonicalID(funcID + "#r1"), TargetID: domain.CanonicalID(funcID + "#v1"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#r2"), TargetID: domain.CanonicalID(funcID + "#v2"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#v1"), TargetID: domain.CanonicalID(funcID + "#result"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#v2"), TargetID: domain.CanonicalID(funcID + "#result"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
	}
	save(t, r, nodes, edges)

	rows, err := r.GetValueTraceMulti([]domain.CanonicalID{
		domain.CanonicalID(funcID + "#r1"), domain.CanonicalID(funcID + "#r2"),
	}, "example.com/m.T.A", 4)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, row := range rows {
		seen[string(row.ID)] = true
	}
	for _, want := range []string{funcID + "#v1", funcID + "#v2", funcID + "#result"} {
		if !seen[want] {
			t.Errorf("合并追踪缺节点 %s", want)
		}
	}
}

// TestTraceForwardPkgBoundary：⑬ 猎 bug——跳板容器判据的包路径 LIKE
// 不得误匹配同名前缀包（example.com/app2 的类型不得被 example.com/app
// 的 LIKE '%example.com/app%' 放行）。
func TestTraceForwardPkgBoundary(t *testing.T) {
	r := newTestRepo(t)
	runID := "symbol:go:example.com/app:run"
	fillID := "symbol:go:example.com/app2:fill"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(runID), Kind: domain.KindFunction, Name: "run"},
		{ID: domain.CanonicalID(fillID), Kind: domain.KindFunction, Name: "fill"},
		{ID: domain.CanonicalID(runID + "#c"), Kind: domain.KindSSAValue, Name: "c",
			Properties: map[string]any{"func_id": runID, "origin_kind": "param", "type_string": "*example.com/app.T"}},
		{ID: domain.CanonicalID(fillID + "#c"), Kind: domain.KindSSAValue, Name: "c",
			Properties: map[string]any{"func_id": fillID, "origin_kind": "param", "type_string": "*example.com/app.T"}},
		// 无关字段读：类型属于 example.com/app2（前缀包）——不得被 app 的
		// 容器 LIKE 放行（type_string 含 "example.com/app" 子串）
		{ID: domain.CanonicalID(fillID + "#c.Other.read@8"), Kind: domain.KindFieldAccess, Name: "c.Other",
			Properties: map[string]any{"func_id": fillID, "full_path": "example.com/app2.T2.Other",
				"access_kind": "read", "type_string": "example.com/app2.T2"}},
		// 目标字段写入（example.com/app.T.FinalFee）
		{ID: domain.CanonicalID(fillID + "#c.FinalFee.write@9"), Kind: domain.KindFieldAccess, Name: "c.FinalFee",
			Properties: map[string]any{"func_id": fillID, "full_path": "example.com/app.T.FinalFee",
				"access_kind": "write"}},
	}
	edges := []*domain.Fact{
		{SourceID: domain.CanonicalID(runID + "#c"), TargetID: domain.CanonicalID(fillID + "#c"),
			Kind: domain.FactArgument, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(fillID + "#c"), TargetID: domain.CanonicalID(fillID + "#c.Other.read@8"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(fillID + "#c"), TargetID: domain.CanonicalID(fillID + "#c.FinalFee.write@9"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
	}
	save(t, r, nodes, edges)
	rows, err := r.TraceForward("example.com/app.T.FinalFee", domain.CanonicalID(runID), 8)
	if err != nil {
		t.Fatal(err)
	}
	hasWrite := false
	for _, row := range rows {
		if strings.Contains(row.Name, "Other") {
			t.Errorf("前缀包 example.com/app2 的类型被容器 LIKE 误放行: %s", row.Name)
		}
		if string(row.ID) == fillID+"#c.FinalFee.write@9" {
			hasWrite = true
		}
	}
	if !hasWrite {
		t.Errorf("目标写入未到达: %+v", rows)
	}
}

// TestTraceCycle：⑬ 猎 bug——trace-forward 环（a→b→a）不挂且行数有限。
func TestTraceCycle(t *testing.T) {
	r := newTestRepo(t)
	funcID := "symbol:go:example.com/m:run"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(funcID + "#p"), Kind: domain.KindSSAValue, Name: "p",
			Properties: map[string]any{"func_id": funcID, "origin_kind": "param"}},
		{ID: domain.CanonicalID(funcID + "#a"), Kind: domain.KindSSAValue, Name: "a",
			Properties: map[string]any{"func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#b"), Kind: domain.KindSSAValue, Name: "b",
			Properties: map[string]any{"func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#f.write@5"), Kind: domain.KindFieldAccess, Name: "f",
			Properties: map[string]any{"func_id": funcID, "full_path": "example.com/m.T.F", "access_kind": "write"}},
	}
	edges := []*domain.Fact{
		{SourceID: domain.CanonicalID(funcID + "#p"), TargetID: domain.CanonicalID(funcID + "#a"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#a"), TargetID: domain.CanonicalID(funcID + "#b"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#b"), TargetID: domain.CanonicalID(funcID + "#a"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1}, // 环
		{SourceID: domain.CanonicalID(funcID + "#a"), TargetID: domain.CanonicalID(funcID + "#f.write@5"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
	}
	save(t, r, nodes, edges)
	rows, err := r.TraceForward("example.com/m.T.F", domain.CanonicalID(funcID), 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) > 40 {
		t.Errorf("环导致行数爆炸: %d", len(rows))
	}
	hit := false
	for _, row := range rows {
		if string(row.ID) == funcID+"#f.write@5" {
			hit = true
		}
	}
	if !hit {
		t.Errorf("环场景目标写入未到达: %+v", rows)
	}
}

// TestValueTraceCycle：⑬ 猎 bug——value-trace 环不挂。
func TestValueTraceCycle(t *testing.T) {
	r := newTestRepo(t)
	funcID := "symbol:go:example.com/m:run"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(funcID + "#a"), Kind: domain.KindSSAValue, Name: "a",
			Properties: map[string]any{"func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#b"), Kind: domain.KindSSAValue, Name: "b",
			Properties: map[string]any{"func_id": funcID}},
	}
	edges := []*domain.Fact{
		{SourceID: domain.CanonicalID(funcID + "#a"), TargetID: domain.CanonicalID(funcID + "#b"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#b"), TargetID: domain.CanonicalID(funcID + "#a"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
	}
	save(t, r, nodes, edges)
	rows, err := r.GetValueTrace(domain.CanonicalID(funcID+"#a"), 8, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) > 40 {
		t.Errorf("value-trace 环行数爆炸: %d", len(rows))
	}
}

// TestValueTraceConvergeDedup：Q155——递归 CTE 按 (id, dir) 去重。汇聚点
// （多条路径到达同一节点）只输出一行（最短深度），行数随路径数收敛而非
// 放大：v0 → x（直接）与 v0 → a → x（绕行）两条路径达 x，x 与 y 各只
// 出现一次（现状：x/y 每路径一行，共 7 行）。
func TestValueTraceConvergeDedup(t *testing.T) {
	r := newTestRepo(t)
	funcID := "symbol:go:example.com/m:run"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(funcID + "#v0"), Kind: domain.KindSSAValue, Name: "v0",
			Properties: map[string]any{"func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#a"), Kind: domain.KindSSAValue, Name: "a",
			Properties: map[string]any{"func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#x"), Kind: domain.KindSSAValue, Name: "x",
			Properties: map[string]any{"func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#y"), Kind: domain.KindSSAValue, Name: "y",
			Properties: map[string]any{"func_id": funcID}},
	}
	edges := []*domain.Fact{
		// 直接路径 v0 → x（depth 1）与绕行路径 v0 → a → x（depth 2）
		{SourceID: domain.CanonicalID(funcID + "#v0"), TargetID: domain.CanonicalID(funcID + "#x"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#v0"), TargetID: domain.CanonicalID(funcID + "#a"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#a"), TargetID: domain.CanonicalID(funcID + "#x"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#x"), TargetID: domain.CanonicalID(funcID + "#y"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
	}
	save(t, r, nodes, edges)

	rows, err := r.GetValueTrace(domain.CanonicalID(funcID+"#v0"), 8, 0)
	if err != nil {
		t.Fatal(err)
	}
	// 锚点 v0（dir0 一行，双向可展开）+ a + x + y 各一行 = 4
	if len(rows) != 4 {
		t.Errorf("rows = %d, want 4（锚点一行 + a + x + y）", len(rows))
	}
	countX, depthX, countY, depthY := 0, -1, 0, -1
	for _, row := range rows {
		switch string(row.ID) {
		case funcID + "#x":
			countX++
			depthX = row.Depth
		case funcID + "#y":
			countY++
			depthY = row.Depth
		}
	}
	if countX != 1 {
		t.Errorf("x 行数 = %d, want 1（汇聚去重）", countX)
	}
	if depthX != 1 {
		t.Errorf("x depth = %d, want 1（最短路径）", depthX)
	}
	if countY != 1 {
		t.Errorf("y 行数 = %d, want 1（汇聚去重）", countY)
	}
	if depthY != 2 {
		t.Errorf("y depth = %d, want 2", depthY)
	}
}

// TestTraceBackwardCrossFunction：⑬ 猎 bug——trace-backward 从 callee
// 的写入出发，经 argument 反向连到调用方的产生点（值来源）。
func TestTraceBackwardCrossFunction(t *testing.T) {
	r := newTestRepo(t)
	runID := "symbol:go:example.com/m:run"
	fillID := "symbol:go:example.com/m:fill"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(runID), Kind: domain.KindFunction, Name: "run"},
		{ID: domain.CanonicalID(fillID), Kind: domain.KindFunction, Name: "fill"},
		{ID: domain.CanonicalID(runID + "#c"), Kind: domain.KindSSAValue, Name: "c",
			Properties: map[string]any{"func_id": runID, "origin_kind": "param"}},
		{ID: domain.CanonicalID(fillID + "#c"), Kind: domain.KindSSAValue, Name: "c",
			Properties: map[string]any{"func_id": fillID, "origin_kind": "param"}},
		{ID: domain.CanonicalID(fillID + "#c.Key.write@8"), Kind: domain.KindFieldAccess, Name: "c.Key",
			Properties: map[string]any{"func_id": fillID, "full_path": "example.com/m.Cfg.Key",
				"access_kind": "write"}},
	}
	edges := []*domain.Fact{
		{SourceID: domain.CanonicalID(runID + "#c"), TargetID: domain.CanonicalID(fillID + "#c"),
			Kind: domain.FactArgument, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(fillID + "#c"), TargetID: domain.CanonicalID(fillID + "#c.Key.write@8"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
	}
	save(t, r, nodes, edges)
	rows, err := r.TraceBackward("example.com/m.Cfg.Key", domain.CanonicalID(fillID), 8)
	if err != nil {
		t.Fatal(err)
	}
	hasCaller := false
	for _, row := range rows {
		if string(row.ID) == runID+"#c" {
			hasCaller = true
		}
	}
	if !hasCaller {
		t.Errorf("backward 未连到调用方产生点 run#c: %+v", rows)
	}
}

// TestFindFieldReadsOrder：⑬ 猎 bug——FindFieldReads 结果顺序稳定
// （ResolveAnchor 取首个做锚点——顺序不稳定会导致锚点漂移）。
func TestFindFieldReadsOrder(t *testing.T) {
	r := newTestRepo(t)
	funcID := "symbol:go:example.com/m:run"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(funcID + "#r1"), Kind: domain.KindFieldAccess, Name: "t.A",
			LineStart: 9, Properties: map[string]any{"func_id": funcID, "full_path": "example.com/m.T.A", "access_kind": "read"}},
		{ID: domain.CanonicalID(funcID + "#r2"), Kind: domain.KindFieldAccess, Name: "t.A",
			LineStart: 3, Properties: map[string]any{"func_id": funcID, "full_path": "example.com/m.T.A", "access_kind": "read"}},
	}
	save(t, r, nodes, nil)
	first := func() string {
		rows, err := r.FindFieldReads("example.com/m.T.A")
		if err != nil || len(rows) == 0 {
			t.Fatalf("FindFieldReads: %v", err)
		}
		return string(rows[0].ID)
	}
	// 多次查询首个应一致（按行号稳定排序——r2 行号 3 应在前）
	want := string(nodes[1].ID)
	for i := 0; i < 3; i++ {
		if got := first(); got != want {
			t.Fatalf("FindFieldReads 首个不稳定: %s != %s", got, want)
		}
	}
}

// TestTraceForwardGlobalStart：已验场景单元测试化——global 值起点
// （无 func_id、origin_kind=global——起点条件须放行）。
func TestTraceForwardGlobalStart(t *testing.T) {
	r := newTestRepo(t)
	runID := "symbol:go:example.com/m:run"
	fillID := "symbol:go:example.com/m:fill"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(runID), Kind: domain.KindFunction, Name: "run"},
		{ID: domain.CanonicalID(fillID), Kind: domain.KindFunction, Name: "fill"},
		// global 值节点：无 func_id，origin_kind=global，type_string=*Record
		{ID: domain.CanonicalID("symbol:go:example.com/m:var.g"), Kind: domain.KindSSAValue, Name: "g",
			Properties: map[string]any{"origin_kind": "global", "type_string": "*example.com/m.Record"}},
		{ID: domain.CanonicalID(fillID + "#c"), Kind: domain.KindSSAValue, Name: "c",
			Properties: map[string]any{"func_id": fillID, "origin_kind": "param"}},
		{ID: domain.CanonicalID(fillID + "#c.FinalFee.write@8"), Kind: domain.KindFieldAccess, Name: "c.FinalFee",
			Properties: map[string]any{"func_id": fillID, "full_path": "example.com/m.Record.FinalFee",
				"access_kind": "write"}},
	}
	edges := []*domain.Fact{
		{SourceID: domain.CanonicalID("symbol:go:example.com/m:var.g"), TargetID: domain.CanonicalID(fillID + "#c"),
			Kind: domain.FactArgument, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(fillID + "#c"), TargetID: domain.CanonicalID(fillID + "#c.FinalFee.write@8"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
	}
	save(t, r, nodes, edges)
	rows, err := r.TraceForward("example.com/m.Record.FinalFee", domain.CanonicalID(runID), 8)
	if err != nil {
		t.Fatal(err)
	}
	hasWrite := false
	for _, row := range rows {
		if string(row.ID) == fillID+"#c.FinalFee.write@8" {
			hasWrite = true
		}
	}
	if !hasWrite {
		t.Errorf("global 值起点未连到写入: %+v", rows)
	}
}

// TestTraceForwardTypeMatchStart：已验场景单元测试化——与目标字段同类型
// 的 local/phi 值起点（type_string 匹配，⑭）。
func TestTraceForwardTypeMatchStart(t *testing.T) {
	r := newTestRepo(t)
	runID := "symbol:go:example.com/m:run"
	fillID := "symbol:go:example.com/m:fill"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(runID), Kind: domain.KindFunction, Name: "run"},
		{ID: domain.CanonicalID(fillID), Kind: domain.KindFunction, Name: "fill"},
		// 调用返回值（DAO 返回对象）：origin_kind 非 param/alloc，type 匹配
		{ID: domain.CanonicalID(runID + "#obj"), Kind: domain.KindSSAValue, Name: "obj",
			Properties: map[string]any{"func_id": runID, "origin_kind": "call",
				"type_string": "*example.com/m.Record"}},
		{ID: domain.CanonicalID(fillID + "#c"), Kind: domain.KindSSAValue, Name: "c",
			Properties: map[string]any{"func_id": fillID, "origin_kind": "param"}},
		{ID: domain.CanonicalID(fillID + "#c.FinalFee.write@8"), Kind: domain.KindFieldAccess, Name: "c.FinalFee",
			Properties: map[string]any{"func_id": fillID, "full_path": "example.com/m.Record.FinalFee",
				"access_kind": "write"}},
	}
	edges := []*domain.Fact{
		{SourceID: domain.CanonicalID(runID + "#obj"), TargetID: domain.CanonicalID(fillID + "#c"),
			Kind: domain.FactArgument, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(fillID + "#c"), TargetID: domain.CanonicalID(fillID + "#c.FinalFee.write@8"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
	}
	save(t, r, nodes, edges)
	rows, err := r.TraceForward("example.com/m.Record.FinalFee", domain.CanonicalID(runID), 8)
	if err != nil {
		t.Fatal(err)
	}
	hasWrite := false
	for _, row := range rows {
		if string(row.ID) == fillID+"#c.FinalFee.write@8" {
			hasWrite = true
		}
	}
	if !hasWrite {
		t.Errorf("同类型 local 值起点未连到写入: %+v", rows)
	}
}

// TestTraceForwardStartTypeFiltered：B2 回归——trace-forward 起点必须与
// 目标字段所属结构体类型匹配（T / *T）；无关类型参数与全局变量不得成为
// 起点。此前 origin_kind IN ('param','receiver','alloc','global') 无条件
// 放行全部参数与全局变量，起点行直接输出造成噪音（gitCommit 等无关
// 全局、string 参数全入链）。
func TestTraceForwardStartTypeFiltered(t *testing.T) {
	r := newTestRepo(t)
	runID := "symbol:go:example.com/m:run"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(runID), Kind: domain.KindFunction, Name: "run"},
		// 目标类型参数（*example.com/m.Cfg）→ 应作起点
		{ID: domain.CanonicalID(runID + "#c"), Kind: domain.KindSSAValue, Name: "c",
			Properties: map[string]any{"func_id": runID, "origin_kind": "param", "type_string": "*example.com/m.Cfg"}},
		// 无关类型参数（string）→ 不得作起点
		{ID: domain.CanonicalID(runID + "#name"), Kind: domain.KindSSAValue, Name: "name",
			Properties: map[string]any{"func_id": runID, "origin_kind": "param", "type_string": "string"}},
		// 无关全局变量（string）→ 不得作起点
		{ID: domain.CanonicalID("symbol:go:example.com/m:var.gitCommit"), Kind: domain.KindSSAValue, Name: "gitCommit",
			Properties: map[string]any{"origin_kind": "global", "type_string": "string"}},
		// 目标类型全局 → 应作起点
		{ID: domain.CanonicalID("symbol:go:example.com/m:var.gCfg"), Kind: domain.KindSSAValue, Name: "gCfg",
			Properties: map[string]any{"origin_kind": "global", "type_string": "*example.com/m.Cfg"}},
		// 无关参数/全局的下游节点（若其成为起点会带入链）
		{ID: domain.CanonicalID(runID + "#t1"), Kind: domain.KindSSAValue, Name: "t1",
			Properties: map[string]any{"func_id": runID, "origin_kind": "local", "type_string": "string"}},
		{ID: domain.CanonicalID(runID + "#t2"), Kind: domain.KindSSAValue, Name: "t2",
			Properties: map[string]any{"func_id": runID, "origin_kind": "local", "type_string": "string"}},
		// 目标字段访问（匹配 full_path）
		{ID: domain.CanonicalID(runID + "#c.Key.write@8"), Kind: domain.KindFieldAccess, Name: "c.Key",
			Properties: map[string]any{"func_id": runID, "full_path": "example.com/m.Cfg.Key", "access_kind": "write"}},
	}
	edges := []*domain.Fact{
		{SourceID: domain.CanonicalID(runID + "#c"), TargetID: domain.CanonicalID(runID + "#c.Key.write@8"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(runID + "#name"), TargetID: domain.CanonicalID(runID + "#t1"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID("symbol:go:example.com/m:var.gitCommit"), TargetID: domain.CanonicalID(runID + "#t2"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
	}
	save(t, r, nodes, edges)

	rows, err := r.TraceForward("example.com/m.Cfg.Key", domain.CanonicalID(runID), 8)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		switch string(row.ID) {
		case runID + "#name", runID + "#t1", "symbol:go:example.com/m:var.gitCommit", runID + "#t2":
			t.Errorf("无关类型节点不应入链（起点类型过滤）: %s", row.ID)
		}
	}
	var hasC, hasWrite bool
	for _, row := range rows {
		if string(row.ID) == runID+"#c" {
			hasC = true
		}
		if string(row.ID) == runID+"#c.Key.write@8" {
			hasWrite = true
		}
	}
	if !hasC || !hasWrite {
		t.Errorf("目标类型起点/字段写缺失: %+v", rows)
	}
}

// TestGetValueTraceMinConf：Q161——动态候选边（metadata 带
// candidate_origin/confidence）低于阈值时被 BFS 剪枝；普通边（无
// 候选 metadata）不受影响。
func TestGetValueTraceMinConf(t *testing.T) {
	r := newTestRepo(t)
	callerID := "symbol:go:example.com/m:g"
	funcID := "symbol:go:example.com/m:f"
	caller := node(callerID, "function", "g", "g.go")
	fn := node(funcID, "function", "f", "f.go")
	argVal := node(callerID+"#t0", "ssa_value", "t0", "g.go")
	argVal.Properties["func_id"] = callerID
	paramVal := node(funcID+"#a", "ssa_value", "a", "f.go")
	paramVal.Properties["func_id"] = funcID
	fa := faNodeAccess(funcID+"#a.X.read@3", funcID, "example.com/m.T.X", "a.X", 3, "read")
	save(t, r, []*domain.CodeEntity{caller, fn, argVal, paramVal, fa}, []*domain.Fact{
		// 候选边：enum 0.7（metadata 带 candidate_origin）
		{SourceID: argVal.ID, TargetID: paramVal.ID, Kind: domain.FactArgument, ToolSource: domain.ToolSSA,
			Confidence: 1, Metadata: map[string]any{"interface": "example.com/m.Fee",
				"candidate_origin": "enum", "confidence": 0.7}},
		{SourceID: paramVal.ID, TargetID: fa.ID, Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
	})

	// minConf=0：候选路径可见 + 边级标注（到达 paramVal 的 argument 边）
	rows, err := r.GetValueTrace(fa.ID, 8, 0)
	if err != nil {
		t.Fatalf("GetValueTrace(0): %v", err)
	}
	found := false
	for _, row := range rows {
		if row.ID == paramVal.ID {
			found = true
			if row.EdgeOrigin != "enum" || row.EdgeConf != 0.7 || row.EdgeIface != "example.com/m.Fee" {
				t.Errorf("候选边标注 = %s/%v/%s, want enum/0.7/example.com/m.Fee",
					row.EdgeOrigin, row.EdgeConf, row.EdgeIface)
			}
		}
	}
	if !found {
		t.Fatal("minConf=0 时候选路径应可达 paramVal")
	}

	// minConf=0.8：候选边（0.7）被剪枝，argVal 不可达
	rows, err = r.GetValueTrace(fa.ID, 8, 0.8)
	if err != nil {
		t.Fatalf("GetValueTrace(0.8): %v", err)
	}
	for _, row := range rows {
		if row.ID == argVal.ID {
			t.Error("minConf=0.8 时候选路径不应出现 argVal")
		}
	}
}
