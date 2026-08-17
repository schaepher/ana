package sqlite

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/schaepher/codeintel/internal/domain"
	"go.uber.org/zap"
)

// GetCallers 返回调用 id（或更上层）的边，深度 ≤ depth，置信度 ≥ minConfidence。
// 递归 CTE 沿 source 方向向上遍历（TD.md ImpactAnalysisSpecification）。
func (r *Repo) GetCallers(id domain.CanonicalID, depth int, minConfidence float64) ([]*domain.Fact, error) {
	logger := zap.L()
	logger.Debug("enter (Repo).GetCallers")
	defer logger.Debug("exit (Repo).GetCallers")
	return r.walkEdges(string(id), depth, minConfidence, "callers")
}

// GetCallees 返回 id 调用（或更下层）的边，深度 ≤ depth，置信度 ≥ minConfidence。
func (r *Repo) GetCallees(id domain.CanonicalID, depth int, minConfidence float64) ([]*domain.Fact, error) {
	logger := zap.L()
	logger.Debug("enter (Repo).GetCallees")
	defer logger.Debug("exit (Repo).GetCallees")
	return r.walkEdges(string(id), depth, minConfidence, "callees")
}

// walkEdges 沿单向方向递归遍历 CALLS 边。
//
//	callers: edges 从 id 向上（e.target_id 为已到达节点）
//	callees: edges 从 id 向下（e.source_id 为已到达节点）
func (r *Repo) walkEdges(id string, depth int, minConfidence float64, dir string) ([]*domain.Fact, error) {
	logger := zap.L()
	logger.Debug("enter (Repo).walkEdges")
	defer logger.Debug("exit (Repo).walkEdges")
	var anchor, walkCol string
	if dir == "callers" {
		anchor, walkCol = "target_id", "src"
	} else {
		anchor, walkCol = "source_id", "tgt"
	}
	q := fmt.Sprintf(`
WITH RECURSIVE walk(src, tgt, kind, tool_source, confidence, metadata, d) AS (
    SELECT source_id, target_id, kind, tool_source, confidence, metadata, 1
    FROM edges WHERE %s = ? AND kind = 'calls' AND confidence >= ?
    UNION
    SELECT e.source_id, e.target_id, e.kind, e.tool_source, e.confidence, e.metadata, w.d + 1
    FROM edges e JOIN walk w ON e.%s = w.%s
    WHERE w.d < ? AND e.kind = 'calls' AND e.confidence >= ?
)
SELECT DISTINCT src, tgt, kind, tool_source, confidence, metadata FROM walk`,
		anchor, anchor, walkCol)

	rows, err := r.Query(q, id, minConfidence, depth, minConfidence)
	if err != nil {
		return nil, fmt.Errorf("walk %s of %s: %w", dir, id, err)
	}
	defer rows.Close()
	return scanFacts(rows)
}
func scanFacts(rows *sql.Rows) ([]*domain.Fact, error) {
	logger := zap.L()
	logger.Debug("enter scanFacts")
	defer logger.Debug("exit scanFacts")
	var out []*domain.Fact
	for rows.Next() {
		f := &domain.Fact{}
		var meta string
		if err := rows.Scan(&f.SourceID, &f.TargetID, &f.Kind, &f.ToolSource, &f.Confidence, &meta); err != nil {
			return nil, err
		}
		if meta != "" {
			var m map[string]any
			if err := json.Unmarshal([]byte(meta), &m); err == nil {
				f.Metadata = m
			}
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// GetImpact 计算变更影响范围：从 id 出发沿任意方向遍历，深度 ≤ depth（TD.md 决策 10）。
func (r *Repo) GetImpact(id domain.CanonicalID, depth int) ([]*domain.CodeEntity, error) {
	logger := zap.L()
	logger.Debug("enter (Repo).GetImpact")
	defer logger.Debug("exit (Repo).GetImpact")
	q := `
WITH RECURSIVE reach(id, d) AS (
    SELECT target_id, 1 FROM edges WHERE source_id = ?
    UNION
    SELECT source_id, 1 FROM edges WHERE target_id = ?
    UNION
    SELECT e.target_id, r.d + 1 FROM edges e JOIN reach r ON e.source_id = r.id WHERE r.d < ?
    UNION
    SELECT e.source_id, r.d + 1 FROM edges e JOIN reach r ON e.target_id = r.id WHERE r.d < ?
)
SELECT id FROM reach LIMIT 2000`

	rows, err := r.Query(q, string(id), string(id), depth, depth)
	if err != nil {
		return nil, fmt.Errorf("impact of %s: %w", id, err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var idStr string
		if err := rows.Scan(&idStr); err != nil {
			return nil, err
		}
		ids = append(ids, idStr)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i, v := range ids {
		args[i] = v
	}
	nodeRows, err := r.Query(
		"SELECT id, kind, name, file_path, line_start, line_end, properties FROM nodes WHERE id IN ("+placeholders+")",
		args...)
	if err != nil {
		return nil, err
	}
	defer nodeRows.Close()
	return scanNodes(nodeRows)
}

// GetRoots 返回顶层入口节点（前端初始视图）：
//   - main 入口函数（排除测试包生成的 main，其 id 形如 <pkg>.test:main）
//   - HTTP 服务入口（serves_http 标记）
//   - gRPC 服务入口（serves_grpc 标记）
//   - 框架回调 struct：方法未被当前 module 其他文件调用（由框架调用）
//
// 约束：入口必须落在当前 module 内的文件（file_path 非空、非 _test.go、
// 非仓库外路径）。
func (r *Repo) GetRoots() ([]*domain.CodeEntity, error) {
	logger := zap.L()
	logger.Debug("enter (Repo).GetRoots")
	defer logger.Debug("exit (Repo).GetRoots")
	rows, err := r.Query(`
SELECT id, kind, name, file_path, line_start, line_end, properties FROM nodes
WHERE file_path IS NOT NULL
  AND file_path NOT LIKE '%_test.go'
  AND file_path NOT LIKE '../%'
  AND ((name = 'main' AND kind = 'function' AND id NOT LIKE '%.test:main')
   OR json_extract(properties, '$.serves_http') = 'true'
   OR json_extract(properties, '$.serves_grpc') = 'true')
ORDER BY kind, name LIMIT 200`)
	if err != nil {
		return nil, fmt.Errorf("get roots: %w", err)
	}
	roots, err := scanNodes(rows)
	if err != nil {
		return nil, err
	}

	framework, err := r.GetFrameworkStructs()
	if err != nil {
		return nil, err
	}
	seen := map[domain.CanonicalID]bool{}
	for _, n := range roots {
		seen[n.ID] = true
	}
	for _, n := range framework {
		if !seen[n.ID] {
			roots = append(roots, n)
			seen[n.ID] = true
		}
	}
	return roots, nil
}
