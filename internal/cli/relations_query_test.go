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

	// Q234：反向 table_b.a_id → table_a.id 也识别为 fk（where 条件字段 +
	// 外键形态呼应 table_a，规则 B 直接识别）→ 共 2 条
	if len(rels) != 2 {
		t.Fatalf("rels = %d 条, want 2（正向 fk + 反向 where 条件 fk）: %s", len(rels), out)
	}
	fwd := rels[0]
	if fwd["from_table"] != "table_a" || fwd["to_table"] != "table_b" ||
		fwd["from_col"] != "id" || fwd["to_col"] != "a_id" {
		t.Errorf("fwd = %v, want table_a.id → table_b.a_id", fwd)
	}
	if fwd["type"] != "fk" {
		t.Errorf("fwd type = %v, want fk", fwd["type"])
	}
	bwd := rels[1]
	if bwd["from_table"] != "table_b" || bwd["from_col"] != "a_id" ||
		bwd["to_table"] != "table_a" || bwd["to_col"] != "id" {
		t.Errorf("bwd = %v, want table_b.a_id → table_a.id", bwd)
	}
	if bwd["type"] != "fk" {
		t.Errorf("bwd type = %v, want fk（where 条件直接识别）", bwd["type"])
	}

	out = captureStdout(func() {
		if code := cmdQuery([]string{"relations", "--all", "--repo", dir, "--json", "--type", "read"}); code != 0 {
			t.Errorf("relations --all --type read exit = %d", code)
		}
	})
	if err := json.Unmarshal([]byte(out), &rels); err != nil {
		t.Fatalf("relations --all --type read JSON: %v", err)
	}
	if len(rels) != 0 {
		t.Fatalf("--type read = %v, want 0（原 read 已升 fk）", rels)
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
	for _, want := range []string{"table_a", "table_b", "外键关联", "2 条"} {
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
	if fwd["from_table"] != "table_a" || fwd["to_table"] != "table_b" || fwd["type"] != "fk" {
		t.Errorf("fwd = %v, want table_a.id → table_b.a_id fk", fwd)
	}
}

// TestQueryRelationsFilters：--type/--max-hops/--max-results 过滤与默认
// 行为（P0④）——默认只输出 query+write（read 低置信隐藏），--type read
// 显式展开，--memory sql 走逐节点 SQL 路径结果一致。
func TestQueryRelationsFilters(t *testing.T) {
	dir := seedTableRelations(t)

	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	r := sqlite.NewRepo(db)
	funcID := domain.CanonicalID("symbol:go:example.com/m:find")
	if _, err := r.SaveBatchStats([]*domain.CodeEntity{
		{ID: funcID + "#ext.sql.table_c.z.read@20", Kind: domain.KindFieldAccess,
			Name: "table_c.z", FilePath: "a.go", LineStart: 20,
			Properties: map[string]any{"full_path": "table_c.z", "access_kind": "read",
				"type_string": "sql", "is_external": "true", "func_id": string(funcID)}},
	}, []*domain.Fact{
		{SourceID: funcID + "#ext.sql.table_a.id.read@6", TargetID: funcID + "#ext.sql.table_c.z.read@20",
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
	}, nil); err != nil {
		t.Fatalf("save read rel: %v", err)
	}
	// Q228：图变化后重建缓存（precompute 写缓存后新节点不被反映）
	if err := r.PrecomputeAllRelations(nil); err != nil {
		t.Fatalf("precompute: %v", err)
	}

	out := captureStdout(func() {
		if code := cmdQuery([]string{"relations", "table_a", "--repo", dir, "--json"}); code != 0 {
			t.Errorf("relations exit = %d", code)
		}
	})
	var rels []*domain.TableRelation
	if err := json.Unmarshal([]byte(out), &rels); err != nil {
		t.Fatalf("json: %v", err)
	}
	if len(rels) != 1 || rels[0].ToTable != "table_b" {
		t.Fatalf("默认输出 = %+v, want 仅 table_b（read 隐藏）", rels)
	}

	out = captureStdout(func() {
		if code := cmdQuery([]string{"relations", "table_a", "--repo", dir, "--json", "--type", "read"}); code != 0 {
			t.Errorf("relations --type read exit = %d", code)
		}
	})
	if err := json.Unmarshal([]byte(out), &rels); err != nil {
		t.Fatalf("json: %v", err)
	}
	if len(rels) != 1 || rels[0].ToTable != "table_c" {
		t.Fatalf("--type read = %+v, want table_c", rels)
	}

	out = captureStdout(func() {
		if code := cmdQuery([]string{"relations", "table_a", "--repo", dir, "--json", "--type=query,write"}); code != 0 {
			t.Errorf("--type=query,write exit = %d", code)
		}
	})
	if err := json.Unmarshal([]byte(out), &rels); err != nil {
		t.Fatalf("json: %v", err)
	}
	if len(rels) != 0 {
		t.Fatalf("--type=query,write = %+v, want 空（Q218：fk 独立类型，不混入 query/write）", rels)
	}

	out = captureStdout(func() {
		if code := cmdQuery([]string{"relations", "table_a", "--repo", dir, "--json", "--max-hops", "1"}); code != 0 {
			t.Errorf("--max-hops exit = %d", code)
		}
	})
	if err := json.Unmarshal([]byte(out), &rels); err != nil {
		t.Fatalf("json: %v", err)
	}
	if len(rels) != 0 {
		t.Fatalf("--max-hops 1 = %+v, want 空", rels)
	}

	out = captureStdout(func() {
		if code := cmdQuery([]string{"relations", "table_a", "--repo", dir, "--json", "--memory", "sql"}); code != 0 {
			t.Errorf("--memory sql exit = %d", code)
		}
	})
	if err := json.Unmarshal([]byte(out), &rels); err != nil {
		t.Fatalf("json: %v", err)
	}
	if len(rels) != 1 || rels[0].ToTable != "table_b" {
		t.Fatalf("--memory sql = %+v, want table_b", rels)
	}
}
