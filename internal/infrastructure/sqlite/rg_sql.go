package sqlite

import (
	"sort"
	"strings"

	"github.com/schaepher/codeintel/internal/domain"
)

func (r *Repo) relationsForSQL(table string) ([]*domain.TableRelation, error) {

	rows, err := r.Query(`SELECT id, name FROM nodes
		WHERE kind = 'field_access' AND json_extract(properties, '$.is_external') = 'true'
		  AND (name = ? OR name LIKE ?)`, table, table+".%")
	if err != nil {
		return nil, err
	}
	type colNode struct{ id, name string }
	var starts []colNode
	for rows.Next() {
		var c colNode
		if err := rows.Scan(&c.id, &c.name); err != nil {
			rows.Close()
			return nil, err
		}
		starts = append(starts, c)
	}
	rows.Close()

	const maxDepth = 12
	dataKinds := "'data_flows_to','argument','returns','summary_io','alias','phi_operand'"
	seen := map[string]*domain.TableRelation{}
	var all []*domain.TableRelation
	for _, st := range starts {

		visited := map[string]int{st.id: 0}
		queue := []string{st.id}
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			depth := visited[cur]
			if depth >= maxDepth {
				continue
			}

			ns, err := r.Query(`SELECT e.source_id, e.target_id FROM edges e
				WHERE e.kind IN (`+dataKinds+`) AND (e.source_id = ? OR e.target_id = ?)`, cur, cur)
			if err != nil {
				return nil, err
			}
			var next []string
			for ns.Next() {
				var src, tgt string
				if err := ns.Scan(&src, &tgt); err != nil {
					ns.Close()
					return nil, err
				}
				other := src
				if src == cur {
					other = tgt
				}
				if _, ok := visited[other]; ok {
					continue
				}
				visited[other] = depth + 1
				next = append(next, other)
			}
			ns.Close()

			if ts, ok := r.typeNameOf(cur); ok && ts != "" {
				fs, err := r.Query(`SELECT n2.id FROM nodes n1, nodes n2
					WHERE n1.id = ? AND n2.kind = 'field_access'
					  AND json_extract(n2.properties, '$.access_kind') = 'read'
					  AND json_extract(n2.properties, '$.func_id') = json_extract(n1.properties, '$.func_id')
					  AND instr(json_extract(n2.properties, '$.full_path'), ?) > 0
					  -- 精确桥：仅桥下游 2 跳内可达 filter 节点的字段读取
					  -- （字段 → 值 → filter：真正进 Where 的字段；防同类型全字段扩散）
					  AND EXISTS (
						SELECT 1 FROM edges e1
						JOIN edges e2 ON e2.source_id = e1.target_id
						JOIN nodes n3 ON n3.id = e2.target_id
						WHERE e1.source_id = n2.id
						  AND n3.kind = 'field_access'
						  AND json_extract(n3.properties, '$.access_kind') = 'filter'
						  AND json_extract(n3.properties, '$.is_external') = 'true'
					  )`, cur, ts)
				if err == nil {
					var bridge []string
					for fs.Next() {
						var bid string
						if err := fs.Scan(&bid); err == nil {
							if _, ok := visited[bid]; !ok {
								visited[bid] = depth + 1
								bridge = append(bridge, bid)
							}
						}
					}
					fs.Close()
					queue = append(queue, bridge...)
				}
			}
			queue = append(queue, next...)
		}

		byNode := map[string]string{}
		if len(visited) > 1 {
			ids := make([]any, 0, len(visited))
			for id := range visited {
				ids = append(ids, id)
			}
			q := `SELECT id, name, json_extract(properties, '$.access_kind') FROM nodes
			  WHERE id IN (` + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + `)
			  AND json_extract(properties, '$.type_string') IN ('sql', 'gorm', 'xorm')`
			ns, err := r.Query(q, ids...)
			if err != nil {
				return nil, err
			}
			for ns.Next() {
				var id, name, access string
				if err := ns.Scan(&id, &name, &access); err != nil {
					ns.Close()
					return nil, err
				}
				byNode[id] = name + "|" + access
			}
			ns.Close()
		}
		for id, d := range visited {
			if id == st.id || d == 0 {
				continue
			}
			meta := byNode[id]
			if meta == "" {
				continue
			}
			name := meta
			access := ""
			if i := strings.Index(meta, "|"); i >= 0 {
				name, access = meta[:i], meta[i+1:]
			}
			if !strings.Contains(name, ".") {
				continue
			}
			dot := strings.Index(name, ".")
			otherTable, col := name[:dot], name[dot+1:]
			if otherTable == table {
				continue
			}
			key := st.name + "|" + otherTable + "|" + col
			fromCol := st.name
			if i := strings.Index(fromCol, "."); i >= 0 {
				fromCol = fromCol[i+1:]
			}
			rtype := domain.RelationRead
			switch access {
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
			if ex, ok := seen[key]; ok {

				if relTypeRank(rtype) > relTypeRank(ex.Type) {
					ex.Type = rtype
				}
				if d < ex.Hops {
					ex.Hops = d
				}
				continue
			}
			rel := &domain.TableRelation{
				FromTable: table, FromCol: fromCol,
				ToTable: otherTable, ToCol: col,
				Hops: d, Type: rtype,
			}
			seen[key] = rel
			all = append(all, rel)
		}
	}

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
	return out, nil
}

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
