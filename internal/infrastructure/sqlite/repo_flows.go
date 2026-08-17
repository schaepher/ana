package sqlite

import (
	"database/sql"

	"github.com/schaepher/codeintel/internal/domain"
	"go.uber.org/zap"
)

// AllSummaries 返回全部函数字段摘要行（S4 导出用，field_trace.md §2），
// 按 field_path, access_kind 排序。
func (r *Repo) AllSummaries() ([]*domain.FunctionFieldSummary, error) {
	logger := zap.L()
	logger.Debug("enter (Repo).AllSummaries")
	defer logger.Debug("exit (Repo).AllSummaries")
	rows, err := r.Query(`SELECT function_id, access_kind, field_path, instance_path, line_start, code_snippet
		FROM function_field_summary ORDER BY field_path, access_kind`)
	if err != nil {
		return nil, err
	}
	return scanSummaries(rows)
}

// GetFunctionFields 按函数查询字段摘要，附带 Q161 origins（间接写
// 多来源：调用点 × 被调函数，origin/confidence 由 action 层 join
// dispatch_to 边填充）。
func (r *Repo) GetFunctionFields(funcID domain.CanonicalID) ([]*domain.FunctionFieldSummary, error) {
	logger := zap.L()
	logger.Debug("enter (Repo).GetFunctionFields")
	defer logger.Debug("exit (Repo).GetFunctionFields")
	rows, err := r.Query(`SELECT function_id, access_kind, field_path, instance_path, line_start, code_snippet
		FROM function_field_summary WHERE function_id = ?
		ORDER BY access_kind, field_path`, string(funcID))
	if err != nil {
		return nil, err
	}
	out, err := scanSummaries(rows)
	if err != nil {
		return nil, err
	}

	orows, err := r.Query(`SELECT function_id, access_kind, field_path, call_line, callee_id
		FROM summary_origins WHERE function_id = ? ORDER BY call_line`, string(funcID))
	if err != nil {
		return nil, err
	}
	defer orows.Close()
	origins := map[string][]*domain.SummaryOrigin{}
	for orows.Next() {
		var (
			o     domain.SummaryOrigin
			fid   string
			cline sql.NullInt64
			cid   sql.NullString
		)
		if err := orows.Scan(&fid, &o.AccessKind, &o.FieldPath, &cline, &cid); err != nil {
			return nil, err
		}
		o.FunctionID = domain.CanonicalID(fid)
		if cline.Valid {
			o.CallLine = int(cline.Int64)
		}
		if cid.Valid {
			o.CalleeID = domain.CanonicalID(cid.String)
		}
		key := o.AccessKind + "|" + o.FieldPath
		origins[key] = append(origins[key], &o)
	}
	if len(origins) > 0 {
		for _, s := range out {
			key := s.AccessKind + "|" + s.FieldPath
			if os, ok := origins[key]; ok {
				s.Origins = os
			}
		}
	}
	return out, nil
}

// scanSummaries 扫描摘要查询行（GetFunctionFields/AllSummaries 共用）。
func scanSummaries(rows *sql.Rows) ([]*domain.FunctionFieldSummary, error) {
	defer rows.Close()
	var out []*domain.FunctionFieldSummary
	for rows.Next() {
		var (
			s   domain.FunctionFieldSummary
			fid string
		)
		if err := rows.Scan(&fid, &s.AccessKind, &s.FieldPath, &s.InstancePath, &s.LineStart, &s.CodeSnippet); err != nil {
			return nil, err
		}
		s.FunctionID = domain.CanonicalID(fid)
		out = append(out, &s)
	}
	return out, rows.Err()
}

// GetFunctionFlows 返回函数内完整字段数据流（前端 /api/flows 用）：
// 起点 = 函数内全部 field_access 节点，双向遍历 data_flows_to / phi_operand
// （func_id 限定在函数内，到参数/返回边界即止）；Dir=0 为产生链（反向），
// Dir=1 为使用链（正向）。
func (r *Repo) GetFunctionFlows(funcID domain.CanonicalID, maxDepth int) ([]*domain.TraceRow, error) {
	logger := zap.L()
	logger.Debug("enter (Repo).GetFunctionFlows")
	defer logger.Debug("exit (Repo).GetFunctionFlows")
	if maxDepth <= 0 {
		maxDepth = 8
	}
	rows, err := r.Query(`WITH RECURSIVE flows(id, depth, name, edge_kinds, line, dir, kind, access, func_id, full_path, ctx) AS (
    SELECT n.id, 0, n.name, '', n.line_start, 0, n.kind,
           json_extract(n.properties, '$.access_kind'),
           json_extract(n.properties, '$.func_id'),
           json_extract(n.properties, '$.full_path'),
           json_extract(n.properties, '$.full_path')
    FROM nodes n
    WHERE n.kind = 'field_access'
      AND json_extract(n.properties, '$.func_id') = ?
    UNION
    -- 反向：流向当前节点（产生链）；字段访问步限定起始字段（⑥：
    -- 共享中间值节点不把其他字段的访问带入本字段链）
    SELECT e.source_id, d.depth + 1, n_prev.name,
           CASE WHEN d.edge_kinds = '' THEN e.kind
                ELSE d.edge_kinds || ',' || e.kind END, n_prev.line_start, 0,
           n_prev.kind, json_extract(n_prev.properties, '$.access_kind'),
           json_extract(n_prev.properties, '$.func_id'),
           json_extract(n_prev.properties, '$.full_path'),
           d.ctx
    FROM edges e
    JOIN flows d ON e.target_id = d.id
    JOIN nodes n_prev ON e.source_id = n_prev.id
    WHERE e.kind IN ('data_flows_to','phi_operand')
      AND (d.dir = 0 OR d.depth = 0) AND d.depth < ?
      AND json_extract(n_prev.properties, '$.func_id') = ?
      AND (n_prev.kind != 'field_access'
           OR json_extract(n_prev.properties, '$.full_path') = d.ctx)
    UNION
    -- 正向：从当前节点流出（使用链）
    SELECT e.target_id, d.depth + 1, n_next.name,
           CASE WHEN d.edge_kinds = '' THEN e.kind
                ELSE d.edge_kinds || ',' || e.kind END, n_next.line_start, 1,
           n_next.kind, json_extract(n_next.properties, '$.access_kind'),
           json_extract(n_next.properties, '$.func_id'),
           json_extract(n_next.properties, '$.full_path'),
           d.ctx
    FROM edges e
    JOIN flows d ON e.source_id = d.id
    JOIN nodes n_next ON e.target_id = n_next.id
    WHERE e.kind IN ('data_flows_to','phi_operand')
      AND (d.dir = 1 OR d.depth = 0) AND d.depth < ?
      AND json_extract(n_next.properties, '$.func_id') = ?
      AND (n_next.kind != 'field_access'
           OR json_extract(n_next.properties, '$.full_path') = d.ctx)
)
SELECT id, depth, name, edge_kinds, line, dir, kind, access, func_id, full_path FROM flows ORDER BY dir, depth, id`,
		string(funcID), maxDepth, string(funcID), maxDepth, string(funcID))
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
		)
		if err := rows.Scan(&id, &row.Depth, &row.Name, &row.EdgeKinds, &line, &dir, &kind, &access, &funcID, &fullPath); err != nil {
			return nil, err
		}
		row.ID = domain.CanonicalID(id)
		row.Dir = dir
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
