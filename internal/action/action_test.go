package action

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
)

// seedRepo 建临时仓库 + 预填一个小图（action 测试用）。
func seedRepo(t *testing.T) (*Actions, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/m\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	r := sqlite.NewRepo(db)
	mainID := domain.CanonicalID("symbol:go:example.com/m:main")
	runID := domain.CanonicalID("symbol:go:example.com/m/svc:(Svc).Run")
	nodes := []*domain.CodeEntity{
		{ID: mainID, Kind: domain.KindFunction, Name: "main", FilePath: "main.go", LineStart: 3},
		{ID: runID, Kind: domain.KindMethod, Name: "(Svc).Run", FilePath: "svc/svc.go", LineStart: 5},
		{ID: "symbol:go:example.com/m:helper", Kind: domain.KindFunction, Name: "helper", FilePath: "helper.go"},
	}
	if _, err := r.SaveBatchStats(nodes, []*domain.Fact{
		{SourceID: mainID, TargetID: runID, Kind: domain.FactCalls, ToolSource: domain.ToolCodeGraph, Confidence: 0.9},
		{SourceID: mainID, TargetID: "symbol:go:example.com/m:helper", Kind: domain.FactCalls, ToolSource: domain.ToolCodeGraph, Confidence: 0.9},
	}, nil); err != nil {
		t.Fatalf("save: %v", err)
	}
	// 字段摘要 + 追溯图（fields/trace/export 用）
	funcID := string(mainID)
	r.SaveBatchStats(nil, nil, []*domain.FunctionFieldSummary{
		{FunctionID: domain.CanonicalID(funcID), AccessKind: domain.SummaryDirectWrite,
			FieldPath: "example.com/m.T.A", InstancePath: "t.A", LineStart: 5, CodeSnippet: "t.A = v"},
		{FunctionID: domain.CanonicalID(funcID), AccessKind: domain.SummaryDirectRead,
			FieldPath: "example.com/m.T.A", InstancePath: "t.A", LineStart: 7, CodeSnippet: "return t.A"},
	})
	writeNode := &domain.CodeEntity{ID: domain.CanonicalID(funcID + "#t.A.write@5"),
		Kind: domain.KindFieldAccess, Name: "t.A", FilePath: "main.go", LineStart: 5,
		Properties: map[string]any{"full_path": "example.com/m.T.A", "instance_path": "t.A",
			"access_kind": "write", "func_id": funcID}}
	val := &domain.CodeEntity{ID: domain.CanonicalID(funcID + "#t0"), Kind: domain.KindSSAValue,
		Name: "t0", Properties: map[string]any{"func_id": funcID}}
	r.SaveBatchStats([]*domain.CodeEntity{writeNode, val}, []*domain.Fact{
		{SourceID: val.ID, TargetID: writeNode.ID, Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
	}, nil)
	return New(r), dir
}

func TestResolveSymbol(t *testing.T) {
	a, _ := seedRepo(t)
	// canonical ID 直连
	n, err := a.ResolveSymbol("symbol:go:example.com/m:main")
	if err != nil || n.Name != "main" {
		t.Errorf("resolve id = %v, %v", n, err)
	}
	// 名称精确
	n, err = a.ResolveSymbol("main")
	if err != nil || n.Name != "main" {
		t.Errorf("resolve name = %v, %v", n, err)
	}
	// 不存在
	if _, err := a.ResolveSymbol("nope_nope"); err == nil {
		t.Error("resolve unknown should fail")
	}
}

func TestSymbolDetail(t *testing.T) {
	a, _ := seedRepo(t)
	d, err := a.SymbolDetail("main")
	if err != nil {
		t.Fatal(err)
	}
	if d.Node.Name != "main" {
		t.Errorf("node = %v", d.Node.Name)
	}
	if len(d.Callees) != 2 {
		t.Errorf("callees = %d, want 2", len(d.Callees))
	}
}

func TestCallersCalleesImpact(t *testing.T) {
	a, _ := seedRepo(t)
	run := domain.CanonicalID("symbol:go:example.com/m/svc:(Svc).Run")
	callers, err := a.Callers(run, 1)
	if err != nil || len(callers) != 1 {
		t.Errorf("callers = %v, %v", callers, err)
	}
	callees, err := a.Callees(domain.CanonicalID("symbol:go:example.com/m:main"), 1)
	if err != nil || len(callees) != 2 {
		t.Errorf("callees = %v, %v", callees, err)
	}
	nodes, err := a.Impact(run, 3)
	if err != nil || len(nodes) == 0 {
		t.Errorf("impact = %v, %v", nodes, err)
	}
}

func TestFunctionFieldsAndTrace(t *testing.T) {
	a, _ := seedRepo(t)
	n, rows, err := a.FunctionFields("main")
	if err != nil || n.Name != "main" {
		t.Fatalf("fields = %v, %v", n, err)
	}
	if len(rows) != 2 {
		t.Errorf("rows = %d, want 2", len(rows))
	}
	tn, tr, err := a.Trace(TraceParams{Field: "example.com/m.T.A", Func: "main", Forward: false})
	if err != nil {
		t.Fatal(err)
	}
	if tn.Name != "main" || len(tr) == 0 {
		t.Errorf("trace-backward = %v, %d rows", tn, len(tr))
	}
}

func TestValueTraceSearchExpandFlows(t *testing.T) {
	a, _ := seedRepo(t)
	vt, err := a.ValueTrace("symbol:go:example.com/m:main#t0", 8, 0, false)
	if err != nil || len(vt) == 0 {
		t.Errorf("value-trace = %v, %v", vt, err)
	}
	s, err := a.Search("main")
	if err != nil || len(s) == 0 {
		t.Errorf("search = %v, %v", s, err)
	}
	cur, facts, nodes, err := a.Expand(domain.CanonicalID("symbol:go:example.com/m:main"))
	if err != nil || cur.Name != "main" {
		t.Errorf("expand = %v, %v", cur, err)
	}
	if len(facts) == 0 || len(nodes) == 0 {
		t.Errorf("expand facts/nodes = %d/%d", len(facts), len(nodes))
	}
	flows, err := a.Flows(domain.CanonicalID("symbol:go:example.com/m:main"), 8)
	if err != nil || len(flows) == 0 {
		t.Errorf("flows = %v, %v", flows, err)
	}
}

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
	// 从写锚点出发：产生链（值 → 写）+ 下游（读 → 结果）
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
	// seedRepo 的 Actions 内部 repo 是窄接口——重开 sqlite 存 dispatch 边
	// （FK：Iface 节点须先存在）
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
	// value-trace 从 main 的写节点反向：t0 行所属 main 是候选 → 标注
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
	acts = New(r)
	rels, err := acts.RelationsAll("")
	if err != nil {
		t.Fatalf("RelationsAll: %v", err)
	}
	// 期望 2 条：正向 query（table_a.id → table_b.a_id）+ 反向 read（table_b.a_id → table_a.id）
	if len(rels) != 2 {
		t.Fatalf("rels = %+v, want 2", rels)
	}
	fwd, bwd := rels[0], rels[1]
	if fwd.FromTable != "table_a" || fwd.ToTable != "table_b" || fwd.Type != domain.RelationQuery {
		t.Errorf("fwd = %+v, want table_a→table_b query", fwd)
	}
	if bwd.FromTable != "table_b" || bwd.ToTable != "table_a" || bwd.Type != domain.RelationRead {
		t.Errorf("bwd = %+v, want table_b→table_a read", bwd)
	}
}
