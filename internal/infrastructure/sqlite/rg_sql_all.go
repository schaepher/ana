package sqlite

import (
	"sort"

	"github.com/schaepher/codeintel/internal/domain"
)

// getAllTableRelationsSQL --memory sql 模式的全库聚合：GetTables 枚举 +
// 逐表 relationsForSQL（逐节点查询，内存 O(1)——大仓库逃生路径）。
func (r *Repo) getAllTableRelationsSQL() ([]*domain.TableRelation, error) {
	tables, err := r.GetTables()
	if err != nil {
		return nil, err
	}
	seen := map[string]*domain.TableRelation{}
	for _, t := range tables {
		rels, err := r.relationsForSQL(t)
		if err != nil {
			return nil, err
		}
		for _, rel := range rels {
			key := rel.FromTable + "|" + rel.FromCol + "|" + rel.ToTable + "|" + rel.ToCol
			ex, ok := seen[key]
			if !ok || rel.Hops < ex.Hops || (rel.Hops == ex.Hops && relTypeRank(rel.Type) > relTypeRank(ex.Type)) {
				seen[key] = rel
			}
		}
	}
	out := make([]*domain.TableRelation, 0, len(seen))
	for _, rel := range seen {
		out = append(out, rel)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.FromTable != b.FromTable {
			return a.FromTable < b.FromTable
		}
		if a.FromCol != b.FromCol {
			return a.FromCol < b.FromCol
		}
		if a.ToTable != b.ToTable {
			return a.ToTable < b.ToTable
		}
		return a.ToCol < b.ToCol
	})
	r.rebuildRelationCandidates(out, tables)
	return out, nil
}
