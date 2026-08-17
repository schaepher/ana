package sqlite

import (
	"database/sql"
	"sort"
	"strings"

	"github.com/schaepher/codeintel/internal/domain"
	"go.uber.org/zap"
)

// GetValueTrace 追踪一个数据值在整条链路上的处理过程（跨函数，无 func_id 限制）：
// 以任意数据节点（field_access / ssa_value / parameter）为锚点，双向遍历
// data_flows_to / argument / returns / phi_operand；
// Dir=0 为产生链（反向），Dir=1 为使用链（正向）；行带 func_id 供函数上下文分组。
// valueTraceFilter 字段访问步过滤（⑥ 字段精度）：
//   - 锚点字段（full_path 匹配）任意放行
//   - 实例路径前缀关系（嵌套容器/子字段：m.cfg ↔ m.cfg.APIKey——
//     full_path 是声明类型路径无前缀关系，须用 instance_path）放行
//   - 外部摘要虚拟节点（is_external：SQL 表.列 / GORM 表.列 / 事务边界）
//     放行——持久化映射点非"无关字段"
//   - 值出发的步按方向放行：正向仅写（值消费点/拷贝目标：
//     kg.ID → t42.ID.write）、反向仅读（值产生源/拷贝来源：
//     m.cfg.APIKey ← m.cfg、kg.ID.read ← kg）——字段访问 → 字段访问
//     仅限精确/前缀/external（嵌套扩散控制）
//
// tbl 为递归目标节点别名（反向 n_prev / 正向 n_next）。
func valueTraceFilter(anchorCtx, anchorInst string, reverse bool, tbl string, includeContainer bool) string {
	fp := `json_extract(` + tbl + `.properties, '$.full_path')`
	inst := `COALESCE(json_extract(` + tbl + `.properties, '$.instance_path'), json_extract(` + tbl + `.properties, '$.full_path'))`

	prefix := ""
	if includeContainer {
		prefix = `
OR (` + q(anchorCtx) + ` != '' AND (instr(` + q(anchorInst) + `, ` + inst + `) = 1 OR instr(` + inst + `, ` + q(anchorInst) + `) = 1))`
	}
	valueBridge := ""
	if reverse {
		valueBridge = `
OR json_extract(` + tbl + `.properties, '$.access_kind') = 'read'`
	} else {
		valueBridge = `
OR json_extract(` + tbl + `.properties, '$.access_kind') = 'write'`
	}
	return fp + ` = ` + q(anchorCtx) + prefix + valueBridge + `
OR json_extract(` + tbl + `.properties, '$.is_external') = 'true'
OR ` + q(anchorCtx) + ` = ''`
}
func q(s string) string { return "'" + s + "'" }

// sortEdgeKinds 排序边类型集合（Q155：GROUP_CONCAT(DISTINCT) 无序，
// server/CLI 按 LastIndex 取末段展示——排序保证输出稳定）。
func sortEdgeKinds(s string) string {
	if s == "" {
		return s
	}
	parts := strings.Split(s, ",")
	sort.Strings(parts)
	return strings.Join(parts, ",")
}
func (r *Repo) GetValueTrace(nodeID domain.CanonicalID, maxDepth int, minConf float64, includeContainer bool) ([]*domain.TraceRow, error) {
	logger := zap.L()
	logger.Debug("enter (Repo).GetValueTrace")
	defer logger.Debug("exit (Repo).GetValueTrace")
	if maxDepth <= 0 {
		maxDepth = 8
	}
	// 锚点字段上下文：field_access 锚点 → full_path（精确）+ instance_path
	// （前缀）；值锚点 → ''（对象级）
	var anchorCtx, anchorInst sql.NullString
	if err := r.QueryRow(`SELECT json_extract(properties, '$.full_path'),
		COALESCE(json_extract(properties, '$.instance_path'), json_extract(properties, '$.full_path'))
		FROM nodes WHERE id = ? AND kind = 'field_access'`, string(nodeID)).Scan(&anchorCtx, &anchorInst); err != nil {
		anchorCtx, anchorInst = sql.NullString{}, sql.NullString{}
	}
	ctx, inst := "", ""
	if anchorCtx.Valid {
		ctx, inst = anchorCtx.String, anchorInst.String
	}
	backFilter := valueTraceFilter(ctx, inst, true, "n_prev", includeContainer)
	fwdFilter := valueTraceFilter(ctx, inst, false, "n_next", includeContainer)

	rows, err := r.Query(`WITH RECURSIVE
vt(id, dir, depth, kind, seed, c_iface, c_origin, c_conf) AS (
    SELECT ?, 0, 0, (SELECT n0.kind FROM nodes n0 WHERE n0.id = ?), 1, '', '', 0
    UNION
    -- 反向：流向当前节点（产生链）
    SELECT e.source_id, 0, d.depth + 1, n_prev.kind, 0,
           CASE WHEN json_extract(e.metadata, '$.candidate_origin') IS NOT NULL
                THEN COALESCE(json_extract(e.metadata, '$.interface'), '') ELSE d.c_iface END,
           CASE WHEN json_extract(e.metadata, '$.candidate_origin') IS NOT NULL
                THEN json_extract(e.metadata, '$.candidate_origin') ELSE d.c_origin END,
           CASE WHEN json_extract(e.metadata, '$.candidate_origin') IS NOT NULL
                THEN COALESCE(json_extract(e.metadata, '$.confidence'), 0) ELSE d.c_conf END
    FROM edges e INDEXED BY idx_edges_target
    JOIN vt d ON e.target_id = d.id
    JOIN nodes n_prev ON e.source_id = n_prev.id
    WHERE d.dir = 0 AND d.depth < ? AND e.kind IN ('data_flows_to','argument','returns','phi_operand','summary_io')
      AND (n_prev.kind != 'field_access' OR (`+backFilter+`))
      AND (e.metadata IS NULL OR json_extract(e.metadata, '$.candidate_origin') IS NULL
           OR json_extract(e.metadata, '$.confidence') >= ?)
    UNION
    -- 正向：从当前节点流出（使用链）；锚点（seed）双向可展开
    SELECT e.target_id, 1, d.depth + 1, n_next.kind, 0,
           CASE WHEN json_extract(e.metadata, '$.candidate_origin') IS NOT NULL
                THEN COALESCE(json_extract(e.metadata, '$.interface'), '') ELSE d.c_iface END,
           CASE WHEN json_extract(e.metadata, '$.candidate_origin') IS NOT NULL
                THEN json_extract(e.metadata, '$.candidate_origin') ELSE d.c_origin END,
           CASE WHEN json_extract(e.metadata, '$.candidate_origin') IS NOT NULL
                THEN COALESCE(json_extract(e.metadata, '$.confidence'), 0) ELSE d.c_conf END
    FROM edges e INDEXED BY idx_edges_source
    JOIN vt d ON e.source_id = d.id
    JOIN nodes n_next ON e.target_id = n_next.id
    WHERE (d.dir = 1 OR d.seed = 1) AND d.depth < ? AND e.kind IN ('data_flows_to','argument','returns','phi_operand','summary_io')
      AND (n_next.kind != 'field_access' OR (`+fwdFilter+`))
      AND (e.metadata IS NULL OR json_extract(e.metadata, '$.candidate_origin') IS NULL
           OR json_extract(e.metadata, '$.confidence') >= ?)
)
SELECT dp.id, MIN(dp.depth), n.name,
       (SELECT COALESCE(GROUP_CONCAT(DISTINCT e2.kind), '') FROM edges e2
         WHERE ((dp.dir = 0 AND e2.target_id = dp.id) OR (dp.dir = 1 AND e2.source_id = dp.id))
           AND e2.kind IN ('data_flows_to','argument','returns','phi_operand','summary_io')),
       n.line_start, dp.dir, n.kind,
       json_extract(n.properties, '$.access_kind'), json_extract(n.properties, '$.func_id'),
       json_extract(n.properties, '$.full_path'),
       (SELECT v2.c_iface FROM vt v2 WHERE v2.id = dp.id AND v2.dir = dp.dir ORDER BY v2.depth LIMIT 1),
       (SELECT v2.c_origin FROM vt v2 WHERE v2.id = dp.id AND v2.dir = dp.dir ORDER BY v2.depth LIMIT 1),
       (SELECT v2.c_conf FROM vt v2 WHERE v2.id = dp.id AND v2.dir = dp.dir ORDER BY v2.depth LIMIT 1)
FROM vt dp JOIN nodes n ON n.id = dp.id
GROUP BY dp.id, dp.dir
ORDER BY dp.dir, MIN(dp.depth), dp.id`,
		string(nodeID), string(nodeID), maxDepth, minConf, maxDepth, minConf)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.TraceRow
	for rows.Next() {
		var (
			row      domain.TraceRow
			id       string
			line     sql.NullInt64
			dir      int
			kind     string
			access   sql.NullString
			funcID   sql.NullString
			fullPath sql.NullString
			cIface   sql.NullString
			cOrigin  sql.NullString
			cConf    sql.NullFloat64
		)
		if err := rows.Scan(&id, &row.Depth, &row.Name, &row.EdgeKinds, &line, &dir, &kind, &access, &funcID, &fullPath, &cIface, &cOrigin, &cConf); err != nil {
			return nil, err
		}
		row.ID = domain.CanonicalID(id)
		row.Dir = dir
		row.EdgeKinds = sortEdgeKinds(row.EdgeKinds)
		row.Kind = domain.EntityKind(kind)
		if access.Valid {
			row.Access = access.String
		}
		if funcID.Valid {
			row.FuncID = funcID.String
		}
		if fullPath.Valid {
			row.FullPath = fullPath.String
		}
		if line.Valid {
			row.Line = int(line.Int64)
		}

		if cIface.Valid {
			row.EdgeIface = cIface.String
		}
		if cOrigin.Valid {
			row.EdgeOrigin = cOrigin.String
		}
		if cConf.Valid {
			row.EdgeConf = cConf.Float64
		}
		out = append(out, &row)
	}
	return out, rows.Err()
}

// GetValueTraceMulti 多锚点合并正向追踪（⑧ 跳板合并）：一次查询返回
// 全部锚点的下游使用链（dir=1），字段访问步按锚点字段 ctx 限定。
// trampoline 用它替代 N 次 GetValueTrace——读点多时累计查询成本
// 大幅下降（单次 CTE + UNION 去重）。
func (r *Repo) GetValueTraceMulti(anchors []domain.CanonicalID, ctxField string, maxDepth int) ([]*domain.TraceRow, error) {
	logger := zap.L()
	logger.Debug("enter (Repo).GetValueTraceMulti")
	defer logger.Debug("exit (Repo).GetValueTraceMulti")
	if len(anchors) == 0 {
		return nil, nil
	}
	if maxDepth <= 0 {
		maxDepth = 4
	}
	ids := make([]string, 0, len(anchors))
	for _, a := range anchors {
		ids = append(ids, string(a))
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	fwdFilter := valueTraceFilter(ctxField, ctxField, false, "n_next", false)
	rows, err := r.Query(`WITH RECURSIVE
vt(id, dir, depth, kind) AS (
    SELECT n.id, 0, 0, n.kind FROM nodes n WHERE n.id IN (`+placeholders+`)
    UNION
    SELECT e.target_id, 1, d.depth + 1, n_next.kind FROM edges e INDEXED BY idx_edges_source
    JOIN vt d ON e.source_id = d.id
    JOIN nodes n_next ON e.target_id = n_next.id
    WHERE (d.dir = 1 OR d.depth = 0) AND d.depth < ? AND e.kind IN ('data_flows_to','argument','returns','phi_operand','summary_io')
      AND (n_next.kind != 'field_access' OR (`+fwdFilter+`))
)
SELECT dp.id, MIN(dp.depth), n.name,
       (SELECT COALESCE(GROUP_CONCAT(DISTINCT e2.kind), '') FROM edges e2
         WHERE ((dp.dir = 0 AND e2.target_id = dp.id) OR (dp.dir = 1 AND e2.source_id = dp.id))
           AND e2.kind IN ('data_flows_to','argument','returns','phi_operand','summary_io')),
       n.line_start, dp.dir, n.kind,
       json_extract(n.properties, '$.access_kind'), json_extract(n.properties, '$.func_id'),
       json_extract(n.properties, '$.full_path')
FROM vt dp JOIN nodes n ON n.id = dp.id
GROUP BY dp.id, dp.dir
ORDER BY MIN(dp.depth), dp.id`,
		append(anySlice(ids), maxDepth)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.TraceRow
	for rows.Next() {
		var (
			row      domain.TraceRow
			id       string
			line     sql.NullInt64
			kind     string
			access   sql.NullString
			funcID   sql.NullString
			fullPath sql.NullString
		)
		if err := rows.Scan(&id, &row.Depth, &row.Name, &row.EdgeKinds, &line, &row.Dir, &kind, &access, &funcID, &fullPath); err != nil {
			return nil, err
		}
		row.ID = domain.CanonicalID(id)
		row.EdgeKinds = sortEdgeKinds(row.EdgeKinds)
		row.Kind = domain.EntityKind(kind)
		if access.Valid {
			row.Access = access.String
		}
		if funcID.Valid {
			row.FuncID = funcID.String
		}
		if fullPath.Valid {
			row.FullPath = fullPath.String
		}
		if line.Valid {
			row.Line = int(line.Int64)
		}
		out = append(out, &row)
	}
	return out, rows.Err()
}
func anySlice(ids []string) []any {
	out := make([]any, len(ids))
	for i, s := range ids {
		out[i] = s
	}
	return out
}
