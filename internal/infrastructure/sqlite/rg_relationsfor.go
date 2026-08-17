package sqlite

import (
	"sort"
	"strings"

	"github.com/schaepher/codeintel/internal/domain"
)

func (g *relationGraph) relationsFor(table string) []*domain.TableRelation {
	// 起点：本表全部列虚拟节点
	var starts []*relNode
	for _, n := range g.nodes {
		if n.kind == string(domain.KindFieldAccess) && n.isExternal &&
			(n.name == table || strings.HasPrefix(n.name, table+".")) {
			starts = append(starts, n)
		}
	}
	seen := map[string]*domain.TableRelation{}
	var all []*domain.TableRelation
	for _, st := range starts {
		visited := map[string]int{st.id: 0}
		queue := []string{st.id}
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			depth := visited[cur]
			if depth >= relationsMaxDepth {
				continue
			}

			for _, other := range g.dataAdj[cur] {
				if _, ok := visited[other]; ok {
					continue
				}
				visited[other] = depth + 1
				queue = append(queue, other)
			}

			if n := g.nodes[cur]; n != nil && n.funcID != "" {
				if tn, ok := g.typeNameOf(cur); ok && tn != "" {
					for _, n2 := range g.readsByFunc[n.funcID] {
						if !strings.Contains(n2.fullPath, tn) || !g.filterReachable2(n2.id) {
							continue
						}
						if _, ok := visited[n2.id]; !ok {
							visited[n2.id] = depth + 1
							queue = append(queue, n2.id)
						}
					}
				}
			}
		}

		if len(visited) <= 1 {
			continue
		}
		for id, d := range visited {
			if d == 0 {
				continue
			}
			n := g.nodes[id]
			if n == nil || n.kind != string(domain.KindFieldAccess) || !relTypeStrings[n.typeString] {
				continue
			}
			if !strings.Contains(n.name, ".") {
				continue
			}
			dot := strings.Index(n.name, ".")
			otherTable, col := n.name[:dot], n.name[dot+1:]
			if otherTable == table {
				continue
			}
			fromCol := st.name
			if i := strings.Index(fromCol, "."); i >= 0 {
				fromCol = fromCol[i+1:]
			}
			rtype := domain.RelationRead
			switch n.access {
			case "filter":
				rtype = domain.RelationQuery
			case "write":
				rtype = domain.RelationWrite
			}

			// 键关联列名呼应（双向）：外键含主键（user_id 含 id）或主键被
			// 外键引用（a_id 以 id 结尾）都保留 query；title→session_id
			// 等无关列降级 read（Q159）
			if rtype == domain.RelationQuery && col != fromCol {
				lc, lf := strings.ToLower(col), strings.ToLower(fromCol)
				if !strings.HasSuffix(lc, lf) && !strings.HasSuffix(lf, lc) {
					rtype = domain.RelationRead
				}
			}
			key := st.name + "|" + otherTable + "|" + col
			if ex, ok := seen[key]; ok {

				if relTypeRank(rtype) > relTypeRank(ex.Type) {
					ex.Type = rtype
				}
				if d < ex.Hops {
					ex.Hops = d
				}
				continue
			}
			seen[key] = &domain.TableRelation{
				FromTable: table, FromCol: fromCol,
				ToTable: otherTable, ToCol: col,
				Hops: d, Type: rtype,
			}
			all = append(all, seen[key])
		}
	}

	out := filterFKNoise(all)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.FromCol != b.FromCol {
			return a.FromCol < b.FromCol
		}
		if a.Hops != b.Hops {
			return a.Hops < b.Hops
		}
		if a.ToTable != b.ToTable {
			return a.ToTable < b.ToTable
		}
		return a.ToCol < b.ToCol
	})
	return out
}
