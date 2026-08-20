package sqlite

import "strings"

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

// taintExact taint 中任一字段与列名完全同名（大小写不敏感）——同名列
// 跨表双写（业务 id 同源：a_tab.biz_id 写入值 → b_tab.biz_id）为
// 强呼应，值流真实传递，无需外键形态兜底（Q225；与 taintMatches 的
// 弱呼应 id ⊆ res_id 区分——弱呼应仍要求目标列外键形态）。
func taintExact(taint []string, col string) bool {
	for _, tf := range taint {
		if strings.EqualFold(tf, col) {
			return true
		}
	}
	return false
}

// colMatchFold 列名与字段名对照：去下划线后大小写不敏感比较——ORM
// snake_case 映射（BizID ≈ biz_id、OrderID ≈ order_id），且无需
// commonInitialisms 表（ID/Id/id 归一为 id；ResId ≈ res_id 与 id
// 仍不匹配，Q202 role 案例不回归）。
func colMatchFold(a, b string) bool {
	na := strings.ReplaceAll(strings.ToLower(a), "_", "")
	nb := strings.ReplaceAll(strings.ToLower(b), "_", "")
	return na == nb
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

// isKeyCol 键形态列（Q234）：下划线归一后等于 id 或以 id 结尾
// （id/biz_id/user_id/order_id/BuyerId）——where 条件字段提升 fk 时
// 排除 create_time/status 等非键字段（create_time 不以 id 结尾）。
func isKeyCol(col string) bool {
	c := strings.ToLower(strings.ReplaceAll(col, "_", ""))
	return c == "id" || strings.HasSuffix(c, "id")
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
