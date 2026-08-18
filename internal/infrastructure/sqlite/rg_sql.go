package sqlite

import (
	"database/sql"
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
		crossed := map[string]bool{} // 到达该节点的链是否经过跨函数边（Q199）
		// Q212/Q218：节点值级 taint（起点列字段名集合）——起点 taint =
		// 起点列名（与内存路径一致，Q212 同步时遗漏——起点空 taint 导致
		// 整链 taint 空，fk 判定不一致）
		stCol := st.name
		if i := strings.Index(stCol, "."); i >= 0 {
			stCol = stCol[i+1:]
		}
		tainted := map[string][]string{st.id: {stCol}}
		queue := []bfsNode{{id: st.id, taint: []string{stCol}}}
		for len(queue) > 0 {
			curNode := queue[0]
			cur := curNode.id
			queue = queue[1:]
			depth := visited[cur]
			if depth >= maxDepth {
				continue
			}

			// Q199：argument/returns 只沿正向穿越（实参→形参）——
			// 无向遍历时形参反向回实参会把调用方的其他调用串入。
			// Q212：join 两端节点元数据（kind/access/type_string/name）——
			// 值级 taint 传播与内存路径（rg_relationsfor.go）一致：
			//   a. read 字段节点 → 出边值：taint 与该字段名求交
			//   b. 对象（*/struct ssa_value）→ 字段写节点：taint 不延续
			//      （写节点的值由写入值决定，基地址对象只是取址）
			ns, err := r.Query(`SELECT e.source_id, e.target_id, e.kind,
				ns.kind, json_extract(ns.properties, '$.access_kind'),
				json_extract(ns.properties, '$.type_string'), ns.name,
				nt.kind, json_extract(nt.properties, '$.access_kind'),
				json_extract(nt.properties, '$.type_string'), nt.name
			  FROM edges e
			  JOIN nodes ns ON ns.id = e.source_id
			  JOIN nodes nt ON nt.id = e.target_id
			  WHERE e.kind IN (`+dataKinds+`) AND (e.source_id = ? OR e.target_id = ?)`, cur, cur)
			if err != nil {
				return nil, err
			}
			var next []string
			for ns.Next() {
				var src, tgt, kind string
				var sk, tk string
				var sa, ta sql.NullString // access_kind/type_string 可为 NULL（ssa_value 无 access）
				var st, tt sql.NullString
				var sn, tn string
				if err := ns.Scan(&src, &tgt, &kind, &sk, &sa, &st, &sn, &tk, &ta, &tt, &tn); err != nil {
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
				// cur/other 元数据（src==cur 时 cur=ns 行、other=nt 行）
				curKind, curAccess, curType, curName := sk, sa.String, st.String, sn
				oKind, oAccess, oType := tk, ta.String, tt.String
				if tgt == cur {
					curKind, curAccess, curType, curName = tk, ta.String, tt.String, tn
					oKind, oAccess, oType = sk, sa.String, st.String
				}
				// Q220b：error 类型值不进入 BFS（与内存路径一致）——
				// 多返回值元组 (T, error) 共享节点，err 跨函数传播链把
				// 无关函数串入（approval_log.id → err → 支付单 id →
				// pay_divide.pay_id 假 fk 链）。error 不携带业务列值。
				if oType == "error" {
					continue
				}
				t := curNode.taint
				if curKind == string(domain.KindFieldAccess) && curAccess == "read" && src == cur {
					if cn := colNameOf(curName); cn != "" {
						t = intersectTaint(curNode.taint, []string{cn})
					}
				}
				if curKind == string(domain.KindSSAValue) &&
					(strings.HasPrefix(curType, "*") || strings.Contains(curType, "struct")) &&
					oKind == string(domain.KindFieldAccess) && oAccess == "write" {
					t = nil
				}
				visited[other] = depth + 1
				crossed[other] = curNode.crossed || isDirectedKind(kind)
				tainted[other] = t
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
					for _, bid := range bridge {
						// Q218：桥接延续 taint（与内存路径一致——Q212 同步时
						// 遗漏，SQL 路径桥接 taint 空 → fk 判定不一致）
						tainted[bid] = curNode.taint
						queue = append(queue, bfsNode{id: bid, crossed: crossed[bid], taint: curNode.taint})
					}
				}
			}
			for _, id := range next {
				// Q218：入队携带 taint（ns 循环已写入 tainted——此前遗漏，
				// 出队时 curNode.taint 恒空 → 整链 taint 断，fk 判定失效）
				queue = append(queue, bfsNode{id: id, crossed: crossed[id], taint: tainted[id]})
			}
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
				// Q212（原 Q199 一律丢弃）：跨函数 write——链上值级 taint
				// （起点列字段名）与终点列呼应（id ⊆ order_id）则字段值
				// 真实传递保留；仅对象整体传递无 taint 或不呼应则丢弃。
				// 判定与内存路径完全一致（fkColMatches/pkColMatches/
				// taintMatches 共用）：
				//   Q202b：无值流 taint 时外键列名回退（外键值即使来自
				//     请求参数，业务上也引用本表主键）
				//   Q202c：跨函数 write 目标列须外键形态（呼应本表名）
				if crossed[id] {
					if !(fkColMatches(col, table) && pkColMatches(fromCol, table)) &&
						!taintMatches(tainted[id], col) {
						continue
					}
					if !fkColMatches(col, table) {
						continue
					}
				}
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
			// Q218：值级 taint 验证（同内存路径）——终点 taint 与终点列
			// 呼应 → fk（真实键关联）；对象字段换名型噪声保持 query
			if rtype == domain.RelationQuery && taintMatches(tainted[id], col) {
				rtype = domain.RelationFK
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

	// Q212：filterFKNoise 统一用共享函数（原内联版缺 query 豁免——
	// hasFK 时 id 起点过滤不作用于 query，attr.id → attr_item.attr_id
	// 键关联被误滤；与内存路径分叉）
	return filterFKNoise(all), nil
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
