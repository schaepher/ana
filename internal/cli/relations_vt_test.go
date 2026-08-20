package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
)

// seedTableRelations 建临时仓库 + 灌入外部表虚拟节点与数据流链
// （table_a.id 读出 → table_b.a_id 过滤，Q160 测试用）。
func seedTableRelations(t *testing.T) string {
	t.Helper()
	dir := seedRepo(t)
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
				"type_string": "sql", "is_external": "true", "func_id": string(funcID)}},
		{ID: funcID + "#t4", Kind: domain.KindSSAValue, Name: "t4",
			Properties: map[string]any{"func_id": string(funcID)}},
		{ID: funcID + "#x", Kind: domain.KindSSAValue, Name: "id",
			Properties: map[string]any{"func_id": string(funcID)}},
		{ID: funcID + "#ext.sql.table_b.a_id.filter@9", Kind: domain.KindFieldAccess,
			Name: "table_b.a_id", FilePath: "a.go", LineStart: 9,
			Properties: map[string]any{"full_path": "table_b.a_id", "access_kind": "filter",
				"type_string": "sql", "is_external": "true", "func_id": string(funcID)}},
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
	// Q228：query relations --all / export relations 要求计算完成——预计算
	if err := r.Save(&domain.BuildMeta{BuildID: "b1", ToolName: "all", Status: "success"}); err != nil {
		t.Fatalf("Save build: %v", err)
	}
	if err := r.PrecomputeAllRelations(nil); err != nil {
		t.Fatalf("precompute: %v", err)
	}
	return dir
}

// TestQueryFieldsOrigins：Q161——query fields 展示间接写多来源
// （summary_origins 落库 + dispatch join）。
func TestQueryFieldsOrigins(t *testing.T) {
	dir := seedRepo(t)
	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	r := sqlite.NewRepo(db)
	runID := "symbol:go:example.com/m:run"
	implID := "symbol:go:example.com/m:(Impl).Write"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(runID), Kind: domain.KindFunction, Name: "run", FilePath: "run.go"},
		{ID: domain.CanonicalID(implID), Kind: domain.KindMethod, Name: "(Impl).Write", FilePath: "impl.go"},
		{ID: "symbol:go:example.com/m:Iface", Kind: domain.KindInterface, Name: "Iface", FilePath: "iface.go"},
	}
	if _, err := r.SaveBatchStats(nodes, []*domain.Fact{
		{SourceID: "symbol:go:example.com/m:Iface", TargetID: domain.CanonicalID(implID),
			Kind: domain.FactDispatchTo, ToolSource: domain.ToolSSA, Confidence: 0.7,
			Metadata: map[string]any{"origin": "enum"}},
	}, []*domain.FunctionFieldSummary{
		{FunctionID: domain.CanonicalID(runID), AccessKind: domain.SummaryIndirectWrite,
			FieldPath: "example.com/m.T.X", InstancePath: "t.X", LineStart: 5},
	}, []*domain.SummaryOrigin{
		{FunctionID: domain.CanonicalID(runID), AccessKind: domain.SummaryIndirectWrite,
			FieldPath: "example.com/m.T.X", CallLine: 7, CalleeID: domain.CanonicalID(implID)},
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	out := captureStdout(func() {
		if code := cmdQuery([]string{"fields", "run", "--repo", dir, "--json"}); code != 0 {
			t.Errorf("fields exit = %d", code)
		}
	})
	var got struct {
		Rows []struct {
			AccessKind string `json:"access_kind"`
			Origins    []struct {
				CallLine   int     `json:"call_line"`
				Callee     string  `json:"callee"`
				Origin     string  `json:"origin"`
				Confidence float64 `json:"confidence"`
			} `json:"origins"`
		} `json:"rows"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("fields JSON: %v\n%s", err, out)
	}
	found := false
	for _, row := range got.Rows {
		if row.AccessKind != domain.SummaryIndirectWrite {
			continue
		}
		for _, o := range row.Origins {
			found = true
			if o.CallLine != 7 || o.Callee == "" || o.Origin != "enum" || o.Confidence != 0.7 {
				t.Errorf("origin = %+v, want call_line 7 enum 0.7", o)
			}
		}
	}
	if !found {
		t.Error("fields 未展示 origins")
	}

	out = captureStdout(func() {
		if code := cmdQuery([]string{"fields", "run", "--repo", dir}); code != 0 {
			t.Errorf("fields text exit = %d", code)
		}
	})
	if !strings.Contains(out, "↳ 来源") {
		t.Errorf("fields 文本缺来源行:\n%s", out)
	}
}

// TestValueTraceIncludeContainerCLI：Q163——--include-container 显式
// 开启父容器路径扩展（默认精确匹配拦截容器读；flag 放行且不影响
// 候选剪枝语义）。
func TestValueTraceIncludeContainerCLI(t *testing.T) {
	dir := seedRepo(t)
	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	r := sqlite.NewRepo(db)
	funcID := "symbol:go:example.com/m:calc"
	write := &domain.CodeEntity{ID: domain.CanonicalID(funcID + "#invoice.SettledFee.write@3"),
		Kind: domain.KindFieldAccess, Name: "invoice.SettledFee", FilePath: "m.go", LineStart: 3,
		Properties: map[string]any{"full_path": "example.com/m.Invoice.SettledFee",
			"instance_path": "invoice.SettledFee", "access_kind": "write", "func_id": funcID}}
	v := &domain.CodeEntity{ID: domain.CanonicalID(funcID + "#t0"), Kind: domain.KindSSAValue, Name: "t0",
		Properties: map[string]any{"func_id": funcID}}
	invRead := &domain.CodeEntity{ID: domain.CanonicalID(funcID + "#invoice.read@5"),
		Kind: domain.KindFieldAccess, Name: "invoice", FilePath: "m.go", LineStart: 5,
		Properties: map[string]any{"full_path": "example.com/m.Invoice",
			"instance_path": "invoice", "access_kind": "read", "func_id": funcID,
			"type_string": "*example.com/m.Invoice"}}

	refundParam := &domain.CodeEntity{ID: domain.CanonicalID(funcID + "#refund"),
		Kind: domain.KindSSAValue, Name: "refund",
		Properties: map[string]any{"func_id": funcID}}
	if _, err := r.SaveBatchStats([]*domain.CodeEntity{write, v, invRead, refundParam}, []*domain.Fact{
		{SourceID: v.ID, TargetID: write.ID, Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: invRead.ID, TargetID: v.ID, Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},

		{SourceID: refundParam.ID, TargetID: v.ID, Kind: domain.FactReturns, ToolSource: domain.ToolSSA,
			Confidence: 1, Metadata: map[string]any{"interface": "example.com/m.RefundSource",
				"candidate_origin": "enum", "confidence": 0.7}},
	}, nil); err != nil {
		t.Fatalf("save: %v", err)
	}
	anchor := string(write.ID)

	out := captureStdout(func() {
		if code := cmdQuery([]string{"value-trace", anchor, "--repo", dir, "--json"}); code != 0 {
			t.Errorf("value-trace exit = %d", code)
		}
	})
	if strings.Contains(out, "refund") {
		t.Error("默认模式不应出现 RefundSource 候选路径")
	}

	out = captureStdout(func() {
		if code := cmdQuery([]string{"value-trace", anchor, "--repo", dir,
			"--include-container", "--min-conf", "0", "--json"}); code != 0 {
			t.Errorf("value-trace flags exit = %d", code)
		}
	})
	if !strings.Contains(out, "refund") {
		t.Error("--include-container --min-conf 0 后候选路径应可达")
	}
}
