package sqlite

import (
	"sort"
	"strings"

	"github.com/schaepher/codeintel/internal/domain"
)

// bfsNode BFS 队列元素：id + 链是否已跨函数（Q199）+ 值级 taint
// （Q202：起点列字段名集合——跨函数 write 时用 taint 与终点列呼应
// 判定字段值是否真实传递；仅对象整体传递无 taint 则丢弃）
type bfsNode struct {
	id      string
	crossed bool
	taint   []string
}

// colNameOf 节点名的列部分（"a.OrderId" → "OrderId"；无点返回空）。
func colNameOf(name string) string {
	if i := strings.LastIndex(name, "."); i >= 0 {
		return name[i+1:]
	}
	return ""
}

// contains 切片包含判断。
func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// intersectTaint 两集合交集（Q218：lowercase 比较——Go 字段名 Id 与
// 列名 id 是同一逻辑值，精确匹配永远为空，taint 链在真实链上断裂）。
func intersectTaint(a, b []string) []string {
	var out []string
	for _, x := range a {
		for _, y := range b {
			if strings.EqualFold(x, y) {
				out = append(out, x)
				break
			}
		}
	}
	return out
}

// taintMatches taint 中任一字段与列名呼应（Q159 规则：a_id 含 id 或
// id 含 a_id，大小写不敏感）——order_id 与 id 呼应，create_time 与
// id 不呼应。
func taintMatches(taint []string, col string) bool {
	if len(taint) == 0 {
		return false
	}
	lc := strings.ToLower(col)
	for _, tf := range taint {
		lt := strings.ToLower(tf)
		if strings.HasSuffix(lc, lt) || strings.HasSuffix(lt, lc) {
			return true
		}
	}
	return false
}

// fkColMatches 外键列名与表名呼应（Q202b）：col=xxx_id/xxx，表名以
// xxx 结尾（rbac_role_res.role_id base=role ↔ rbac_role）或相等
// （含 _ 前缀形式）。id/xxx 无 base 不匹配（create_time → id 不受益）。
func fkColMatches(col, table string) bool {
	base := strings.ToLower(col)
	for _, suf := range []string{"_id", "id"} {
		if strings.HasSuffix(base, suf) && len(base) > len(suf) {
			base = base[:len(base)-len(suf)]
			break
		}
	}
	if base == "" {
		return false
	}
	tl := strings.ToLower(table)
	return tl == base || strings.HasSuffix(tl, "_"+base) || strings.HasSuffix(tl, base)
}

// pkColMatches 起点列是否主键形态（Q202b）：id 或表名单数
// （rbac_role 表的主键列 id）——外键回退仅主键列出发（防任意列误连）。
func pkColMatches(col, table string) bool {
	lc := strings.ToLower(col)
	if lc == "id" {
		return true
	}
	tl := strings.ToLower(table)
	base := tl
	for _, pre := range []string{"t_", "tb_"} {
		base = strings.TrimPrefix(base, pre)
	}
	base = strings.TrimSuffix(base, "s")
	return lc == base || lc == base+"_id"
}

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
				// Q202 精确化：对象（指针/结构体）→ 字段写节点不延续 taint
				// ——字段写节点的值由写入值（另一条边）决定，基地址对象只是
				// 取址，不携带字段值流（go2o 实测：role 对象整体传入后
				// t9.ResId.write 被误标 {id}，实际 res_id 来自请求参数）
				if n := g.nodes[cur.id]; n != nil && n.kind == string(domain.KindSSAValue) &&
					(strings.HasPrefix(n.typeString, "*") || strings.Contains(n.typeString, "struct")) {
					if on := g.nodes[other]; on != nil && on.kind == string(domain.KindFieldAccess) &&
						on.access == "write" {
						t = nil
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
			// Q218：值级 taint 验证——终点 taint（起点列字段名，经 lowercase
			// 求交传播）与终点列呼应 → 真实键关联（fk）。对象字段换名型噪声
			// （pay_order.id → t15.BuyerId：id 与 BuyerId 求交为空）终点
			// taint 空 → 保持 query。fk 是 ER 图默认连线类型。
			if rtype == domain.RelationQuery && taintMatches(tainted[id], col) {
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
