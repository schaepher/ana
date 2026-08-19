package sqlite

import (
	"errors"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

// progressFixture 构造小型关系图：table_a.id.read → table_b.a_id.filter
// （query 键关联），供进度测试复用。
func progressFixture(t *testing.T) *Repo {
	t.Helper()
	r := newTestRepo(t)
	funcID := "symbol:go:example.com/m:find"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(funcID), Kind: domain.KindFunction, Name: "find"},
		{ID: domain.CanonicalID(funcID + "#ext.sql.table_a.id.read@6"), Kind: domain.KindFieldAccess,
			Name: "table_a.id", FilePath: "a.go", LineStart: 6,
			Properties: map[string]any{"full_path": "table_a.id", "access_kind": "read",
				"type_string": "sql", "is_external": "true", "func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#ext.sql.table_b.a_id.filter@9"), Kind: domain.KindFieldAccess,
			Name: "table_b.a_id", FilePath: "a.go", LineStart: 9,
			Properties: map[string]any{"full_path": "table_b.a_id", "access_kind": "filter",
				"type_string": "sql", "is_external": "true", "func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#t4"), Kind: domain.KindSSAValue, Name: "t4",
			Properties: map[string]any{"func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#x"), Kind: domain.KindSSAValue, Name: "x",
			Properties: map[string]any{"func_id": funcID}},
	}
	edges := []*domain.Fact{
		{SourceID: domain.CanonicalID(funcID + "#ext.sql.table_a.id.read@6"), TargetID: domain.CanonicalID(funcID + "#t4"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#t4"), TargetID: domain.CanonicalID(funcID + "#x"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#x"), TargetID: domain.CanonicalID(funcID + "#ext.sql.table_b.a_id.filter@9"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
	}
	if _, err := r.SaveBatchStats(nodes, edges, nil); err != nil {
		t.Fatalf("save: %v", err)
	}
	// build_metadata（cacheKey 依赖）
	if _, err := r.Exec(`INSERT INTO build_metadata (build_id, commit_sha, tool_name, status, duration_ms, error_message, nodes_count, edges_count) VALUES ('b1', 'c1', 'all', 'success', 1, '', 5, 3)`); err != nil {
		t.Fatalf("meta: %v", err)
	}
	return r
}

// TestRelationProgressFlow：PrecomputeAllRelations 完整流程——进度从
// running 推进到 done（回调递增），完成后 GetAllTableRelations 命中
// 缓存返回数据（不再现场计算）。
func TestRelationProgressFlow(t *testing.T) {
	r := progressFixture(t)
	// 未计算：GetAllTableRelations 返回 ErrRelationInProgress（不现场算）
	if _, err := r.GetAllTableRelations("full"); !errors.Is(err, ErrRelationInProgress) {
		t.Fatalf("未计算时应返回 ErrRelationInProgress，got %v", err)
	}
	// 预计算（进度回调递增）
	var progress []int
	if err := r.PrecomputeAllRelations(func(done, total int) { progress = append(progress, done) }); err != nil {
		t.Fatal(err)
	}
	if len(progress) == 0 || progress[len(progress)-1] != 2 {
		t.Fatalf("进度回调应推进到 total=2，got %v", progress)
	}
	// 完成后状态 done
	p, err := r.RelationProgress()
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != "done" || p.Done != 2 || p.Total != 2 {
		t.Fatalf("进度应 done 2/2，got %+v", p)
	}
	// 完成后再查询：返回数据（缓存命中，含规则合并）
	rels, err := r.GetAllTableRelations("full")
	if err != nil {
		t.Fatalf("done 后应返回数据：%v", err)
	}
	found := false
	for _, rel := range rels {
		// Q218：id 读出 → a_id 过滤，taint 呼应升 fk（非 query）
		if rel.FromTable == "table_a" && rel.FromCol == "id" &&
			rel.ToTable == "table_b" && rel.ToCol == "a_id" && rel.Type == domain.RelationFK {
			found = true
		}
	}
	if !found {
		detail := []string{}
		for _, rel := range rels {
			detail = append(detail, rel.FromTable+"."+rel.FromCol+"→"+rel.ToTable+"."+rel.ToCol+":"+rel.Type)
		}
		t.Fatalf("table_a.id → table_b.a_id [query] 应出现，got %v", detail)
	}
}

// TestStartRelationComputeIfNeeded：查询端自动兜底——unknown 启动；
// done 不启动；running（新鲜）不重复启动。
func TestStartRelationComputeIfNeeded(t *testing.T) {
	r := progressFixture(t)
	// unknown → 启动
	started, err := r.StartRelationComputeIfNeeded()
	if err != nil || !started {
		t.Fatalf("unknown 应启动计算：started=%v err=%v", started, err)
	}
	// running（刚抢占，fresh）→ 不重复启动
	started, err = r.StartRelationComputeIfNeeded()
	if err != nil || started {
		t.Fatalf("running 活跃中不应重复启动：started=%v err=%v", started, err)
	}
	// 完成后再调 → 不启动
	if err := r.PrecomputeAllRelations(nil); err != nil {
		t.Fatal(err)
	}
	started, err = r.StartRelationComputeIfNeeded()
	if err != nil || started {
		t.Fatalf("done 后不应启动：started=%v err=%v", started, err)
	}
}
