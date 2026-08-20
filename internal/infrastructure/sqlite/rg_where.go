package sqlite

import (
	"strings"

	"github.com/schaepher/codeintel/internal/domain"
)

// colNode 外部列节点（id/表.列名/access_kind）——SQL 路径 BFS 起点与
// 规则 B filter 筛选共用。
type colNode struct{ id, name, access string }

// collectWhereMeta SQL 路径（relationsForSQL）Q234 元数据：全库 where
// 条件字段集（"table.col" → 规则 A 提升）与其他表列集合（规则 B 直接
// 识别）——一次全表查询收集。
func collectWhereMeta(r *Repo, table string) (map[string]bool, map[string]map[string]bool, error) {
	whereCols := map[string]bool{}
	otherCols := map[string]map[string]bool{}
	fc, err := r.Query(`SELECT name, json_extract(properties, '$.access_kind') FROM nodes
		WHERE kind = 'field_access' AND json_extract(properties, '$.is_external') = 'true'`)
	if err != nil {
		return nil, nil, err
	}
	defer fc.Close()
	for fc.Next() {
		var nm, acc string
		if err := fc.Scan(&nm, &acc); err != nil {
			return nil, nil, err
		}
		if !strings.Contains(nm, ".") {
			continue
		}
		if acc == "filter" {
			whereCols[nm] = true
		}
		dot := strings.Index(nm, ".")
		t := nm[:dot]
		if t == table {
			continue
		}
		if otherCols[t] == nil {
			otherCols[t] = map[string]bool{}
		}
		otherCols[t][nm[dot+1:]] = true
	}
	return whereCols, otherCols, fc.Err()
}

// whereDirectRelsSQL SQL 路径规则 B（与内存版 whereDirectRels 语义
// 一致）：本表 filter 起点按列名呼应直接识别 fk——外键形态（user_id →
// user.id）/ 同名键列（biz_id ↔ 另一表 biz_id）；自表主键与非键字段
// 不识别。同 key 走 seen 合并（fk rank 最高）。
func whereDirectRelsSQL(seen map[string]*domain.TableRelation, all []*domain.TableRelation,
	table string, starts []colNode, otherCols map[string]map[string]bool) []*domain.TableRelation {
	for _, st := range starts {
		if st.access != "filter" {
			continue
		}
		col := st.name
		if i := strings.Index(col, "."); i >= 0 {
			col = col[i+1:]
		}
		if pkColMatches(col, table) {
			continue
		}
		for t, cols := range otherCols {
			if fkColMatches(col, t) && cols["id"] {
				all = mergeRelation(seen, all, st.name+"|"+t+"|id", &domain.TableRelation{
					FromTable: table, FromCol: col, ToTable: t, ToCol: "id",
					Hops: 0, Type: domain.RelationFK,
				})
				break
			}
		}
		if isKeyCol(col) {
			for t, cols := range otherCols {
				for c := range cols {
					if colMatchFold(col, c) {
						all = mergeRelation(seen, all, st.name+"|"+t+"|"+c, &domain.TableRelation{
							FromTable: table, FromCol: col, ToTable: t, ToCol: c,
							Hops: 0, Type: domain.RelationFK,
						})
						break
					}
				}
			}
		}
	}
	return all
}

// Q234 规则 B：where 条件字段直接识别——本表查询时 where 使用的列
// （filter 节点）按列名呼应直接生成 fk 关联（BFS 值流之外：where 参数
// 来自请求/字面量时 BFS 不通，仍能识别——Q220c merchant_id 案例）。
// 呼应两种方式（用户确认两者都做）：
//   - 外键形态：列与另一表表名呼应（user_id ↔ user 表）→ 连该表 id
//   - 同名键列：列与另一表键列呼应（biz_id ↔ 另一表 biz_id）→ 连该列
//
// 自表主键（WHERE id=? 是主键查询非外键）与非键字段（create_time/
// status 不以 id 结尾）不识别。返回值与 BFS 结果同 key 去重（fk rank
// 最高，覆盖低 rank 同 key 行）。
func (g *relationGraph) whereDirectRels(table string) []*domain.TableRelation {
	// 本表 filter 节点 + 其他表列集合（"table.col" → access）
	var filters []string
	otherCols := map[string]map[string]bool{}
	for _, n := range g.nodes {
		if n.kind != string(domain.KindFieldAccess) || !n.isExternal ||
			!relTypeStrings[n.typeString] || !strings.Contains(n.name, ".") {
			continue
		}
		dot := strings.Index(n.name, ".")
		t, c := n.name[:dot], n.name[dot+1:]
		if t == table {
			if n.access == "filter" {
				filters = append(filters, c)
			}
			continue
		}
		if otherCols[t] == nil {
			otherCols[t] = map[string]bool{}
		}
		otherCols[t][c] = true
	}
	if len(filters) == 0 {
		return nil
	}
	var out []*domain.TableRelation
	for _, col := range filters {
		if pkColMatches(col, table) {
			continue // 自表主键（WHERE id=?）——主键查询非外键
		}
		// 外键形态：col ↔ 表名（user_id → user.id）
		for t, cols := range otherCols {
			if fkColMatches(col, t) && cols["id"] {
				out = append(out, &domain.TableRelation{
					FromTable: table, FromCol: col, ToTable: t, ToCol: "id",
					Hops: 0, Type: domain.RelationFK,
				})
				break
			}
		}
		// 同名键列：col ↔ 另一表键列（biz_id ↔ biz_id；colMatchFold 归一
		// 处理 ID 大小写变体）。create_time/status 非键字段不识别。
		if isKeyCol(col) {
			for t, cols := range otherCols {
				for c := range cols {
					if colMatchFold(col, c) {
						out = append(out, &domain.TableRelation{
							FromTable: table, FromCol: col, ToTable: t, ToCol: c,
							Hops: 0, Type: domain.RelationFK,
						})
						break
					}
				}
			}
		}
	}
	return out
}
