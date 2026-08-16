package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
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
	return dir
}

// TestQueryRelationsAll：query relations --all 一次返回全库关联
// （Q160）——JSON 数组含正向 query 关联，无需逐表查询。
func TestQueryRelationsAll(t *testing.T) {
	dir := seedTableRelations(t)
	out := captureStdout(func() {
		if code := cmdQuery([]string{"relations", "--all", "--repo", dir, "--json"}); code != 0 {
			t.Errorf("relations --all exit = %d", code)
		}
	})
	var rels []map[string]any
	if err := json.Unmarshal([]byte(out), &rels); err != nil {
		t.Fatalf("relations --all JSON: %v\n%s", err, out)
	}
	if len(rels) != 2 {
		t.Fatalf("rels = %d 条, want 2（正向 query + 反向 read）: %s", len(rels), out)
	}
	fwd := rels[0]
	if fwd["from_table"] != "table_a" || fwd["to_table"] != "table_b" ||
		fwd["from_col"] != "id" || fwd["to_col"] != "a_id" {
		t.Errorf("fwd = %v, want table_a.id → table_b.a_id", fwd)
	}
	if fwd["type"] != "query" {
		t.Errorf("fwd type = %v, want query", fwd["type"])
	}
}

// TestQueryRelationsAllText：--all 文本模式按表分组展示。
func TestQueryRelationsAllText(t *testing.T) {
	dir := seedTableRelations(t)
	out := captureStdout(func() {
		if code := cmdQuery([]string{"relations", "--all", "--repo", dir}); code != 0 {
			t.Errorf("relations --all exit = %d", code)
		}
	})
	for _, want := range []string{"table_a", "table_b", "查询关联", "2 条"} {
		if !strings.Contains(out, want) {
			t.Errorf("relations --all text missing %q:\n%s", want, out)
		}
	}
}

// TestExportRelations：export relations 一次性导出全库关联 JSON 文件（Q160）。
func TestExportRelations(t *testing.T) {
	dir := seedTableRelations(t)
	outPath := filepath.Join(t.TempDir(), "relations.json")
	if code := cmdExport([]string{"relations", "--repo", dir, "--out", outPath}); code != 0 {
		t.Fatalf("export relations exit = %d", code)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Relations []map[string]any `json:"relations"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("export relations JSON: %v\n%s", err, data)
	}
	if len(got.Relations) != 2 {
		t.Fatalf("relations = %d 条, want 2: %s", len(got.Relations), data)
	}
	fwd := got.Relations[0]
	if fwd["from_table"] != "table_a" || fwd["to_table"] != "table_b" || fwd["type"] != "query" {
		t.Errorf("fwd = %v, want table_a.id → table_b.a_id query", fwd)
	}
}

// TestValueTraceMinConfCLI：Q161——value-trace --min-conf 剪枝低置信
// 候选边（0.7 < 0.8），且边级候选标注 JSON 输出。
func TestValueTraceMinConfCLI(t *testing.T) {
	dir := seedRepo(t)
	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	r := sqlite.NewRepo(db)
	callerID := "symbol:go:example.com/m:g"
	funcID := "symbol:go:example.com/m:f"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(callerID), Kind: domain.KindFunction, Name: "g", FilePath: "g.go"},
		{ID: domain.CanonicalID(funcID), Kind: domain.KindFunction, Name: "f", FilePath: "f.go"},
		{ID: domain.CanonicalID(callerID + "#t0"), Kind: domain.KindSSAValue, Name: "t0",
			Properties: map[string]any{"func_id": callerID}},
		{ID: domain.CanonicalID(funcID + "#a"), Kind: domain.KindSSAValue, Name: "a",
			Properties: map[string]any{"func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#a.X.read@3"), Kind: domain.KindFieldAccess, Name: "a.X",
			FilePath: "f.go", LineStart: 3,
			Properties: map[string]any{"func_id": funcID, "full_path": "example.com/m.T.X",
				"access_kind": "read"}},
	}
	if _, err := r.SaveBatchStats(nodes, []*domain.Fact{
		{SourceID: domain.CanonicalID(callerID + "#t0"), TargetID: domain.CanonicalID(funcID + "#a"),
			Kind: domain.FactArgument, ToolSource: domain.ToolSSA, Confidence: 1,
			Metadata: map[string]any{"interface": "example.com/m.Fee",
				"candidate_origin": "enum", "confidence": 0.7}},
		{SourceID: domain.CanonicalID(funcID + "#a"), TargetID: domain.CanonicalID(funcID + "#a.X.read@3"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
	}, nil); err != nil {
		t.Fatalf("save: %v", err)
	}
	anchor := string(domain.CanonicalID(funcID + "#a.X.read@3"))
	// Q163 默认（minConf=1.0）：候选边剪枝——调用方实参不可达、无标注
	out := captureStdout(func() {
		if code := cmdQuery([]string{"value-trace", anchor, "--repo", dir, "--json"}); code != 0 {
			t.Errorf("value-trace exit = %d", code)
		}
	})
	var flows struct {
		Flows []map[string]any `json:"flows"`
	}
	if err := json.Unmarshal([]byte(out), &flows); err != nil {
		t.Fatalf("value-trace JSON: %v\n%s", err, out)
	}
	for _, f := range flows.Flows {
		if f["func_id"] == callerID {
			t.Error("默认模式候选路径不应出现（Q163 候选边剪枝）")
		}
	}
	// 显式 --min-conf 0：候选路径可见 + 边级标注（累计）
	out = captureStdout(func() {
		if code := cmdQuery([]string{"value-trace", anchor, "--repo", dir, "--min-conf", "0", "--json"}); code != 0 {
			t.Errorf("value-trace --min-conf exit = %d", code)
		}
	})
	if err := json.Unmarshal([]byte(out), &flows); err != nil {
		t.Fatalf("value-trace --min-conf JSON: %v", err)
	}
	marked := false
	for _, f := range flows.Flows {
		if ec, ok := f["edge_candidate"].(map[string]any); ok {
			marked = true
			if ec["origin"] != "enum" {
				t.Errorf("edge_candidate.origin = %v, want enum", ec["origin"])
			}
		}
	}
	if !marked {
		t.Error("--min-conf 0 后应有 edge_candidate 标注")
	}
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
	// JSON：origins 数组带 origin/confidence（dispatch join）
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
	// 文本：来源行展示
	out = captureStdout(func() {
		if code := cmdQuery([]string{"fields", "run", "--repo", dir}); code != 0 {
			t.Errorf("fields text exit = %d", code)
		}
	})
	if !strings.Contains(out, "↳ 来源") {
		t.Errorf("fields 文本缺来源行:\n%s", out)
	}
}
