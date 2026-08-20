package sqlite

import "github.com/schaepher/codeintel/internal/domain"

// isDataKind 是否为 BFS 数据流边。

// mergeRelation 同 key 去重合并（Q234 规则 B 用，内存/SQL 路径共享）：
// 已有同 key 行时保留 rank 更高类型 + 更小 hops；否则追加。
func mergeRelation(seen map[string]*domain.TableRelation, all []*domain.TableRelation,
	key string, rel *domain.TableRelation) []*domain.TableRelation {
	if ex, ok := seen[key]; ok {
		if relTypeRank(rel.Type) > relTypeRank(ex.Type) {
			ex.Type = rel.Type
		}
		if rel.Hops < ex.Hops {
			ex.Hops = rel.Hops
		}
		return all
	}
	seen[key] = rel
	return append(all, rel)
}

func isDataKind(kind string) bool {
	switch kind {
	case "data_flows_to", "argument", "returns", "summary_io", "alias", "phi_operand":
		return true
	}
	return false
}

// isDirectedKind 单向边（Q199）：argument/returns 只允许沿值流方向穿越
// （实参→形参、被调返回值→调用方）；反向穿越会把调用方的其他调用
// 串入，产生跨函数假同源（go2o create_time → id 误报根因）。
func isDirectedKind(kind string) bool {
	return kind == "argument" || kind == "returns"
}

// filterFKNoise Q159 外键语义过滤（独立函数便于单测）：
// id→id 一律丢弃（两表都不会拿各自自增主键互查）；同目标列多起点时
// 外键形态列（xxx_id）优先——主键 id 起点是对象值共享桥接噪音；保留
// 形态：A.xxx_id → B.id（外键查主键）、A.id → B.xxx_id（主键被外键引用
// 查询）、A.xxx_id → B.xxx_id（业务关联键）。

func filterFKNoise(all []*domain.TableRelation) []*domain.TableRelation {
	byTarget := map[string][]*domain.TableRelation{}
	for _, rel := range all {
		byTarget[rel.ToTable+"."+rel.ToCol] = append(byTarget[rel.ToTable+"."+rel.ToCol], rel)
	}
	var out []*domain.TableRelation
	for _, rels := range byTarget {
		hasFK := false
		for _, r := range rels {
			if r.FromCol != "id" {
				hasFK = true
				break
			}
		}
		for _, r := range rels {
			if r.FromCol == "id" && r.ToCol == "id" {
				continue
			}
			// Q205：id 起点过滤只作用于低置信（read/write）——query 键
			// 关联列级独立有意义（attr.id 读出 → 查 attr_item.attr_id
			// 是真实键关联，Q159 的 id 起点=对象值桥接噪音假设不适用）
			if hasFK && r.FromCol == "id" && r.Type != domain.RelationQuery && r.Type != domain.RelationFK {
				continue
			}
			out = append(out, r)
		}
	}
	return out
}
