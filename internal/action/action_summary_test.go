package action

import (
	"strings"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
)

func TestExportIndex(t *testing.T) {
	a, _ := seedRepo(t)
	idx, err := a.ExportIndex()
	if err != nil {
		t.Fatal(err)
	}
	ef, ok := idx["example.com/m.T.A"]
	if !ok {
		t.Fatalf("export missing field: %v", idx)
	}
	if len(ef.Producers) != 1 || len(ef.Consumers) != 1 {
		t.Errorf("producers/consumers = %d/%d", len(ef.Producers), len(ef.Consumers))
	}
	if !strings.Contains(ef.Producers[0].Function, "main") {
		t.Errorf("producer = %+v", ef.Producers[0])
	}
}

// TestSummaryChainWriteAnchorDownstream：③ 回归——写锚点的下游经
// 同 full_path 读节点跳板接入使用链（consume）。
func TestSummaryChainWriteAnchorDownstream(t *testing.T) {
	a, dir := seedRepo(t)
	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	r := sqlite.NewRepo(db)
	funcID := "symbol:go:example.com/m:main"
	writeNode := &domain.CodeEntity{ID: domain.CanonicalID(funcID + "#t.A.write@5"),
		Kind: domain.KindFieldAccess, Name: "t.A", FilePath: "main.go", LineStart: 5,
		Properties: map[string]any{"full_path": "example.com/m.T.A", "instance_path": "t.A",
			"access_kind": "write", "func_id": funcID}}
	readNode := &domain.CodeEntity{ID: domain.CanonicalID(funcID + "#t.A.read@7"),
		Kind: domain.KindFieldAccess, Name: "t.A", FilePath: "main.go", LineStart: 7,
		Properties: map[string]any{"full_path": "example.com/m.T.A", "instance_path": "t.A",
			"access_kind": "read", "func_id": funcID}}
	result := &domain.CodeEntity{ID: domain.CanonicalID(funcID + "#t1"), Kind: domain.KindSSAValue,
		Name: "t1", Properties: map[string]any{"func_id": funcID}}
	if _, err := r.SaveBatchStats([]*domain.CodeEntity{writeNode, readNode, result}, []*domain.Fact{
		{SourceID: readNode.ID, TargetID: result.ID, Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
	}, nil); err != nil {
		t.Fatalf("save: %v", err)
	}

	steps, err := a.SummaryChain(writeNode.ID)
	if err != nil {
		t.Fatal(err)
	}
	hasWrite, hasConsume := false, false
	for _, st := range steps {
		if st.Kind == "write" {
			hasWrite = true
		}
		if st.Kind == "consume" {
			hasConsume = true
		}
	}
	if !hasWrite || !hasConsume {
		t.Errorf("写锚点 summary 应含 write 与 consume（下游读节点）: %+v", steps)
	}
}

// TestValueTraceDispatchMark：Q157 P1——value-trace 候选派发标注。
// 行所属函数是 dispatch_to 边 target（接口候选实现）时标记来源与
// 置信度（链路混入多候选实现时可区分）。
func TestValueTraceDispatchMark(t *testing.T) {
	acts, dir := seedRepo(t)
	_ = acts

	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	r := sqlite.NewRepo(db)
	if _, err := r.SaveBatchStats([]*domain.CodeEntity{
		{ID: "symbol:go:example.com/m:Iface", Kind: domain.KindInterface, Name: "Iface", FilePath: "iface.go"},
	}, []*domain.Fact{
		{SourceID: "symbol:go:example.com/m:Iface", TargetID: "symbol:go:example.com/m:main",
			Kind: domain.FactDispatchTo, ToolSource: domain.ToolSSA, Confidence: 0.9,
			Metadata: map[string]any{"origin": "register"}},
	}, nil); err != nil {
		t.Fatalf("save dispatch: %v", err)
	}

	rows, err := acts.ValueTrace(domain.CanonicalID("symbol:go:example.com/m:main#t.A.write@5"), 8, 0, false)
	if err != nil {
		t.Fatalf("ValueTrace: %v", err)
	}
	marked := false
	for _, r := range rows {
		if r.FuncID == "symbol:go:example.com/m:main" {
			if !r.DispatchCandidate {
				t.Errorf("main 行应标注候选派发: %+v", r)
			}
			if r.DispatchOrigin != "register" || r.DispatchConf != 0.9 {
				t.Errorf("候选元数据 = %s %.1f, want register 0.9", r.DispatchOrigin, r.DispatchConf)
			}
			marked = true
		}
	}
	if !marked {
		t.Error("value-trace 未包含 main 函数行")
	}
}

// TestRelationsAll：全库关联聚合（Q160）——多表 BFS 结果合并去重，
// 同列对保留 hops 最小 + query 类型（如 A.id → B 在 B 视角可能重复出现）。
func TestRelationsAll(t *testing.T) {
	acts, dir := seedRepo(t)
	_ = acts
	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	r := sqlite.NewRepo(db)
	funcID := domain.CanonicalID("symbol:go:example.com/m:find")
	nodes := []*domain.CodeEntity{
		{ID: funcID, Kind: domain.KindFunction, Name: "find", FilePath: "a.go"},
		{ID: funcID + "#ext.sql.table_a.id.read@6", Kind: domain.KindFieldAccess,
			Name: "table_a.id", FilePath: "a.go", LineStart: 6,
			Properties: map[string]any{"full_path": "table_a.id", "access_kind": "read",
				"type_string": "sql", "is_external": "true", "func_id": funcID}},
		{ID: funcID + "#t4", Kind: domain.KindSSAValue, Name: "t4",
			Properties: map[string]any{"func_id": funcID}},
		{ID: funcID + "#x", Kind: domain.KindSSAValue, Name: "id",
			Properties: map[string]any{"func_id": funcID}},
		{ID: funcID + "#ext.sql.table_b.a_id.filter@9", Kind: domain.KindFieldAccess,
			Name: "table_b.a_id", FilePath: "a.go", LineStart: 9,
			Properties: map[string]any{"full_path": "table_b.a_id", "access_kind": "filter",
				"type_string": "sql", "is_external": "true", "func_id": funcID}},
	}
	if _, err := r.SaveBatchStats(nodes, []*domain.Fact{
		{SourceID: funcID + "#ext.sql.table_a.id.read@6", TargetID: funcID + "#t4",
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: funcID + "#t4", TargetID: funcID + "#x",
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: funcID + "#x", TargetID: funcID + "#ext.sql.table_b.a_id.filter@9",
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
	}, nil); err != nil {
		t.Fatalf("save: %v", err)
	}
	// Q228：全量查询要求计算完成——预计算
	if err := r.Save(&domain.BuildMeta{BuildID: "b1", ToolName: "all", Status: "success"}); err != nil {
		t.Fatalf("Save build: %v", err)
	}
	if err := r.PrecomputeAllRelations(nil); err != nil {
		t.Fatalf("precompute: %v", err)
	}
	acts = New(r)
	rels, err := acts.RelationsAll("")
	if err != nil {
		t.Fatalf("RelationsAll: %v", err)
	}

	if len(rels) != 2 {
		t.Fatalf("rels = %+v, want 2", rels)
	}
	fwd, bwd := rels[0], rels[1]
	if fwd.FromTable != "table_a" || fwd.ToTable != "table_b" || fwd.Type != domain.RelationFK {
		t.Errorf("fwd = %+v, want table_a→table_b fk", fwd)
	}
	if bwd.FromTable != "table_b" || bwd.ToTable != "table_a" || bwd.Type != domain.RelationRead {
		t.Errorf("bwd = %+v, want table_b→table_a read", bwd)
	}
}
