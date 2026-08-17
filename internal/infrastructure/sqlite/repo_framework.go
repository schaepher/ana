package sqlite

import (
	"fmt"
	"strings"

	"github.com/schaepher/codeintel/internal/domain"
	"go.uber.org/zap"
)

// GetFrameworkStructs 返回"方法未被当前 module 其他文件调用"的 struct
// （无跨文件 caller → 推测由框架通过注册/回调机制调用），标记为顶层。
func (r *Repo) GetFrameworkStructs() ([]*domain.CodeEntity, error) {
	logger := zap.L()
	logger.Debug("enter (Repo).GetFrameworkStructs")
	defer logger.Debug("exit (Repo).GetFrameworkStructs")

	rows, err := r.Query(`
SELECT e.target_id, caller_n.file_path
FROM edges e
JOIN nodes caller_n ON caller_n.id = e.source_id
JOIN nodes method_n ON method_n.id = e.target_id
WHERE e.kind = 'calls'
  AND caller_n.file_path IS NOT NULL
  AND method_n.file_path IS NOT NULL
  AND caller_n.file_path != method_n.file_path`)
	if err != nil {
		return nil, fmt.Errorf("framework structs: %w", err)
	}
	defer rows.Close()

	calledBy := map[string]string{}
	for rows.Next() {
		var methodID, callerFile string
		if err := rows.Scan(&methodID, &callerFile); err != nil {
			return nil, err
		}
		if _, ok := calledBy[methodID]; !ok {
			calledBy[methodID] = callerFile
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	methodRows, err := r.Query(`SELECT id FROM nodes WHERE kind = 'method'`)
	if err != nil {
		return nil, fmt.Errorf("framework structs methods: %w", err)
	}
	methodsOf := map[string][]string{}
	for methodRows.Next() {
		var methodID string
		if err := methodRows.Scan(&methodID); err != nil {
			methodRows.Close()
			return nil, err
		}
		if sid, ok := structIDFromMethod(methodID); ok {
			methodsOf[sid] = append(methodsOf[sid], methodID)
		}
	}
	methodRows.Close()
	if err := methodRows.Err(); err != nil {
		return nil, err
	}

	nodeRows, err := r.Query(`
SELECT id, kind, name, file_path, line_start, line_end, properties FROM nodes
WHERE kind = 'struct'
ORDER BY name LIMIT 2000`)
	if err != nil {
		return nil, fmt.Errorf("framework structs query: %w", err)
	}
	defer nodeRows.Close()
	structs, err := scanNodes(nodeRows)
	if err != nil {
		return nil, err
	}

	var nodes []*domain.CodeEntity
	for _, st := range structs {
		short := shortStructID(st.ID)
		switch {
		case st.FilePath == "" || strings.HasSuffix(st.FilePath, "_test.go") || strings.HasPrefix(st.FilePath, "../"):
			logger.Info("struct 未加入顶层（文件不在 module 内）",
				zap.String("struct", short), zap.String("file", st.FilePath))
			continue
		case len(methodsOf[string(st.ID)]) == 0:
			logger.Info("struct 未加入顶层（纯字段无方法）",
				zap.String("struct", short), zap.String("file", st.FilePath))
			continue
		}

		called := false
		for _, mid := range methodsOf[string(st.ID)] {
			if callerFile, ok := calledBy[mid]; ok {
				called = true
				logger.Info("struct 未加入顶层（方法被其他文件调用）",
					zap.String("struct", short),
					zap.String("method", shortMethodID(mid)),
					zap.String("caller_file", callerFile))
			}
		}
		if called {
			continue
		}
		logger.Info("struct 加入顶层（框架回调：方法无跨文件调用）",
			zap.String("struct", short), zap.String("file", st.FilePath))
		if st.Properties == nil {
			st.Properties = map[string]any{}
		}
		st.Properties["framework"] = "true"
		nodes = append(nodes, st)
	}
	return nodes, nil
}

// Expand 返回节点的直接邻居（前端点击展开）：
//   - 双向的 calls / implements / imports 边（含方向）
//   - 邻居节点（去重）
//
// 上限 500 条边防止超大数据拖垮前端。
func (r *Repo) Expand(id domain.CanonicalID) (facts []*domain.Fact, nodes []*domain.CodeEntity, err error) {
	logger := zap.L()
	logger.Debug("enter (Repo).Expand")
	defer logger.Debug("exit (Repo).Expand")

	// Q178：参数/接收者节点（#param.<name>）本身承载数据边（data_flows_to/
	// argument/phi_operand 等），不再桥接到 ssa_value 参数副本（旧设计
	// #<name>，已废弃）；has_param 边也落库——直接按 id 查边即可
	queryID := string(id)
	cur, gerr := r.GetSymbol(id)
	var funcBridgeID string
	if gerr == nil && cur.Kind == domain.KindSSAValue {
		ok := cur.Property("origin_kind") == "param" || cur.Property("origin_kind") == "receiver"
		if ok && cur.Property("func_id") != "" {
			if _, gerr := r.GetSymbol(domain.CanonicalID(cur.Property("func_id"))); gerr == nil {
				funcBridgeID = cur.Property("func_id")
			}
		}
	}
	rows, err := r.Query(`
SELECT e.source_id, e.target_id, e.kind, e.tool_source, e.confidence, e.metadata
FROM edges e
LEFT JOIN nodes n ON n.id = CASE WHEN e.source_id = ? THEN e.target_id ELSE e.source_id END
WHERE (e.source_id = ? OR e.target_id = ?) AND e.kind IN ('calls', 'implements', 'imports', 'initializes', 'uses', 'passes_to', 'passes_result', 'of_type', 'has_method', 'has_param', 'has_result', 'data_flows_to', 'argument', 'returns', 'phi_operand', 'alias', 'dispatch_to')
ORDER BY CASE WHEN e.kind = 'has_param' THEN 0
              WHEN e.kind = 'has_result' THEN 1
              ELSE 2 END,
         COALESCE(CASE WHEN e.kind IN ('has_param','has_result')
                       THEN json_extract(n.properties, '$.index') END, 999),
         e.id
LIMIT 500`, queryID, queryID, queryID, queryID)
	if err != nil {
		return nil, nil, fmt.Errorf("expand %s: %w", id, err)
	}
	defer rows.Close()
	facts, err = scanFacts(rows)
	if err != nil {
		return nil, nil, err
	}

	if funcBridgeID != "" {
		facts = append(facts, &domain.Fact{
			SourceID:   domain.CanonicalID(funcBridgeID),
			TargetID:   id,
			Kind:       domain.FactHasParam,
			ToolSource: domain.ToolSSA,
			Confidence: 1.0,
		})
	}
	if len(facts) == 0 {
		return facts, nil, nil
	}

	neighborIDs := make([]string, 0, len(facts)*2)
	seen := map[string]bool{string(id): true}
	for _, f := range facts {
		for _, nid := range []string{string(f.SourceID), string(f.TargetID)} {
			if !seen[nid] {
				seen[nid] = true
				neighborIDs = append(neighborIDs, nid)
			}
		}
	}
	if len(neighborIDs) == 0 {
		return facts, nil, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(neighborIDs)), ",")
	args := make([]any, len(neighborIDs))
	for i, v := range neighborIDs {
		args[i] = v
	}
	nodeRows, err := r.Query(
		"SELECT id, kind, name, file_path, line_start, line_end, properties FROM nodes WHERE id IN ("+placeholders+")",
		args...)
	if err != nil {
		return nil, nil, err
	}
	defer nodeRows.Close()
	nodes, err = scanNodes(nodeRows)
	if err != nil {
		return nil, nil, err
	}
	return facts, nodes, nil
}
