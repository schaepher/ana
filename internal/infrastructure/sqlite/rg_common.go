package sqlite

import "github.com/schaepher/codeintel/internal/domain"

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
			if hasFK && r.FromCol == "id" {
				continue
			}
			out = append(out, r)
		}
	}
	return out
}
