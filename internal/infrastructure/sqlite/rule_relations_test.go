package sqlite

import (
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

// Q220c：用户连线规则（relation_rules）——merchant_id 这类外键形态列
// 值来自函数参数、无值流验证时，由用户添加规则声明连线（存数据库，
// clean/reindex 保留）。两种形态：
//   - 模式规则（from_table=''）：`merchant_id → mch_merchant.id`——所有
//     含 merchant_id 列的表都连到 mch_merchant.id
//   - 显式规则：`pt_member_level.merchant_id → mch_merchant.id`——单对
//
// 生效约束：目标表/列必须真实存在（防幽灵线）；生成关系类型 fk（用户
// 声明可信，ER 默认显示）、hops=1。

// ruleFixture 两张含 merchant_id 的表 + 目标表 id 列。
func ruleFixture(t *testing.T, r *Repo) {
	t.Helper()
	fA := "symbol:go:example.com/m:load"
	fB := "symbol:go:example.com/m:save"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(fA), Kind: domain.KindFunction, Name: "load"},
		{ID: domain.CanonicalID(fB), Kind: domain.KindFunction, Name: "save"},
		// table_a.merchant_id（filter）+ table_b.merchant_id（filter）
		{ID: domain.CanonicalID(fA + "#ext.sql.table_a.merchant_id.filter@6"), Kind: domain.KindFieldAccess,
			Name: "table_a.merchant_id", FilePath: "a.go", LineStart: 6,
			Properties: map[string]any{"full_path": "table_a.merchant_id", "access_kind": "filter",
				"type_string": "sql", "is_external": "true", "func_id": fA}},
		{ID: domain.CanonicalID(fB + "#ext.sql.table_b.merchant_id.filter@8"), Kind: domain.KindFieldAccess,
			Name: "table_b.merchant_id", FilePath: "b.go", LineStart: 8,
			Properties: map[string]any{"full_path": "table_b.merchant_id", "access_kind": "filter",
				"type_string": "sql", "is_external": "true", "func_id": fB}},
		// 目标表 mch_merchant.id（read 节点）
		{ID: domain.CanonicalID(fA + "#ext.sql.mch_merchant.id.read@10"), Kind: domain.KindFieldAccess,
			Name: "mch_merchant.id", FilePath: "a.go", LineStart: 10,
			Properties: map[string]any{"full_path": "mch_merchant.id", "access_kind": "read",
				"type_string": "sql", "is_external": "true", "func_id": fA}},
	}
	save(t, r, nodes, nil)
}

// TestRuleRelationModePattern：模式规则一条覆盖两张表的 merchant_id。
func TestRuleRelationModePattern(t *testing.T) {
	r := newTestRepo(t)
	ruleFixture(t, r)
	id, err := r.AddRelationRule(RelationRule{FromCol: "merchant_id", ToTable: "mch_merchant", ToCol: "id"})
	if err != nil || id <= 0 {
		t.Fatalf("AddRelationRule: id=%d err=%v", id, err)
	}
	rels, err := r.GetTableRelations("table_a", "full")
	if err != nil {
		t.Fatalf("full: %v", err)
	}
	found := false
	for _, rel := range rels {
		if rel.FromTable == "table_a" && rel.FromCol == "merchant_id" &&
			rel.ToTable == "mch_merchant" && rel.ToCol == "id" {
			found = true
			if rel.Type != domain.RelationFK || rel.Hops != 1 {
				t.Fatalf("规则关系应为 fk hops=1，got %s/%d", rel.Type, rel.Hops)
			}
		}
	}
	if !found {
		t.Fatalf("模式规则应生成 table_a.merchant_id → mch_merchant.id，rels=%+v", rels)
	}
	// table_b 同样被模式规则覆盖（规则生成在 all 路径一致）
	relsB, err := r.GetTableRelations("table_b", "full")
	if err != nil {
		t.Fatalf("table_b: %v", err)
	}
	foundB := false
	for _, rel := range relsB {
		if rel.FromTable == "table_b" && rel.FromCol == "merchant_id" &&
			rel.ToTable == "mch_merchant" {
			foundB = true
		}
	}
	if !foundB {
		t.Fatalf("模式规则应覆盖 table_b.merchant_id，rels=%+v", relsB)
	}
	// 单表查询不得混入其他表的规则线（Q220c 回归：mergeRuleRelations
	// 曾把全库规则线合并进单表结果——mm_relation 查询返回无关表线）
	for _, rel := range rels {
		if rel.FromTable != "table_a" {
			t.Fatalf("table_a 单表查询不得含其他表规则线，got %s.%s → %s.%s",
				rel.FromTable, rel.FromCol, rel.ToTable, rel.ToCol)
		}
	}
}

// TestRuleRelationExplicitAndValidation：显式规则只生成单对；目标表/
	// 列不存在时不生成（幽灵线防护）。
func TestRuleRelationExplicitAndValidation(t *testing.T) {
	r := newTestRepo(t)
	ruleFixture(t, r)
	// 显式规则：table_a.merchant_id → mch_merchant.id（存在 → 生成）
	if _, err := r.AddRelationRule(RelationRule{
		FromTable: "table_a", FromCol: "merchant_id", ToTable: "mch_merchant", ToCol: "id"}); err != nil {
		t.Fatalf("AddRelationRule: %v", err)
	}
	// 目标表不存在（ghost_table）→ 不生成
	if _, err := r.AddRelationRule(RelationRule{
		FromCol: "merchant_id", ToTable: "ghost_table", ToCol: "id"}); err != nil {
		t.Fatalf("AddRelationRule ghost: %v", err)
	}
	// 显式来源列不存在（table_a.nonexist）→ 不生成
	if _, err := r.AddRelationRule(RelationRule{
		FromTable: "table_a", FromCol: "nonexist", ToTable: "mch_merchant", ToCol: "id"}); err != nil {
		t.Fatalf("AddRelationRule badcol: %v", err)
	}
	rels, err := r.GetTableRelations("table_a", "full")
	if err != nil {
		t.Fatalf("full: %v", err)
	}
	var toGhost, toMch int
	for _, rel := range rels {
		if rel.FromTable == "table_a" && rel.FromCol == "merchant_id" && rel.ToTable == "mch_merchant" {
			toMch++
		}
		if rel.ToTable == "ghost_table" {
			toGhost++
		}
	}
	if toMch != 1 {
		t.Fatalf("显式规则应生成 1 条 mch_merchant 关联，got %d（rels=%+v）", toMch, rels)
	}
	if toGhost != 0 {
		t.Fatalf("目标表不存在不得生成（幽灵线），got %d", toGhost)
	}
}

// TestRuleRelationSurvivesReset：clean/reindex（ResetGraphTables）后规则
// 仍在且继续生效。
func TestRuleRelationSurvivesReset(t *testing.T) {
	r := newTestRepo(t)
	ruleFixture(t, r)
	if _, err := r.AddRelationRule(RelationRule{FromCol: "merchant_id", ToTable: "mch_merchant", ToCol: "id"}); err != nil {
		t.Fatalf("AddRelationRule: %v", err)
	}
	if err := r.ResetGraphTables(); err != nil {
		t.Fatalf("ResetGraphTables: %v", err)
	}
	rules, err := r.ListRelationRules()
	if err != nil || len(rules) != 1 {
		t.Fatalf("reset 后规则应保留，got %d err=%v", len(rules), err)
	}
	// reset 后重新灌节点，规则仍生效
	ruleFixture(t, r)
	rels, err := r.GetTableRelations("table_a", "full")
	if err != nil {
		t.Fatalf("full: %v", err)
	}
	found := false
	for _, rel := range rels {
		if rel.FromTable == "table_a" && rel.FromCol == "merchant_id" &&
			rel.ToTable == "mch_merchant" {
			found = true
		}
	}
	if !found {
		t.Fatal("reset 后规则应继续生效")
	}
}

// TestRuleRelationMergeRank：同 key 已有低 rank 关系（read）时规则 fk 覆盖。
func TestRuleRelationMergeRank(t *testing.T) {
	r := newTestRepo(t)
	fA := "symbol:go:example.com/m:load"
	fB := "symbol:go:example.com/m:save"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(fA), Kind: domain.KindFunction, Name: "load"},
		{ID: domain.CanonicalID(fB), Kind: domain.KindFunction, Name: "save"},
		// table_a.merchant_id write + 值 → 目标（产生 read 型关联）
		{ID: domain.CanonicalID(fA + "#ext.sql.table_a.merchant_id.filter@6"), Kind: domain.KindFieldAccess,
			Name: "table_a.merchant_id", FilePath: "a.go", LineStart: 6,
			Properties: map[string]any{"full_path": "table_a.merchant_id", "access_kind": "filter",
				"type_string": "sql", "is_external": "true", "func_id": fA}},
		{ID: domain.CanonicalID(fA + "#v"), Kind: domain.KindSSAValue, Name: "v",
			Properties: map[string]any{"func_id": fA, "type_string": "int64"}},
		// 目标表 table_b.id（read 节点——同 key 低 rank 关系来源）
		{ID: domain.CanonicalID(fA + "#ext.sql.table_b.id.read@9"), Kind: domain.KindFieldAccess,
			Name: "table_b.id", FilePath: "b.go", LineStart: 9,
			Properties: map[string]any{"full_path": "table_b.id", "access_kind": "read",
				"type_string": "sql", "is_external": "true", "func_id": fA}},
	}
	edges := []*domain.Fact{
		{SourceID: domain.CanonicalID(fA + "#v"), TargetID: domain.CanonicalID(fA + "#ext.sql.table_a.merchant_id.filter@6"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(fA + "#ext.sql.table_a.merchant_id.filter@6"), TargetID: domain.CanonicalID(fA + "#ext.sql.table_b.id.read@9"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
	}
	save(t, r, nodes, edges)
	// 规则：merchant_id → table_b.id（目标存在）
	if _, err := r.AddRelationRule(RelationRule{FromCol: "merchant_id", ToTable: "table_b", ToCol: "id"}); err != nil {
		t.Fatalf("AddRelationRule: %v", err)
	}
	rels, err := r.GetTableRelations("table_a", "full")
	if err != nil {
		t.Fatalf("full: %v", err)
	}
	for _, rel := range rels {
		if rel.FromTable == "table_a" && rel.FromCol == "merchant_id" && rel.ToTable == "table_b" {
			if rel.Type != domain.RelationFK {
				t.Fatalf("同 key 规则 fk 应覆盖低 rank，got %s", rel.Type)
			}
		}
	}
}
