package sqlite

import (
	"sync"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

// 任务 #165：serve 进程内缓存 relationGraph（按 build_id 失效）。
// 单表展开/全量查询每次重复 loadRelationGraph（go2o 530ms）——进程内
// 缓存内存图，build_id（或分析逻辑版本）变化自动重载。

// graphFixtureNodes 最小图 fixture：table_a.id.read → t4 → x →
// table_b.a_id.filter（与 TestGetAllTableRelationsCacheHit 同形态）。
func graphFixtureNodes(funcID string) []*domain.CodeEntity {
	return []*domain.CodeEntity{
		{ID: domain.CanonicalID(funcID), Kind: domain.KindFunction, Name: "find"},
		{ID: domain.CanonicalID(funcID + "#ext.sql.table_a.id.read@6"), Kind: domain.KindFieldAccess,
			Name: "table_a.id", FilePath: "a.go", LineStart: 6,
			Properties: map[string]any{"full_path": "table_a.id", "access_kind": "read",
				"type_string": "sql", "is_external": "true", "func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#t4"), Kind: domain.KindSSAValue, Name: "t4",
			Properties: map[string]any{"func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#x"), Kind: domain.KindSSAValue, Name: "x",
			Properties: map[string]any{"func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#ext.sql.table_b.a_id.filter@9"), Kind: domain.KindFieldAccess,
			Name: "table_b.a_id", FilePath: "a.go", LineStart: 9,
			Properties: map[string]any{"full_path": "table_b.a_id", "access_kind": "filter",
				"type_string": "sql", "is_external": "true", "func_id": funcID}},
	}
}

func graphFixtureEdges(funcID string) []*domain.Fact {
	return []*domain.Fact{
		{SourceID: domain.CanonicalID(funcID + "#ext.sql.table_a.id.read@6"), TargetID: domain.CanonicalID(funcID + "#t4"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#t4"), TargetID: domain.CanonicalID(funcID + "#x"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#x"), TargetID: domain.CanonicalID(funcID + "#ext.sql.table_b.a_id.filter@9"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
	}
}

// TestRelationGraphCacheReuse：同 build_id 二次调用返回同一图对象
// （缓存命中，不再查询 DB）；build_id 变化后返回新对象（失效重载）。
func TestRelationGraphCacheReuse(t *testing.T) {
	r := newTestRepo(t)
	funcID := "symbol:go:example.com/m:find"
	save(t, r, graphFixtureNodes(funcID), graphFixtureEdges(funcID))
	if err := r.Save(&domain.BuildMeta{BuildID: "b1", ToolName: "all", Status: "success"}); err != nil {
		t.Fatalf("Save build: %v", err)
	}
	g1, err := r.cachedRelationGraph()
	if err != nil || g1 == nil {
		t.Fatalf("first load = %v, %v", g1, err)
	}
	if len(g1.nodes) == 0 {
		t.Fatal("graph should be non-empty")
	}
	g2, err := r.cachedRelationGraph()
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	if g1 != g2 {
		t.Fatal("same build_id 应返回同一图对象（缓存命中）")
	}
	// 模拟增量构建：新 build_id → 失效重载（新对象）
	if err := r.Save(&domain.BuildMeta{BuildID: "b2", ToolName: "incremental", Status: "success"}); err != nil {
		t.Fatalf("Save b2: %v", err)
	}
	g3, err := r.cachedRelationGraph()
	if err != nil {
		t.Fatalf("third load: %v", err)
	}
	if g1 == g3 {
		t.Fatal("build_id 变化应重新加载（新图对象）")
	}
}

// TestRelationGraphCacheNoMeta：无 build_metadata（fixture 手动建库）时
// 不缓存——每次调用重新加载（与 relation_candidates 同语义）。
func TestRelationGraphCacheNoMeta(t *testing.T) {
	r := newTestRepo(t)
	funcID := "symbol:go:example.com/m:find"
	save(t, r, graphFixtureNodes(funcID), graphFixtureEdges(funcID))
	g1, err := r.cachedRelationGraph()
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	g2, err := r.cachedRelationGraph()
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if g1 == g2 {
		t.Fatal("无 build_metadata 不应缓存（每次新对象）")
	}
}

// TestRelationGraphCacheConcurrent：并发调用并发安全（-race 下验证），
// 全部成功且最终指向同一缓存对象。
func TestRelationGraphCacheConcurrent(t *testing.T) {
	r := newTestRepo(t)
	funcID := "symbol:go:example.com/m:find"
	save(t, r, graphFixtureNodes(funcID), graphFixtureEdges(funcID))
	if err := r.Save(&domain.BuildMeta{BuildID: "b1", ToolName: "all", Status: "success"}); err != nil {
		t.Fatalf("Save build: %v", err)
	}
	const n = 16
	gs := make([]*relationGraph, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			g, err := r.cachedRelationGraph()
			if err != nil {
				t.Errorf("goroutine %d: %v", i, err)
				return
			}
			gs[i] = g
		}(i)
	}
	wg.Wait()
	for i := 1; i < n; i++ {
		if gs[i] == nil || gs[0] == nil {
			t.Fatalf("nil graph at %d", i)
		}
		if gs[i] != gs[0] {
			t.Fatalf("并发调用应命中同一缓存对象（%d ≠ 0）", i)
		}
	}
}

// TestGraphCacheThreshold：大图阈值判定——超过节点/边上限不缓存
// （每请求重载，防 ~100MB 常驻膨胀）。
func TestGraphCacheThreshold(t *testing.T) {
	// 纯函数判定：shouldCacheGraph(g, maxNodes, maxEdges)
	small := &relationGraph{
		nodes:   map[string]*relNode{"a": {}, "b": {}},
		allOut:  map[string][]string{"a": {"b"}},
		dataAdj: map[string][]string{"a": {"b"}, "b": {"a"}},
	}
	if !shouldCacheGraph(small, 10, 10) {
		t.Fatal("小图应可缓存")
	}
	bigNodes := &relationGraph{nodes: map[string]*relNode{"a": {}, "b": {}}, allOut: map[string][]string{"a": {"b"}}}
	if shouldCacheGraph(bigNodes, 1, 10) {
		t.Fatal("超节点上限不应缓存")
	}
	bigEdges := &relationGraph{
		nodes:   map[string]*relNode{"a": {}, "b": {}},
		allOut:  map[string][]string{"a": {"b", "c"}, "b": {"c"}, "c": {}},
		dataAdj: map[string][]string{},
	}
	if shouldCacheGraph(bigEdges, 10, 2) {
		t.Fatal("超边数上限不应缓存")
	}
}
