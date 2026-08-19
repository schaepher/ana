package cli

import (
	"encoding/json"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
)

// seedRuleRepo 带目标表 id 节点的 relations fixture（Q220c 规则测试用）：
// table_a.id → table_b.a_id 值流 + table_b.id read 节点（规则目标）。
func seedRuleRepo(t *testing.T) string {
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
		// 规则来源列（table_a.a_id——模式/显式规则匹配）
		{ID: funcID + "#ext.sql.table_a.a_id.filter@10", Kind: domain.KindFieldAccess,
			Name: "table_a.a_id", FilePath: "a.go", LineStart: 10,
			Properties: map[string]any{"full_path": "table_a.a_id", "access_kind": "filter",
				"type_string": "sql", "is_external": "true", "func_id": string(funcID)}},
		// 规则目标列（存在性校验通过的前提）
		{ID: funcID + "#ext.sql.table_b.id.read@11", Kind: domain.KindFieldAccess,
			Name: "table_b.id", FilePath: "a.go", LineStart: 11,
			Properties: map[string]any{"full_path": "table_b.id", "access_kind": "read",
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
	// Q228：query relations --all 要求计算完成——预计算
	if err := r.Save(&domain.BuildMeta{BuildID: "b1", ToolName: "all", Status: "success"}); err != nil {
		t.Fatalf("Save build: %v", err)
	}
	if err := r.PrecomputeAllRelations(nil); err != nil {
		t.Fatalf("precompute: %v", err)
	}
	return dir
}

// ruleRelationsJSON query relations --all --json（captureStdout）。
func ruleRelationsJSON(t *testing.T, dir string) []map[string]any {
	t.Helper()
	out := captureStdout(func() {
		if code := cmdQuery([]string{"relations", "--all", "--repo", dir, "--json"}); code != 0 {
			t.Errorf("relations --all exit = %d", code)
		}
	})
	var rels []map[string]any
	if err := json.Unmarshal([]byte(out), &rels); err != nil {
		t.Fatalf("relations --all JSON: %v\n%s", err, out)
	}
	return rels
}

// TestRuleCLIAddListRemove：rule add（模式）→ relations 含 fk；list 显示；
// remove 后消失。
func TestRuleCLIAddListRemove(t *testing.T) {
	dir := seedRuleRepo(t)
	if code := cmdRule([]string{"add", "a_id", "table_b.id", "--repo", dir}); code != 0 {
		t.Fatalf("rule add 应成功，code=%d", code)
	}
	rels := ruleRelationsJSON(t, dir)
	found := false
	for _, rel := range rels {
		if rel["from_table"] == "table_a" && rel["from_col"] == "a_id" &&
			rel["to_table"] == "table_b" && rel["to_col"] == "id" &&
			rel["type"] == "fk" {
			found = true
		}
	}
	if !found {
		t.Fatalf("模式规则应生成 table_a.a_id → table_b.id fk，got=%v", rels)
	}
	// list
	listOut := captureStdout(func() {
		if code := cmdRule([]string{"list", "--repo", dir, "--json"}); code != 0 {
			t.Errorf("rule list exit = %d", code)
		}
	})
	var rules []domain.RelationRule
	if err := json.Unmarshal([]byte(listOut), &rules); err != nil {
		t.Fatalf("rule list JSON: %v\n%s", err, listOut)
	}
	if len(rules) != 1 || rules[0].FromCol != "a_id" || rules[0].ToTable != "table_b" {
		t.Fatalf("rule list 应有 1 条 a_id → table_b，got %+v", rules)
	}
	// remove
	if code := cmdRule([]string{"remove", "1", "--repo", dir}); code != 0 {
		t.Fatalf("rule remove 应成功，code=%d", code)
	}
	for _, rel := range ruleRelationsJSON(t, dir) {
		if rel["from_table"] == "table_a" && rel["from_col"] == "a_id" &&
			rel["to_table"] == "table_b" && rel["to_col"] == "id" {
			t.Fatalf("remove 后规则关系应消失，got=%v", rel)
		}
	}
}

// TestRuleCLIExplicit：显式列对规则只生成单对。
func TestRuleCLIExplicit(t *testing.T) {
	dir := seedRuleRepo(t)
	if code := cmdRule([]string{"add", "table_a.a_id", "table_b.id", "--repo", dir}); code != 0 {
		t.Fatalf("rule add 应成功，code=%d", code)
	}
	rels := ruleRelationsJSON(t, dir)
	found := false
	for _, rel := range rels {
		if rel["from_table"] == "table_a" && rel["from_col"] == "a_id" &&
			rel["to_table"] == "table_b" && rel["to_col"] == "id" {
			found = true
		}
	}
	if !found {
		t.Fatalf("显式规则应生成 table_a.a_id → table_b.id，got=%v", rels)
	}
}

// TestRuleCLIInvalid：非法表达式报错；语法合法但目标不存在不报错
// （存在性校验在生效期静默跳过）。
func TestRuleCLIInvalid(t *testing.T) {
	dir := seedRuleRepo(t)
	if code := cmdRule([]string{"add", "garbage", "--repo", dir}); code == 0 {
		t.Fatal("非法规则应报错")
	}
	if code := cmdRule([]string{"add", "a_id", "ghost_table.id", "--repo", dir}); code != 0 {
		t.Fatalf("语法合法应成功（存在性校验在生效期），code=%d", code)
	}
}
