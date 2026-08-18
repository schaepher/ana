package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/schaepher/codeintel/internal/action"
	"github.com/schaepher/codeintel/internal/domain"
)

// relationsFilter P0④ 输出过滤：--type/--max-hops/--max-results。
// 默认类型：query + write（read 低置信间接扩散，--type read 显式展开）。
func relationsFilter(f *queryFlags) func([]*domain.TableRelation) []*domain.TableRelation {
	types := map[string]bool{}
	for _, t := range f.relTypes {
		if t = strings.TrimSpace(t); t != "" {
			types[t] = true
		}
	}
	if len(types) == 0 {
		types[domain.RelationQuery] = true
		types[domain.RelationWrite] = true
	}
	return func(rels []*domain.TableRelation) []*domain.TableRelation {
		out := make([]*domain.TableRelation, 0, len(rels))
		for _, r := range rels {
			if !types[r.Type] {
				continue
			}
			if f.maxHops > 0 && r.Hops > f.maxHops {
				continue
			}
			out = append(out, r)
		}
		if f.maxResults > 0 && len(out) > f.maxResults {
			out = out[:f.maxResults]
		}
		return out
	}
}

// queryRelations 实现 `codeintel query relations <表名> [--mermaid]`：
// 表间关联分析——本表列的值沿数据流链流入其他表列（A.x 读出 → B.y
// 过滤/写入，代码层推断，无外键依赖）。--mermaid 输出列级 mermaid 图；
// --type/--max-hops/--max-results 过滤输出；--memory full|sql 选择实现
// 路径（默认 auto 按规模）。
func queryRelations(acts *action.Actions, table, format string, opts outputOpts, f *queryFlags) int {
	acts.IncludeLongQuery(f.includeLongQuery)
	rels, err := acts.Relations(table, f.memory)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	rels = relationsFilter(f)(rels)
	if format == "mermaid" {
		return printRelationsMermaid(table, rels)
	}
	if opts.json {
		if rels == nil {
			rels = []*domain.TableRelation{} // nil slice 会输出 null——无关联时输出 []
		}
		data, err := json.MarshalIndent(rels, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		fmt.Println(string(data))
		return 0
	}
	if len(rels) == 0 {
		fmt.Printf("表 %s：无关联表（数据流链上未命中其他表的列）\n", table)
		return 0
	}
	// 按类型分组展示：query（键关联）优先
	sort.SliceStable(rels, func(i, j int) bool {
		if rels[i].Type != rels[j].Type {
			return rels[i].Type == domain.RelationQuery
		}
		return rels[i].Hops < rels[j].Hops
	})
	fmt.Printf("表 %s 关联（数据流链推断，%d 条）:\n", table, len(rels))
	var q, w int
	for _, r := range rels {
		tag := ""
		switch r.Type {
		case domain.RelationQuery:
			tag = " [查询关联]"
			q++
		case domain.RelationWrite:
			tag = " [同源写入]"
			w++
		}
		fmt.Printf("  %s.%s → %s.%s  [%d 跳]%s\n", r.FromTable, r.FromCol, r.ToTable, r.ToCol, r.Hops, tag)
	}
	fmt.Printf("  （query=%d 键关联 / write=%d 同源 / read=%d 间接）\n", q, w, len(rels)-q-w)
	return 0
}

// queryRelationsAll 实现 `codeintel query relations --all`（Q160）：
// 一次遍历全部表返回所有表对关联（合并去重），AGENT 单次调用拿全库。
// --json 输出数组（与单表同构）；文本模式按表分组展示。
func queryRelationsAll(acts *action.Actions, format string, opts outputOpts, f *queryFlags) int {
	acts.IncludeLongQuery(f.includeLongQuery)
	rels, err := acts.RelationsAll(f.memory)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	rels = relationsFilter(f)(rels)
	if format == "mermaid" {
		return printRelationsAllMermaid(rels)
	}
	if opts.json {
		if rels == nil {
			rels = []*domain.TableRelation{} // nil slice 会输出 null
		}
		data, err := json.MarshalIndent(rels, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		fmt.Println(string(data))
		return 0
	}
	if len(rels) == 0 {
		fmt.Println("全库无表间关联（数据流链上未命中其他表的列）")
		return 0
	}
	// 按 from 表分组，组内 query 优先、跳数升序
	byFrom := map[string][]*domain.TableRelation{}
	for _, r := range rels {
		byFrom[r.FromTable] = append(byFrom[r.FromTable], r)
	}
	var fromTables []string
	for t := range byFrom {
		fromTables = append(fromTables, t)
	}
	sort.Strings(fromTables)
	fmt.Printf("全库表间关联（数据流链推断，共 %d 条 / %d 张表）:\n", len(rels), len(fromTables))
	var q, w int
	for _, ft := range fromTables {
		list := byFrom[ft]
		sort.SliceStable(list, func(i, j int) bool {
			if list[i].Type != list[j].Type {
				return list[i].Type == domain.RelationQuery
			}
			return list[i].Hops < list[j].Hops
		})
		fmt.Printf("  [%s]\n", ft)
		for _, r := range list {
			tag := ""
			switch r.Type {
			case domain.RelationQuery:
				tag = " [查询关联]"
				q++
			case domain.RelationWrite:
				tag = " [同源写入]"
				w++
			}
			fmt.Printf("    %s.%s → %s.%s  [%d 跳]%s\n", r.FromTable, r.FromCol, r.ToTable, r.ToCol, r.Hops, tag)
		}
	}
	fmt.Printf("  （query=%d 键关联 / write=%d 同源 / read=%d 间接）\n", q, w, len(rels)-q-w)
	return 0
}

// printRelationsAllMermaid 全库列级 mermaid 图（query relations --all --mermaid）：
// 表为子图（列节点），关联为列到列的边（query 类型粗线）。
func printRelationsAllMermaid(rels []*domain.TableRelation) int {
	var sb strings.Builder
	sb.WriteString("flowchart LR\n")
	// 列节点 + 表子图
	byTable := map[string]map[string]bool{} // 表 → 列集合
	for _, r := range rels {
		if byTable[r.FromTable] == nil {
			byTable[r.FromTable] = map[string]bool{}
		}
		byTable[r.FromTable][r.FromCol] = true
		if byTable[r.ToTable] == nil {
			byTable[r.ToTable] = map[string]bool{}
		}
		byTable[r.ToTable][r.ToCol] = true
	}
	var tableNames []string
	for t := range byTable {
		tableNames = append(tableNames, t)
	}
	sort.Strings(tableNames)
	for _, t := range tableNames {
		sb.WriteString(fmt.Sprintf("  subgraph %s[\"%s\"]\n", t, t))
		for c := range byTable[t] {
			sb.WriteString(fmt.Sprintf("    %s[\"%s.%s\"]\n", colID(t, c), t, c))
		}
		sb.WriteString("  end\n")
	}
	// 列间边
	for _, r := range rels {
		style := " --> "
		if r.Type == domain.RelationQuery {
			style = " ==> "
		}
		sb.WriteString(fmt.Sprintf("  %s%s%s\n",
			colID(r.FromTable, r.FromCol), style, colID(r.ToTable, r.ToCol)))
	}
	fmt.Println(sb.String())
	return 0
}

// printRelationsMermaid 输出列级 mermaid 图：表为子图（列节点），
// 关联为列到列的边（query 类型粗线）。
func printRelationsMermaid(fromTable string, rels []*domain.TableRelation) int {
	var sb strings.Builder
	sb.WriteString("flowchart LR\n")
	// 本表列节点
	fromCols := map[string]bool{}
	for _, r := range rels {
		fromCols[r.FromCol] = true
	}
	for c := range fromCols {
		sb.WriteString(fmt.Sprintf("  %s[\"%s.%s\"]\n", colID(fromTable, c), fromTable, c))
	}
	// 关联表列节点（子图按表分组）
	byTable := map[string][]*domain.TableRelation{}
	for _, r := range rels {
		byTable[r.ToTable] = append(byTable[r.ToTable], r)
	}
	for tt, list := range byTable {
		sb.WriteString(fmt.Sprintf("  subgraph %s[\"%s\"]\n", tt, tt))
		for _, r := range list {
			sb.WriteString(fmt.Sprintf("    %s[\"%s.%s\"]\n", colID(tt, r.ToCol), tt, r.ToCol))
		}
		sb.WriteString("  end\n")
	}
	// 列间边（query 类型粗线标注）
	seen := map[string]bool{}
	for _, r := range rels {
		key := r.FromCol + "|" + r.ToTable + "|" + r.ToCol
		if seen[key] {
			continue
		}
		seen[key] = true
		style := " --> "
		if r.Type == domain.RelationQuery {
			style = " ==> "
		}
		sb.WriteString(fmt.Sprintf("  %s%s%s[\"%s.%s\"]\n",
			colID(fromTable, r.FromCol), style, colID(r.ToTable, r.ToCol), r.ToTable, r.ToCol))
	}
	fmt.Println(sb.String())
	return 0
}

// colID mermaid 节点 ID（表名.列名 转义为合法标识符）。
func colID(table, col string) string {
	return strings.ReplaceAll(table+"_"+col, ".", "_")
}
