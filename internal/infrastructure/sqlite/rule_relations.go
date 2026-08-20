package sqlite

import (
	"fmt"
	"time"

	"github.com/schaepher/codeintel/internal/domain"
	"go.uber.org/zap"
)

// RelationRule 用户连线规则（Q220c）：声明外键形态列 → 目标表主键的
// 关联（值流无法静态验证时的用户补充）。FromTable 为空 = 模式规则
// （所有含 FromCol 列的表都连到 ToTable.ToCol）；非空 = 显式列对。
// 存 relation_rules 表，clean/reindex（ResetGraphTables）保留。
// AddRelationRule 添加连线规则（Q220c）。校验语法（列名非空、目标表名
// 非空），存在性校验在生效期（ruleRelations）执行——允许先加规则后建
// 索引。
func (r *Repo) AddRelationRule(rule domain.RelationRule) (int64, error) {
	logger := zap.L()
	logger.Debug("enter (Repo).AddRelationRule")
	defer logger.Debug("exit (Repo).AddRelationRule")
	if rule.FromCol == "" || rule.ToTable == "" {
		return 0, fmt.Errorf("规则须含 from_col 与 to_table（如 merchant_id → mch_merchant.id）")
	}
	if rule.ToCol == "" {
		rule.ToCol = "id"
	}
	res, err := r.Exec(`INSERT INTO relation_rules (from_table, from_col, to_table, to_col, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		rule.FromTable, rule.FromCol, rule.ToTable, rule.ToCol, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListRelationRules 全部规则（Q220c）。
func (r *Repo) ListRelationRules() ([]domain.RelationRule, error) {
	logger := zap.L()
	logger.Debug("enter (Repo).ListRelationRules")
	defer logger.Debug("exit (Repo).ListRelationRules")
	rows, err := r.Query(`SELECT id, from_table, from_col, to_table, to_col, created_at
		FROM relation_rules ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.RelationRule
	for rows.Next() {
		var ru domain.RelationRule
		if err := rows.Scan(&ru.ID, &ru.FromTable, &ru.FromCol, &ru.ToTable, &ru.ToCol, &ru.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, ru)
	}
	return out, rows.Err()
}

// RemoveRelationRule 删除规则（Q220c）。
func (r *Repo) RemoveRelationRule(id int64) error {
	logger := zap.L()
	logger.Debug("enter (Repo).RemoveRelationRule")
	defer logger.Debug("exit (Repo).RemoveRelationRule")
	_, err := r.Exec(`DELETE FROM relation_rules WHERE id = ?`, id)
	return err
}

// ruleRelations 按规则生成关系（Q220c）：存在性校验——目标表在
// GetTables 中、目标列有外部节点、显式规则的来源表/列存在，不满足
// 的规则静默跳过（幽灵线防护）。生成关系 type=fk（用户声明可信，
// ER 默认显示）、hops=1。
func (r *Repo) ruleRelations() ([]*domain.TableRelation, error) {
	rules, err := r.ListRelationRules()
	if err != nil || len(rules) == 0 {
		return nil, err
	}
	tables, err := r.GetTables()
	if err != nil {
		return nil, err
	}
	tableSet := map[string]bool{}
	for _, t := range tables {
		tableSet[t] = true
	}
	// 目标列存在性缓存（to_table|to_col）
	colOK := func(table, col string) bool {
		if !tableSet[table] {
			return false
		}
		var cnt int
		if err := r.QueryRow(`SELECT COUNT(*) FROM nodes WHERE kind = 'field_access'
			AND json_extract(properties, '$.is_external') = 'true' AND name = ?`, table+"."+col).Scan(&cnt); err != nil {
			return false
		}
		return cnt > 0
	}
	var out []*domain.TableRelation
	for _, ru := range rules {
		if !colOK(ru.ToTable, ru.ToCol) {
			continue // 目标表/列不存在——幽灵线防护
		}
		if ru.FromTable != "" {
			// 显式列对：来源表/列须存在
			if !tableSet[ru.FromTable] || !colOK(ru.FromTable, ru.FromCol) {
				continue
			}
			if ru.FromTable == ru.ToTable {
				continue // 自关联不属于表间关联语义
			}
			out = append(out, &domain.TableRelation{
				FromTable: ru.FromTable, FromCol: ru.FromCol,
				ToTable: ru.ToTable, ToCol: ru.ToCol,
				Hops: 1, Type: domain.RelationFK,
			})
			continue
		}
		// 模式规则：所有含 FromCol 列的表
		for _, t := range tables {
			if t == ru.ToTable || !colOK(t, ru.FromCol) {
				continue
			}
			out = append(out, &domain.TableRelation{
				FromTable: t, FromCol: ru.FromCol,
				ToTable: ru.ToTable, ToCol: ru.ToCol,
				Hops: 1, Type: domain.RelationFK,
			})
		}
	}
	return out, nil
}

// mergeRuleRelations 合并规则生成的关系（Q220c）：同 key 时类型 rank 高
// 者胜（规则 fk 覆盖低 rank 的 read/query），新 key 追加。规则关系不进
// relation_candidates 缓存（用户配置独立于 build_id，加规则无需重算）。
// table 非空（单表查询）时只合并 FromTable == table 的规则线——模式
// 规则对全库生效，但单表结果不得混入其他表的规则线（Q220c 回归）。
func (r *Repo) mergeRuleRelations(rels []*domain.TableRelation, table string) ([]*domain.TableRelation, error) {
	rules, err := r.ruleRelations()
	if err != nil || len(rules) == 0 {
		return rels, err
	}
	if table != "" {
		filtered := rules[:0]
		for _, rr := range rules {
			if rr.FromTable == table {
				filtered = append(filtered, rr)
			}
		}
		rules = filtered
	}
	if len(rules) == 0 {
		return rels, nil
	}
	seen := map[string]int{}
	for i, rel := range rels {
		seen[ruleRelKey(rel)] = i
	}
	out := make([]*domain.TableRelation, 0, len(rels)+len(rules))
	out = append(out, rels...)
	for _, rr := range rules {
		key := ruleRelKey(rr)
		if i, ok := seen[key]; ok {
			// Q234：同 rank 也覆盖——用户显式声明优先于自动识别（规则 B
			// 的 where 字段直接识别可能与规则同 key 同 fk，保持用户规则
			// 的 hops 语义）
			if relTypeRank(rr.Type) >= relTypeRank(out[i].Type) {
				out[i] = rr
			}
			continue
		}
		seen[key] = len(out)
		out = append(out, rr)
	}
	return out, nil
}

// ruleRelKey 规则关系去重键（与 dedupRelationNoise 的 fk/query 列级键一致）。
func ruleRelKey(rel *domain.TableRelation) string {
	return rel.FromTable + "|" + rel.FromCol + "|" + rel.ToTable + "|" + rel.ToCol
}
