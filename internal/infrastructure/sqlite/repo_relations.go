package sqlite

import (
	"database/sql"
	"encoding/json"
	"sort"
	"strings"

	"github.com/schaepher/codeintel/internal/domain"
	"go.uber.org/zap"
)

// GetTableRelations 表间关联分析（query relations）：对该表的全部列
// 虚拟节点沿数据流边 BFS，收集命中其他表的虚拟节点（表.列，is_external）。
// P0② 一次加载全图到内存（loadRelationGraph）替代逐节点 SQL；P0③ 结果
// 按 build_id 缓存到 relation_candidates，命中直接返回（无 build_metadata
// 时跳过缓存）。mode 为 --memory（""=auto 按规模、full=强制内存图、
// sql=强制逐节点 SQL——大仓库防爆内存逃生口）。无外键依赖——纯代码使用
// 方式推断（A.x 读出值流入 B.y 过滤/写入）。
func (r *Repo) GetTableRelations(table, mode string) ([]*domain.TableRelation, error) {
	logger := zap.L()
	logger.Debug("enter (Repo).GetTableRelations")
	defer logger.Debug("exit (Repo).GetTableRelations")

	if rels, ok := r.loadRelationCandidates(table); ok {
		return rels, nil
	}
	var rels []*domain.TableRelation
	var err error
	if r.useMemoryGraph(mode) {
		var g *relationGraph
		if g, err = loadRelationGraph(r); err == nil {
			rels = g.relationsFor(table)
		}
	} else {
		rels, err = r.relationsForSQL(table)
	}
	if err != nil {
		return nil, err
	}
	r.saveRelationCandidates(table, rels)
	return rels, nil
}

// GetTables 枚举全库外部表名（gorm/sql 虚拟节点表名去重，Q160）。
func (r *Repo) GetTables() ([]string, error) {
	logger := zap.L()
	logger.Debug("enter (Repo).GetTables")
	defer logger.Debug("exit (Repo).GetTables")
	rows, err := r.Query(`SELECT DISTINCT substr(name, 1, instr(name, '.') - 1) FROM nodes
		WHERE kind = 'field_access' AND json_extract(properties, '$.is_external') = 'true'
		  AND json_extract(properties, '$.type_string') IN ('gorm', 'sql', 'xorm')
		  AND name NOT LIKE '%.%.%' ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		if t == "" {
			continue
		}
		tables = append(tables, t)
	}
	return tables, rows.Err()
}

// GetAllTableRelations 全库关联聚合（Q160）：一次加载图（loadRelationGraph），
// 全部表内存 BFS 合并去重——同 from/to 列对取 hops 最小 + Type 最高
// （query > write > read）。结果按 build_id 全量写入 relation_candidates
// （--all 重建缓存，后续单表查询命中缓存）。输出按 from/to 稳定排序，
// AGENT 一次调用拿全库（query relations --all / export relations）。
// mode 同 GetTableRelations（--memory）；sql 模式逐表走 relationsForSQL。
func (r *Repo) GetAllTableRelations(mode string) ([]*domain.TableRelation, error) {
	logger := zap.L()
	logger.Debug("enter (Repo).GetAllTableRelations")
	defer logger.Debug("exit (Repo).GetAllTableRelations")
	if !r.useMemoryGraph(mode) {
		return r.getAllTableRelationsSQL()
	}
	g, err := loadRelationGraph(r)
	if err != nil {
		return nil, err
	}
	tables := g.tables()
	seen := map[string]*domain.TableRelation{}
	for _, t := range tables {
		for _, rel := range g.relationsFor(t) {
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

// relTypeRank 关联类型优先级（聚合去重用）：query > write > read。
func relTypeRank(t string) int {
	switch t {
	case domain.RelationQuery:
		return 2
	case domain.RelationWrite:
		return 1
	default:
		return 0
	}
}

// GetTableColumns 按表名聚合列虚拟节点（query table）：Name=表（整表行）
// 或 表.列（Q97 持久化映射）；每列带写入方（summary_io 入边 source 值节点
// 的所属函数与行号）。读取方（出边）通常为空——SELECT 读路径未解析。
func (r *Repo) GetTableColumns(table string) ([]*domain.TableColumn, error) {
	logger := zap.L()
	logger.Debug("enter (Repo).GetTableColumns")
	defer logger.Debug("exit (Repo).GetTableColumns")
	rows, err := r.Query(`SELECT id, name, line_start, properties
		FROM nodes WHERE kind = 'field_access'
		  AND json_extract(properties, '$.is_external') = 'true'
		  AND (name = ? OR name LIKE ?)
		ORDER BY name, id`, table, table+".%")
	if err != nil {
		return nil, err
	}
	// 先收完外层行再关（SQLite 单连接：迭代中开新 Query 会挂起）
	type rowT struct {
		id, name, access string
		line             int
	}
	var raw []rowT
	for rows.Next() {
		var id, name, props string
		var line int
		if err := rows.Scan(&id, &name, &line, &props); err != nil {
			return nil, err
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(props), &m); err != nil {
			return nil, err
		}
		access := ""
		if a, ok := m["access_kind"].(string); ok {
			access = a
		}
		raw = append(raw, rowT{id: id, name: name, access: access, line: line})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()

	cols := map[string]*domain.TableColumn{}
	var order []string
	for _, rt := range raw {
		col, ok := cols[rt.name]
		if !ok {
			col = &domain.TableColumn{Name: rt.name, Access: rt.access, LineStart: rt.line}
			cols[rt.name] = col
			order = append(order, rt.name)
		}

		ws, err := r.Query(`SELECT source_id, json_extract(metadata, '$.line_num')
			FROM edges WHERE target_id = ? AND kind = 'summary_io'`, rt.id)
		if err != nil {
			return nil, err
		}
		for ws.Next() {
			var src string
			var ln sql.NullFloat64
			if err := ws.Scan(&src, &ln); err != nil {
				ws.Close()
				return nil, err
			}

			funcID := src
			if i := strings.LastIndex(src, "#"); i >= 0 {
				funcID = src[:i]
			}

			line := rt.line
			if ln.Valid {
				line = int(ln.Float64)
			}
			col.Writers = append(col.Writers, domain.TableEndpoint{
				FuncID:   funcID,
				FuncName: shortNameFromID(funcID),
				Line:     line,
			})
		}
		ws.Close()

		if rt.access == "read" {
			rs, err := r.Query(`SELECT target_id, json_extract(metadata, '$.line_num')
				FROM edges WHERE source_id = ? AND kind = 'summary_io'`, rt.id)
			if err != nil {
				return nil, err
			}
			for rs.Next() {
				var tgt string
				var ln sql.NullFloat64
				if err := rs.Scan(&tgt, &ln); err != nil {
					rs.Close()
					return nil, err
				}
				funcID := tgt
				if i := strings.LastIndex(tgt, "#"); i >= 0 {
					funcID = tgt[:i]
				}
				line := rt.line
				if ln.Valid {
					line = int(ln.Float64)
				}
				col.Readers = append(col.Readers, domain.TableEndpoint{
					FuncID:   funcID,
					FuncName: shortNameFromID(funcID),
					Line:     line,
				})
			}
			rs.Close()
		}
	}
	out := make([]*domain.TableColumn, 0, len(order))
	for _, name := range order {
		out = append(out, cols[name])
	}
	return out, nil
}
