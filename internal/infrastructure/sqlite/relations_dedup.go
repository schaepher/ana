package sqlite

import (
	"strings"

	"github.com/schaepher/codeintel/internal/domain"
)

// relTypeRank 关联类型优先级（聚合去重用）：fk > query > write > read。
func relTypeRank(t string) int {
	switch t {
	case domain.RelationFK:
		return 3
	case domain.RelationQuery:
		return 2
	case domain.RelationWrite:
		return 1
	default:
		return 0
	}
}

// MaxRelationHops 关系跳数上限默认值（Q195/Q196：6-10 跳长链为噪音失真）。
const MaxRelationHops = 4

// DefaultRelationHops 默认跳数上限（引用 domain 版，当前设定值 4/4/4）。
var DefaultRelationHops = domain.DefaultRelationHops

// dedupRelationNoise 关系降噪（Q195/Q196/Q197，全部 relations 出口统一应用——
// 缓存命中路径也过一遍，保证旧缓存同样被降噪）：
// ① 跳数上限：按类型取 h（0=不限制）——query 长链同样失真，
//
//	需要查看长链时设 Query=0（--include-long-query）
//
// ② 同源写/间接读按 from字段→to表 聚合：同一 from 字段流入同一 to 表
//
//	的多列（全列 INSERT/UPDATE 的列爆炸，如 atoms.aliases →
//	knowledge_graphs 的 13 列各一条）只保留 hops 最小一条；
//	query 保持列级（键关联每列独立有意义）。
//
// 输出保持输入顺序（第一条位次，后续 hops 更小者替换值）。
func dedupRelationNoise(rels []*domain.TableRelation, h domain.RelationHops) []*domain.TableRelation {

	seen := map[string]*domain.TableRelation{}
	var order []string
	for _, r := range rels {

		if r.FromTable == r.ToTable {
			continue
		}
		limit := h.Read
		switch r.Type {
		case domain.RelationFK:
			limit = 0
		case domain.RelationQuery:
			limit = h.Query
		case domain.RelationWrite:
			limit = h.Write
		}
		if limit > 0 && r.Hops > limit {
			continue
		}
		var key string
		if r.Type == domain.RelationFK || r.Type == domain.RelationQuery {

			key = r.FromTable + "|" + r.FromCol + "|" + r.ToTable + "|" + r.ToCol
		} else if r.Type == domain.RelationWrite && strings.HasSuffix(r.ToCol, "id") {

			key = r.FromTable + "|" + r.FromCol + "|" + r.ToTable + "|" + r.ToCol
		} else {
			key = r.FromTable + "|" + r.FromCol + "|" + r.ToTable
		}
		ex, ok := seen[key]
		if !ok {
			order = append(order, key)
			seen[key] = r
		} else if r.Hops < ex.Hops {
			seen[key] = r
		}
	}
	out := make([]*domain.TableRelation, 0, len(order))
	for _, k := range order {
		out = append(out, seen[k])
	}
	return out
}
