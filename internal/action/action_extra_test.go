package action

import (
	"errors"
	"strings"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
)

// TestSymbol：canonical ID 直接查询（HTTP expand 存在性检查）。
func TestSymbol(t *testing.T) {
	a, _ := seedRepo(t)
	mainID := domain.CanonicalID("symbol:go:example.com/m:main")
	n, err := a.Symbol(mainID)
	if err != nil || n.Name != "main" {
		t.Errorf("Symbol = %v, %v", n, err)
	}
	if _, err := a.Symbol("symbol:go:example.com/m:nope"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Symbol missing = %v, want ErrNotFound", err)
	}
}

// TestRootsCountsLatest：前端初始视图 / 健康检查。
func TestRootsCountsLatest(t *testing.T) {
	a, dir := seedRepo(t)
	roots, err := a.Roots()
	if err != nil || len(roots) == 0 {
		t.Errorf("Roots = %v, %v", roots, err)
	}
	nodes, edges, err := a.Counts()
	if err != nil || nodes == 0 {
		t.Errorf("Counts = %d/%d, %v", nodes, edges, err)
	}
	// 未构建过 → 错误；写元数据后可查
	if _, err := a.Latest(); err == nil {
		t.Error("Latest on empty build_metadata should error")
	}
	meta := &domain.BuildMeta{BuildID: "b1", ToolName: "all", Status: domain.BuildSuccess}
	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	r := sqlite.NewRepo(db)
	if err := r.Save(meta); err != nil {
		t.Fatal(err)
	}
	got, err := a.Latest()
	if err != nil || got.BuildID != "b1" || got.ToolName != "all" {
		t.Errorf("Latest = %+v, %v", got, err)
	}
}

// TestResolveSymbolAmbiguous：多匹配错误列出全部候选 ID（joinIDs）。
func TestResolveSymbolAmbiguous(t *testing.T) {
	a, dir := seedRepo(t)
	// 再种一个名字含 "main" 的节点 → 模糊匹配歧义
	main2 := &domain.CodeEntity{ID: domain.CanonicalID("symbol:go:example.com/m:main2"),
		Kind: domain.KindFunction, Name: "main2", FilePath: "main2.go"}
	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := sqlite.NewRepo(db).SaveBatchStats([]*domain.CodeEntity{main2}, nil, nil); err != nil {
		t.Fatal(err)
	}
	// 精确匹配 "main" 只命中一个 → 歧义须用模糊输入（"ain" 命中 main+main2）
	_, rerr := a.ResolveSymbol("ain")
	if rerr == nil {
		t.Fatal("ResolveSymbol(main) should be ambiguous")
	}
	if !strings.Contains(rerr.Error(), "symbol:go:example.com/m:main") ||
		!strings.Contains(rerr.Error(), "symbol:go:example.com/m:main2") {
		t.Errorf("ambiguous error should list both candidates: %v", rerr)
	}
	// canonical ID 仍直接命中
	if n, err := a.ResolveSymbol("symbol:go:example.com/m:main2"); err != nil || n.Name != "main2" {
		t.Errorf("ResolveSymbol by id = %v, %v", n, err)
	}
}

// TestIndirectWriteSitesDispatchCandidates：INDIRECT_WRITE（含调用点
// metadata）与 dispatch_to 边查询。
func TestIndirectWriteSitesDispatchCandidates(t *testing.T) {
	a, dir := seedRepo(t)
	mainID := domain.CanonicalID("symbol:go:example.com/m:main")
	ifaceID := domain.CanonicalID("symbol:go:example.com/m/svc:Handler")
	implID := domain.CanonicalID("symbol:go:example.com/m/svc:(Svc).Run")
	writeSite := &domain.CodeEntity{ID: domain.CanonicalID(string(mainID) + "#w"),
		Kind: domain.KindFieldAccess, Name: "w", FilePath: "main.go",
		Properties: map[string]any{"full_path": "example.com/m.T.A", "access_kind": "write"}}
	// iface 节点需存在（边的 FK）
	ifaceNode := &domain.CodeEntity{ID: ifaceID, Kind: domain.KindInterface, Name: "Handler", FilePath: "svc/svc.go"}
	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := sqlite.NewRepo(db).SaveBatchStats([]*domain.CodeEntity{writeSite, ifaceNode}, []*domain.Fact{
		{SourceID: mainID, TargetID: writeSite.ID, Kind: domain.FactIndirectWrite,
			ToolSource: domain.ToolSSA, Confidence: 1,
			Metadata: map[string]any{"call_line": 12, "call_args": "v"}},
		{SourceID: ifaceID, TargetID: implID, Kind: domain.FactDispatchTo,
			ToolSource: domain.ToolSSA, Confidence: 0.9},
	}, nil); err != nil {
		t.Fatal(err)
	}
	sites, err := a.IndirectWriteSites(mainID)
	if err != nil || len(sites) != 1 {
		t.Fatalf("IndirectWriteSites = %v, %v", sites, err)
	}
	if sites[0].Metadata["call_line"] != float64(12) {
		t.Errorf("metadata = %+v, want call_line=12", sites[0].Metadata)
	}
	// 非本函数的调用点 → 空
	if sites, err := a.IndirectWriteSites(domain.CanonicalID("symbol:go:example.com/m:other")); err != nil || len(sites) != 0 {
		t.Errorf("other func sites = %v, %v", sites, err)
	}
	cands, err := a.DispatchCandidates(ifaceID)
	if err != nil || len(cands) != 1 || string(cands[0].TargetID) != string(implID) {
		t.Errorf("DispatchCandidates = %v, %v", cands, err)
	}
}

// TestSummaryChainStepKinds：链步骤类型分类——源头 entry、
// sql 摘要名 → write、字段读点 → consume。
func TestSummaryChainStepKinds(t *testing.T) {
	a, dir := seedRepo(t)
	funcID := "symbol:go:example.com/m:main"
	sqlNode := &domain.CodeEntity{ID: domain.CanonicalID(funcID + "#sql"),
		Kind: domain.KindFieldAccess, Name: "sql.DB.Exec", FilePath: "main.go",
		Properties: map[string]any{"full_path": "example.com/m.T.A", "access_kind": "write", "func_id": funcID}}
	anchor := &domain.CodeEntity{ID: domain.CanonicalID(funcID + "#v"), Kind: domain.KindSSAValue,
		Name: "v", Properties: map[string]any{"func_id": funcID}}
	readNode := &domain.CodeEntity{ID: domain.CanonicalID(funcID + "#r"),
		Kind: domain.KindFieldAccess, Name: "t.A", FilePath: "main.go",
		Properties: map[string]any{"full_path": "example.com/m.T.A", "instance_path": "t.A",
			"access_kind": "read", "func_id": funcID}}
	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := sqlite.NewRepo(db).SaveBatchStats([]*domain.CodeEntity{sqlNode, anchor, readNode}, []*domain.Fact{
		{SourceID: anchor.ID, TargetID: sqlNode.ID, Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: sqlNode.ID, TargetID: readNode.ID, Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
	}, nil); err != nil {
		t.Fatal(err)
	}
	steps, err := a.SummaryChain(sqlNode.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, st := range steps {
		got[st.Kind] = true
	}
	for _, want := range []string{"entry", "write", "consume"} {
		if !got[want] {
			t.Errorf("steps %+v missing kind %q", steps, want)
		}
	}
}

// TestResolveAnchor：③ 跨层摘要锚点解析——符号 ID/名称优先，类型限定
// 字段路径（example.com/m.T.A）回退到同字段读节点。
func TestResolveAnchor(t *testing.T) {
	a, dir := seedRepo(t)
	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	r := sqlite.NewRepo(db)
	funcID := "symbol:go:example.com/m:main"
	readNode := &domain.CodeEntity{ID: domain.CanonicalID(funcID + "#t.A.read@7"),
		Kind: domain.KindFieldAccess, Name: "t.A", FilePath: "main.go", LineStart: 7,
		Properties: map[string]any{"full_path": "example.com/m.T.A", "instance_path": "t.A",
			"access_kind": "read", "func_id": funcID}}
	if _, err := r.SaveBatchStats([]*domain.CodeEntity{readNode}, nil, nil); err != nil {
		t.Fatal(err)
	}
	// 字段路径 → 读节点锚点
	id, err := a.ResolveAnchor("example.com/m.T.A")
	if err != nil || id != readNode.ID {
		t.Errorf("ResolveAnchor(field path) = %v, %v", id, err)
	}
	// 符号 ID / 名称
	if id, err := a.ResolveAnchor("symbol:go:example.com/m:main"); err != nil || id != domain.CanonicalID(funcID) {
		t.Errorf("ResolveAnchor(id) = %v, %v", id, err)
	}
	if id, err := a.ResolveAnchor("main"); err != nil || id != domain.CanonicalID(funcID) {
		t.Errorf("ResolveAnchor(name) = %v, %v", id, err)
	}
	// 未知输入 → 错误
	if _, err := a.ResolveAnchor("nope_nope"); err == nil {
		t.Error("ResolveAnchor(unknown) should error")
	}
}

// TestLifecycleWriteAnchorDownstream：⑤ 生命周期图——写锚点经同字段读
// 节点跳板接入下游使用链（写入节点本身无出边）。
func TestLifecycleWriteAnchorDownstream(t *testing.T) {
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
	rows, err := a.Lifecycle(writeNode.ID)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, row := range rows {
		seen[string(row.ID)] = true
	}
	for _, want := range []string{string(writeNode.ID), string(readNode.ID), string(result.ID)} {
		if !seen[want] {
			t.Errorf("Lifecycle 缺节点 %s（写锚点下游未接入）: %+v", want, rows)
		}
	}
	// 读节点下游应为正向（dir=1）
	for _, row := range rows {
		if string(row.ID) == string(result.ID) && row.Dir != 1 {
			t.Errorf("result 应位于使用链 dir=1，got dir=%d", row.Dir)
		}
	}
}

// TestSummaryChainDedup：④ 摘要去重——多个读节点共享同一下游时，步骤
// 去重（同 Kind/Name/File/Line/Func），避免 N×深度8 全链的重复噪音。
func TestSummaryChainDedup(t *testing.T) {
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
	read1 := &domain.CodeEntity{ID: domain.CanonicalID(funcID + "#t.A.read@7"),
		Kind: domain.KindFieldAccess, Name: "t.A", FilePath: "main.go", LineStart: 7,
		Properties: map[string]any{"full_path": "example.com/m.T.A", "instance_path": "t.A",
			"access_kind": "read", "func_id": funcID}}
	read2 := &domain.CodeEntity{ID: domain.CanonicalID(funcID + "#t.A.read@8"),
		Kind: domain.KindFieldAccess, Name: "t.A", FilePath: "main.go", LineStart: 8,
		Properties: map[string]any{"full_path": "example.com/m.T.A", "instance_path": "t.A",
			"access_kind": "read", "func_id": funcID}}
	result := &domain.CodeEntity{ID: domain.CanonicalID(funcID + "#t1"), Kind: domain.KindSSAValue,
		Name: "t1", Properties: map[string]any{"func_id": funcID}}
	if _, err := r.SaveBatchStats([]*domain.CodeEntity{writeNode, read1, read2, result}, []*domain.Fact{
		{SourceID: read1.ID, TargetID: result.ID, Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: read2.ID, TargetID: result.ID, Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
	}, nil); err != nil {
		t.Fatalf("save: %v", err)
	}
	steps, err := a.SummaryChain(writeNode.ID)
	if err != nil {
		t.Fatal(err)
	}
	consumeT1 := 0
	for _, st := range steps {
		if st.Kind == "consume" && st.Name == "t1" {
			consumeT1++
		}
	}
	if consumeT1 != 1 {
		t.Errorf("共享下游 t1 应去重为 1 步，got %d: %+v", consumeT1, steps)
	}
}
