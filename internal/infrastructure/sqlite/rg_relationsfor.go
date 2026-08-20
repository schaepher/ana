package sqlite

import (
	"sort"
	"strings"

	"github.com/schaepher/codeintel/internal/domain"
)

// relationsFor 表间关联 BFS（Q177）：本表全部列虚拟节点为起点，沿
// 数据流边无向扩散（跨函数 argument/returns 置 crossed；error 节点
// 阻断 Q220b；taint 值级传播 Q202/Q225），终点判定 filter→query /
// write→write / read，列名呼应分级（fk/query/read），同 key 去重取
// rank 最高 + hops 最小。taint/列名工具见 rg_taint.go。
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
		crossed := map[string]bool{} // 到达该节点的链是否经过跨函数边（argument/returns）
		stCol := st.name
		if i := strings.Index(stCol, "."); i >= 0 {
			stCol = stCol[i+1:]
		}
		tainted := map[string][]string{}
		queue := []bfsNode{{id: st.id, taint: []string{stCol}}}
		tainted[st.id] = queue[0].taint
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			depth := visited[cur.id]
			if depth >= relationsMaxDepth {
				continue
			}

			for _, other := range g.dataAdj[cur.id] {
				if _, ok := visited[other]; ok {
					continue
				}
				// Q220b：error 类型值不进入 BFS——多返回值元组 (T, error)
				// 共享节点，err 元素与业务值元素（如 (int, error) 的 int）
				// 被无向边连在一起，err 跨函数传播链会把无关函数串入
				// （go2o 实测假链：approval_log.id → err → ... → 支付单
				// id → pay_divide.pay_id [12跳 fk]）。error 不携带业务列值。
				if n := g.nodes[other]; n != nil && n.typeString == "error" {
					continue
				}
				visited[other] = depth + 1
				crossed[other] = cur.crossed || g.crossEdges[cur.id][other]
				// Q202 值级 taint 传播：字段读节点 → 解引用值 时 taint 与
				// 该字段名求交（role.Id.read 只取出 id 的 taint——create_time
				// 的 taint 不流入 id 值）；其余边 taint 延续（对象整体携带
				// 起点字段 taint，字段赋值处延续到目标对象）
				t := cur.taint
				if n := g.nodes[cur.id]; n != nil && n.kind == string(domain.KindFieldAccess) &&
					n.access == "read" && contains(g.allOut[cur.id], other) {
					if cn := colNameOf(n.name); cn != "" {
						t = intersectTaint(cur.taint, []string{cn})
					}
				}
				// Q202 精确化：对象（指针/结构体）→ 字段写节点不延续对象
				// 整体 taint，只取与字段名呼应的部分（与字段读求交对称，
				// Q225 修正）：字段写节点的值由写入值（另一条边）决定，
				// 基址对象只取址——但对象 taint 若与字段名呼应（如对象
				// taint={biz_id} 写 BizID 字段）正是该字段的真实值流
				// （业务 id 先 insert 表 A 再 update 表 B 的同源双写，
				// 跨函数时原「置 nil」把链上 a.BizID.write 清零导致终点
				// taint 丢失）。role taint={id} 写 ResId → 对照空仍丢弃
				// （Q202 go2o 案例不回归）。isExternal 虚拟列节点豁免——
				// 虚拟列的值来源就是对象字段映射（ORM 类型展开）
				if n := g.nodes[cur.id]; n != nil && n.kind == string(domain.KindSSAValue) &&
					(strings.HasPrefix(n.typeString, "*") || strings.Contains(n.typeString, "struct")) {
					if on := g.nodes[other]; on != nil && on.kind == string(domain.KindFieldAccess) &&
						on.access == "write" && !on.isExternal {
						if cn := colNameOf(on.name); cn != "" {
							t = nil
							for _, x := range cur.taint {
								if colMatchFold(x, cn) {
									t = append(t, x)
									break
								}
							}
						} else {
							t = nil
						}
					}
				}
				tainted[other] = t
				queue = append(queue, bfsNode{id: other, crossed: crossed[other], taint: t})
			}

			if n := g.nodes[cur.id]; n != nil && n.funcID != "" {
				if tn, ok := g.typeNameOf(cur.id); ok && tn != "" {
					for _, n2 := range g.readsByFunc[n.funcID] {
						if !strings.Contains(n2.fullPath, tn) || !g.filterReachable2(n2.id) {
							continue
						}
						if _, ok := visited[n2.id]; !ok {
							visited[n2.id] = depth + 1
							crossed[n2.id] = cur.crossed
							queue = append(queue, bfsNode{id: n2.id, crossed: crossed[n2.id], taint: cur.taint})
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
				// Q199/Q202：跨函数 write——链上值级 taint（起点列字段名）
				// 与终点列呼应（id ⊆ order_id）则字段值真实传递（order.id
				// 读出 → 赋 A.order_id），保留；仅对象整体传递无 taint 或
				// 不呼应则丢弃（create_time → res.id 假同源）。
				// Q202b：无值流 taint 时外键列名回退——写入列是外键模式
				// （xxx_id/xxx 与表名呼应，如 rbac_role_res.role_id ↔
				// rbac_role）时保留：外键值即使来自请求参数，业务上
				// 也引用本表主键（用户确认）
				if crossed[id] {
					if !(fkColMatches(col, table) && pkColMatches(fromCol, table)) &&
						!taintMatches(tainted[id], col) {
						continue
					}
					// Q202c：跨函数 write 目标列须外键形态（呼应本表名）——
					// role.id → res_id 虽值流 taint 呼应（{id} ⊆ res_id），
					// 但 res_id 是资源 id 非角色外键（值仅同函数上下文
					// 连通，非直接关系），不展示；role_id/order_id 呼应
					// Q225：taintExact 豁免——与终点列完全同名（biz_id =
					// biz_id）是同名列双写的强呼应（业务 id 同源），值流
					// 真实传递，不因非外键形态丢弃
					if !fkColMatches(col, table) && !taintExact(tainted[id], col) {
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
			// Q218：值级 taint 验证——终点 taint（起点列字段名，经 lowercase
			// 求交传播）与终点列呼应 → 真实键关联（fk）。对象字段换名型噪声
			// （pay_order.id → t15.BuyerId：id 与 BuyerId 求交为空）终点
			// taint 空 → 保持 query。fk 是 ER 图默认连线类型。
			if rtype == domain.RelationQuery && taintMatches(tainted[id], col) {
				rtype = domain.RelationFK
			}
			// Q234 规则 A：where 条件字段增强——终点列被查询 where 用作
			// 条件（filter 节点存在）通常有外键：query 筛选为 fk / 同源
			// 写提升为 fk（biz_id 先 insert 表 A 再 update 表 B 且被查询
			// 条件使用 → 真实键关联）。isKeyCol 排除 create_time 等非键
			// 字段；呼应（同名键列 / 外键形态 / 值流 taint）防 Q218 换名
			// 噪声（t15.BuyerId 全不满足保持 query）。
			if (rtype == domain.RelationQuery || rtype == domain.RelationWrite) &&
				g.whereCols[otherTable+"."+col] && isKeyCol(col) &&
				(colMatchFold(col, fromCol) || fkColMatches(col, table) ||
					taintMatches(tainted[id], col)) {
				rtype = domain.RelationFK
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

	// Q234 规则 B：where 条件字段直接识别（BFS 值流之外——where 参数
	// 来自请求/字面量时 BFS 不通）——filter 字段按列名呼应直接 fk。
	// 同 key 走 seen 去重（fk rank 最高覆盖 BFS 低 rank 行）。
	for _, rel := range g.whereDirectRels(table) {
		key := table + "." + rel.FromCol + "|" + rel.ToTable + "|" + rel.ToCol
		all = mergeRelation(seen, all, key, rel)
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
