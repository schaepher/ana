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
