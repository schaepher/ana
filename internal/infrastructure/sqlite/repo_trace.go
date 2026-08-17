package sqlite

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/schaepher/codeintel/internal/domain"
	"go.uber.org/zap"
)

// TraceBackward 反向追溯字段产生点（S2，field_trace.md §6.3）：
// 起点为入口函数内匹配 full_path 的 field_access 节点，沿
// data_flows_to / argument / returns / alias / phi_operand 反向遍历。
func (r *Repo) TraceBackward(field string, funcID domain.CanonicalID, maxDepth int) ([]*domain.TraceRow, error) {
	logger := zap.L()
	logger.Debug("enter (Repo).TraceBackward")
	defer logger.Debug("exit (Repo).TraceBackward")
	return r.trace(field, funcID, maxDepth, false)
}

// TraceBackwardIndirect 反向追溯 + 跨函数间接写（Q172 --follow-indirect）：
// 起点函数对目标字段只有 function_field_summary.indirect_write（无本函数
// 真实 field_access）时，沿 summary_origins 递归解析调用链（outer → inner
// → ... → 真实写者），收集链上全部函数的 field_access 写节点作起点，
// 再执行反向 data_flows_to 遍历（赋值来源）。
func (r *Repo) TraceBackwardIndirect(field string, funcID domain.CanonicalID, maxDepth int) ([]*domain.TraceRow, error) {
	logger := zap.L()
	logger.Debug("enter (Repo).TraceBackwardIndirect")
	defer logger.Debug("exit (Repo).TraceBackwardIndirect")
	if maxDepth <= 0 {
		maxDepth = 8
	}

	chain := map[domain.CanonicalID]bool{funcID: true}
	frontier := []domain.CanonicalID{funcID}
	for len(frontier) > 0 && len(chain) < maxDepth*4 {
		cur := frontier[0]
		frontier = frontier[1:]
		rows, err := r.Query(`SELECT DISTINCT callee_id FROM summary_origins
			WHERE function_id = ? AND access_kind = 'indirect_write' AND field_path = ?`,
			string(cur), field)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var cid sql.NullString
			if err := rows.Scan(&cid); err != nil {
				rows.Close()
				return nil, err
			}
			if !cid.Valid {
				continue
			}
			id := domain.CanonicalID(cid.String)
			if !chain[id] {
				chain[id] = true
				frontier = append(frontier, id)
			}
		}
		rows.Close()
	}

	ids := make([]any, 0, len(chain))
	for id := range chain {
		ids = append(ids, string(id))
	}
	if len(ids) == 0 {
		return nil, nil
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")

	q := fmt.Sprintf(`WITH RECURSIVE back(id, depth, name, edge_kinds, line, kind, access, func_id, full_path) AS (
    SELECT n.id, 0, n.name, '', n.line_start, n.kind,
           json_extract(n.properties, '$.access_kind'),
           json_extract(n.properties, '$.func_id'),
           json_extract(n.properties, '$.full_path')
    FROM nodes n
    WHERE n.kind = 'field_access'
      AND json_extract(n.properties, '$.full_path') = ?
      AND json_extract(n.properties, '$.access_kind') = 'write'
      AND json_extract(n.properties, '$.func_id') IN (%s)
    UNION
    SELECT e.source_id, d.depth + 1, n_prev.name,
           CASE WHEN d.edge_kinds = '' THEN e.kind ELSE d.edge_kinds || ',' || e.kind END,
           n_prev.line_start, n_prev.kind,
           json_extract(n_prev.properties, '$.access_kind'),
           json_extract(n_prev.properties, '$.func_id'),
           json_extract(n_prev.properties, '$.full_path')
    FROM edges e
    JOIN back d ON e.target_id = d.id
    JOIN nodes n_prev ON e.source_id = n_prev.id
    WHERE e.kind = 'data_flows_to' AND d.depth < ?
)
SELECT id, depth, name, edge_kinds, line, kind, access, func_id, full_path
FROM back ORDER BY depth, id`, ph)
	args := append([]any{field}, ids...)
	args = append(args, maxDepth)
	rows, err := r.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.TraceRow
	for rows.Next() {
		var (
			row      domain.TraceRow
			id, kind string
			line     sql.NullInt64
			access   sql.NullString
			funcID   sql.NullString
			fullPath sql.NullString
		)
		if err := rows.Scan(&id, &row.Depth, &row.Name, &row.EdgeKinds, &line, &kind, &access, &funcID, &fullPath); err != nil {
			return nil, err
		}
		row.ID = domain.CanonicalID(id)
		row.Kind = domain.EntityKind(kind)
		row.EdgeKinds = sortEdgeKinds(row.EdgeKinds)
		if line.Valid {
			row.Line = int(line.Int64)
		}
		if access.Valid {
			row.Access = access.String
		}
		if funcID.Valid {
			row.FuncID = funcID.String
		}
		if fullPath.Valid {
			row.FullPath = fullPath.String
		}
		out = append(out, &row)
	}
	return out, rows.Err()
}

// TraceForward 正向追踪字段后续使用（S3，field_trace.md §6.4）：
// 起点同 S2，沿 data_flows_to / argument / returns / phi_operand / alias
// 正向遍历（跨函数经 argument/returns 边，不沿函数级 calls 边跳跃）；
// 遇到匹配 full_path 的 field_access 标记为使用点。
func (r *Repo) TraceForward(field string, funcID domain.CanonicalID, maxDepth int) ([]*domain.TraceRow, error) {
	logger := zap.L()
	logger.Debug("enter (Repo).TraceForward")
	defer logger.Debug("exit (Repo).TraceForward")
	return r.trace(field, funcID, maxDepth, true)
}

// trace 递归 CTE 实现 S2/S3；UNION 去重 + 深度限制防环（Q49）。
func (r *Repo) trace(field string, funcID domain.CanonicalID, maxDepth int, forward bool) ([]*domain.TraceRow, error) {
	logger := zap.L()
	logger.Debug("enter (Repo).trace")
	defer logger.Debug("exit (Repo).trace")
	if maxDepth <= 0 {
		maxDepth = 8
	}
	var query string
	if !forward {
		query = `WITH RECURSIVE def_trace(id, depth, name, edge_kinds, line) AS (
    SELECT n.id, 0, n.name, '', n.line_start
    FROM nodes n
    WHERE n.kind = 'field_access'
      AND json_extract(n.properties, '$.full_path') = ?
      AND json_extract(n.properties, '$.func_id') = ?
    UNION
    SELECT e.source_id, d.depth + 1, n_prev.name,
           CASE WHEN d.edge_kinds = '' THEN e.kind
                 ELSE d.edge_kinds || ',' || e.kind END, n_prev.line_start
    FROM edges e
    JOIN def_trace d ON e.target_id = d.id
    JOIN nodes n_prev ON e.source_id = n_prev.id
    WHERE e.kind IN ('data_flows_to','argument','returns','alias','phi_operand')
      AND d.depth < ?
)
SELECT id, depth, name, edge_kinds, line FROM def_trace ORDER BY depth, id`
	} else {
		query = `WITH RECURSIVE fwd_trace(id, depth, name, edge_kinds, line, is_usage, kind) AS (
    SELECT n.id, 0, n.name, '', n.line_start, 1, n.kind
    FROM nodes n
    WHERE n.kind = 'field_access'
      AND json_extract(n.properties, '$.full_path') = ?
      AND json_extract(n.properties, '$.func_id') = ?
    UNION
    -- 起点：与目标字段所属结构体同类型的 SSA 值（param/receiver/alloc/
    -- local/phi/global——① 传参、⑩ 局部对象、⑭ DAO 返回对象→局部变量→
    -- helper、global 溯源）。B2：类型不匹配的参数与全局变量（gitCommit
    -- 等 string）不再是起点——此前 origin_kind IN ('param','receiver',
    -- 'alloc','global') 无条件放行，全部参数与全局变量入链（噪音）
    SELECT n.id, 0, n.name, '', n.line_start, 0, n.kind
    FROM nodes n
    WHERE n.kind = 'ssa_value'
      AND (json_extract(n.properties, '$.func_id') = ?
           OR json_extract(n.properties, '$.origin_kind') = 'global')
      AND (json_extract(n.properties, '$.type_string') = ?
           OR json_extract(n.properties, '$.type_string') = ?)
    UNION
    -- 起点：承载目标类型对象的字段访问节点（C 形态：s.cfg → fill 写
    -- c.Key——容器对象 Svc 自身类型不匹配，但其字段节点 s.cfg 类型
    -- *Cfg 匹配，作起点经 argument 进入 callee）
    SELECT n.id, 0, n.name, '', n.line_start, 0, n.kind
    FROM nodes n
    WHERE n.kind = 'field_access'
      AND json_extract(n.properties, '$.func_id') = ?
      AND (json_extract(n.properties, '$.type_string') = ?
           OR json_extract(n.properties, '$.type_string') = ?)
    UNION
    SELECT e.target_id, d.depth + 1, n_next.name,
           CASE WHEN d.edge_kinds = '' THEN e.kind
                 ELSE d.edge_kinds || ',' || e.kind END, n_next.line_start,
           CASE WHEN n_next.kind = 'field_access'
                     AND json_extract(n_next.properties, '$.full_path') = ? THEN 1 ELSE 0 END,
           n_next.kind
    FROM edges e
    JOIN fwd_trace d ON e.source_id = d.id
    JOIN nodes n_next ON e.target_id = n_next.id
    WHERE e.kind IN ('data_flows_to','argument','returns','phi_operand','alias')
      -- 字段访问步：目标字段任意读写放行；参数/值 → 其他字段读 放行
      -- 但仅限与目标字段同名的读（⑫ 跳板精确化：dest.ID = src.ID 拷贝链
      -- 同名保留——kg.ID → t42.ID；record.Metadata/Status 等无关字段
      -- 读拦截——噪音与超时来源）；字段访问 → 字段访问 仅限目标字段
      AND (n_next.kind != 'field_access'
           OR json_extract(n_next.properties, '$.full_path') = ?
           OR (d.kind != 'field_access' AND json_extract(n_next.properties, '$.access_kind') = 'read'
               AND (json_extract(n_next.properties, '$.full_path') LIKE ?
                    OR json_extract(n_next.properties, '$.type_string') LIKE ?)))
      AND d.depth < ?
)
SELECT id, depth, name, edge_kinds, line, is_usage FROM fwd_trace ORDER BY depth, id`
	}
	var (
		rows *sql.Rows
		err  error
	)
	if forward {
		targetType := strings.TrimSuffix(field, "."+lastSeg(field))
		rows, err = r.Query(query, field, string(funcID), string(funcID),
			targetType, "*"+targetType,
			string(funcID), targetType, "*"+targetType,
			field, field,
			"%."+lastSeg(field), "%"+pkgOf(field)+".%", maxDepth)
	} else {
		rows, err = r.Query(query, field, string(funcID), maxDepth)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.TraceRow
	for rows.Next() {
		var (
			row  domain.TraceRow
			id   string
			line sql.NullInt64
		)
		if forward {
			var usage int
			if err := rows.Scan(&id, &row.Depth, &row.Name, &row.EdgeKinds, &line, &usage); err != nil {
				return nil, err
			}
			row.IsUsage = usage == 1
		} else {
			if err := rows.Scan(&id, &row.Depth, &row.Name, &row.EdgeKinds, &line); err != nil {
				return nil, err
			}
		}
		row.ID = domain.CanonicalID(id)
		if line.Valid {
			row.Line = int(line.Int64)
		}
		out = append(out, &row)
	}
	return out, rows.Err()
}

// lastSeg 取类型限定字段路径的字段名（example.com/m.T.FinalFee → FinalFee）。
func lastSeg(fullPath string) string {
	if i := strings.LastIndex(fullPath, "."); i >= 0 {
		return fullPath[i+1:]
	}
	return fullPath
}

// pkgOf 取类型限定字段路径的模块包路径（example.com/m.T.FinalFee → example.com/m）。
func pkgOf(fullPath string) string {
	parts := strings.Split(fullPath, ".")
	if len(parts) <= 2 {
		return fullPath
	}
	return strings.Join(parts[:len(parts)-2], ".")
}
