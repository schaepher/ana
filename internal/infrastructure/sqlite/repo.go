package sqlite

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/schaepher/codeintel/internal/domain"
	"go.uber.org/zap"
)

// 确保 DB 实现仓储接口
var _ domain.CodeRepository = (*Repo)(nil)
var _ domain.BuildMetadataRepository = (*Repo)(nil)

// Repo 实现 CodeRepository / BuildMetadataRepository。
type Repo struct {
	*DB
}

// NewRepo 基于已打开的数据库创建仓储。
func NewRepo(db *DB) *Repo {
	logger := zap.L()
	logger.Debug("enter NewRepo")
	defer logger.Debug("exit NewRepo")
	return &Repo{DB: db}
}

const insertNodeSQL = `
INSERT INTO nodes (id, kind, name, file_path, line_start, line_end, properties)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
    properties = json_patch(COALESCE(properties, '{}'), excluded.properties)`

const insertEdgeSQL = `
INSERT INTO edges (source_id, target_id, kind, tool_source, confidence, metadata)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(source_id, target_id, kind) DO UPDATE SET
    confidence = excluded.confidence,
    tool_source = excluded.tool_source,
    metadata = excluded.metadata
WHERE excluded.confidence > edges.confidence`

// saveBatchResult 记录批次写入的统计信息。
type saveBatchResult struct {
	// SkippedEdges 因外键冲突（端点节点不存在）被跳过的边数
	SkippedEdges int
}

// SaveBatch 在单个事务中保存节点与边（节点必须先于边插入以满足外键）。
func (r *Repo) SaveBatch(nodes []*domain.CodeEntity, edges []*domain.Fact) error {
	logger := zap.L()
	logger.Debug("enter (Repo).SaveBatch")
	defer logger.Debug("exit (Repo).SaveBatch")
	_, err := r.SaveBatchStats(nodes, edges)
	return err
}

// SaveBatchStats 与 SaveBatch 相同，但返回批次统计（跳过的外键冲突边数）。
// 端点节点不存在的边（如 Git 追踪到 SCIP 未索引的文件）静默跳过，不中断构建。
func (r *Repo) SaveBatchStats(nodes []*domain.CodeEntity, edges []*domain.Fact) (*saveBatchResult, error) {
	logger := zap.L()
	logger.Debug("enter (Repo).SaveBatchStats")
	defer logger.Debug("exit (Repo).SaveBatchStats")
	result := &saveBatchResult{}
	if len(nodes) == 0 && len(edges) == 0 {
		return result, nil
	}
	tx, err := r.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	if len(nodes) > 0 {
		stmt, err := tx.Prepare(insertNodeSQL)
		if err != nil {
			return nil, fmt.Errorf("prepare node insert: %w", err)
		}
		for _, n := range nodes {
			props, err := marshalProps(n.Properties)
			if err != nil {
				stmt.Close()
				return nil, fmt.Errorf("marshal properties of %s: %w", n.ID, err)
			}
			if _, err := stmt.Exec(string(n.ID), string(n.Kind), n.Name, n.FilePath,
				n.LineStart, n.LineEnd, string(props)); err != nil {
				stmt.Close()
				return nil, fmt.Errorf("insert node %s: %w", n.ID, err)
			}
		}
		stmt.Close()
	}
	if len(edges) > 0 {
		stmt, err := tx.Prepare(insertEdgeSQL)
		if err != nil {
			return nil, fmt.Errorf("prepare edge insert: %w", err)
		}
		for _, e := range edges {
			meta, err := json.Marshal(e.Metadata)
			if err != nil {
				stmt.Close()
				return nil, fmt.Errorf("marshal metadata: %w", err)
			}
			if _, err := stmt.Exec(string(e.SourceID), string(e.TargetID), string(e.Kind),
				e.ToolSource, e.Confidence, string(meta)); err != nil {
				if isFKError(err) {
					// 端点节点不存在（如 Git 追踪到未索引文件），跳过该边
					result.SkippedEdges++
					continue
				}
				stmt.Close()
				return nil, fmt.Errorf("insert edge %s->%s (%s): %w", e.SourceID, e.TargetID, e.Kind, err)
			}
		}
		stmt.Close()
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

// marshalProps 序列化节点属性；nil 映射为空对象（json_patch 需要对象操作数）。
func marshalProps(props map[string]any) ([]byte, error) {
	logger := zap.L()
	logger.Debug("enter marshalProps")
	defer logger.Debug("exit marshalProps")
	if props == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(props)
}

// isFKError 判断是否为外键约束错误（SQLITE_CONSTRAINT_FOREIGNKEY = 787）。
func isFKError(err error) bool {
	logger := zap.L()
	logger.Debug("enter isFKError")
	defer logger.Debug("exit isFKError")
	var sqliteErr interface{ ErrorCode() int }
	if errors.As(err, &sqliteErr) {
		return sqliteErr.ErrorCode() == 787
	}
	return false
}

// SaveNode 保存单个节点（TD.md 4.2 接口）。
func (r *Repo) SaveNode(node *domain.CodeEntity) error {
	logger := zap.L()
	logger.Debug("enter (Repo).SaveNode")
	defer logger.Debug("exit (Repo).SaveNode")
	return r.SaveBatch([]*domain.CodeEntity{node}, nil)
}

// SaveEdges 保存边列表（TD.md 4.2 接口）。
func (r *Repo) SaveEdges(edges []*domain.Fact) error {
	logger := zap.L()
	logger.Debug("enter (Repo).SaveEdges")
	defer logger.Debug("exit (Repo).SaveEdges")
	return r.SaveBatch(nil, edges)
}

// DeleteByFile 删除某个文件的所有节点及其边（级联），用于增量构建。
func (r *Repo) DeleteByFile(filePath string) error {
	logger := zap.L()
	logger.Debug("enter (Repo).DeleteByFile")
	defer logger.Debug("exit (Repo).DeleteByFile")
	_, err := r.Exec("DELETE FROM nodes WHERE file_path = ?", filePath)
	if err != nil {
		return fmt.Errorf("delete nodes of file %s: %w", filePath, err)
	}
	return nil
}

// GetSymbol 按 Canonical ID 查询符号。
func (r *Repo) GetSymbol(id domain.CanonicalID) (*domain.CodeEntity, error) {
	logger := zap.L()
	logger.Debug("enter (Repo).GetSymbol")
	defer logger.Debug("exit (Repo).GetSymbol")
	return scanNode(r.QueryRow(
		"SELECT id, kind, name, file_path, line_start, line_end, properties FROM nodes WHERE id = ?",
		string(id)))
}

// GetSymbolByName 按名称查找：先精确匹配，无结果时退化为模糊匹配
// （CLI 按名查找用）。
func (r *Repo) GetSymbolByName(name string) ([]*domain.CodeEntity, error) {
	logger := zap.L()
	logger.Debug("enter (Repo).GetSymbolByName")
	defer logger.Debug("exit (Repo).GetSymbolByName")
	// 精确匹配
	rows, err := r.Query(
		"SELECT id, kind, name, file_path, line_start, line_end, properties FROM nodes WHERE name = ? ORDER BY name LIMIT 50",
		name)
	if err != nil {
		return nil, err
	}
	nodes, err := scanNodes(rows)
	if err != nil || len(nodes) > 0 {
		return nodes, err
	}
	// 模糊匹配（名称或 canonical ID 包含）
	rows, err = r.Query(
		"SELECT id, kind, name, file_path, line_start, line_end, properties FROM nodes WHERE name LIKE ? OR id LIKE ? ORDER BY name LIMIT 50",
		"%"+name+"%", "%"+name+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanNodes(rows)
}

func scanNode(row *sql.Row) (*domain.CodeEntity, error) {
	logger := zap.L()
	logger.Debug("enter scanNode")
	defer logger.Debug("exit scanNode")
	n := &domain.CodeEntity{}
	var props string
	err := row.Scan(&n.ID, &n.Kind, &n.Name, &n.FilePath, &n.LineStart, &n.LineEnd, &props)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if props != "" {
		var m map[string]any
		if err := json.Unmarshal([]byte(props), &m); err == nil {
			n.Properties = m
		}
	}
	return n, nil
}

func scanNodes(rows *sql.Rows) ([]*domain.CodeEntity, error) {
	logger := zap.L()
	logger.Debug("enter scanNodes")
	defer logger.Debug("exit scanNodes")
	var out []*domain.CodeEntity
	for rows.Next() {
		n := &domain.CodeEntity{}
		var props string
		if err := rows.Scan(&n.ID, &n.Kind, &n.Name, &n.FilePath, &n.LineStart, &n.LineEnd, &props); err != nil {
			return nil, err
		}
		if props != "" {
			var m map[string]any
			if err := json.Unmarshal([]byte(props), &m); err == nil {
				n.Properties = m
			}
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

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
	var anchor, other, walkCol string
	if dir == "callers" {
		anchor, other, walkCol = "target_id", "source_id", "src"
	} else {
		anchor, other, walkCol = "source_id", "target_id", "tgt"
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
		anchor, other, walkCol)

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
	// 框架回调 struct：方法未被其他文件调用（无跨文件 CALLS 入边）
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

// GetFrameworkStructs 返回"方法未被当前 module 其他文件调用"的 struct
// （无跨文件 caller → 推测由框架通过注册/回调机制调用），标记为顶层。
func (r *Repo) GetFrameworkStructs() ([]*domain.CodeEntity, error) {
	logger := zap.L()
	logger.Debug("enter (Repo).GetFrameworkStructs")
	defer logger.Debug("exit (Repo).GetFrameworkStructs")
	// 被"其他文件"调用过的方法 → caller 所在文件（跨文件 CALLS 边，用于日志）
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

	calledBy := map[string]string{} // methodID → 跨文件 caller 文件
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

	// 收集"带方法"的 struct：structID → 方法 ID 列表
	methodRows, err := r.Query(`SELECT id FROM nodes WHERE kind = 'method'`)
	if err != nil {
		return nil, fmt.Errorf("framework structs methods: %w", err)
	}
	methodsOf := map[string][]string{} // structID → [methodID]
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

	// 遍历所有 struct，逐个判定并打印原因
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
		// 检查方法是否被跨文件调用
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

// shortStructID 压缩 struct ID 便于日志（保留 pkg 末段与类型名）。
func shortStructID(id domain.CanonicalID) string {
	s := strings.TrimPrefix(string(id), "symbol:go:")
	if i := strings.LastIndex(s, ":"); i >= 0 {
		if j := strings.LastIndex(s[:i], "/"); j >= 0 {
			return s[j+1:]
		}
		return s
	}
	return s
}

// shortMethodID 压缩方法 ID 便于日志（保留类型名与方法名）。
func shortMethodID(id string) string {
	s := strings.TrimPrefix(id, "symbol:go:")
	if i := strings.Index(s, ":("); i >= 0 {
		return s[i+1:]
	}
	return s
}

// structIDFromMethod 将方法 ID（symbol:go:<pkg>:(T).M）还原为所属 struct ID
// （symbol:go:<pkg>:T）。
func structIDFromMethod(methodID string) (string, bool) {
	s := strings.TrimPrefix(methodID, "symbol:go:")
	i := strings.Index(s, ":(")
	if i < 0 {
		return "", false
	}
	rest := s[i+2:]
	j := strings.Index(rest, ").")
	if j < 0 {
		return "", false
	}
	return "symbol:go:" + s[:i] + ":" + rest[:j], true
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
	rows, err := r.Query(`
SELECT source_id, target_id, kind, tool_source, confidence, metadata
FROM edges
WHERE (source_id = ? OR target_id = ?) AND kind IN ('calls', 'implements', 'imports', 'initializes', 'uses', 'passes_to', 'passes_result', 'of_type', 'has_method')
LIMIT 500`, string(id), string(id))
	if err != nil {
		return nil, nil, fmt.Errorf("expand %s: %w", id, err)
	}
	defer rows.Close()
	facts, err = scanFacts(rows)
	if err != nil {
		return nil, nil, err
	}
	if len(facts) == 0 {
		return facts, nil, nil
	}

	// 收集邻居节点 id（去重，不含自身）
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

// GetDataFlows 返回符号的数据流信息：
//   - 方法内路径（properties.data_flows，来自 Joern REACHING_DEF 聚合）
//   - 跨方法 DATA_FLOWS_TO 边
func (r *Repo) GetDataFlows(id domain.CanonicalID) (flows []string, facts []*domain.Fact, err error) {
	n, err := r.GetSymbol(id)
	if err != nil {
		return nil, nil, err
	}
	if arr, ok := n.Properties["data_flows"].([]any); ok {
		for _, v := range arr {
			if s, ok := v.(string); ok {
				flows = append(flows, s)
			}
		}
	}
	rows, err := r.Query(`
SELECT source_id, target_id, kind, tool_source, confidence, metadata
FROM edges WHERE (source_id = ? OR target_id = ?) AND kind = 'data_flows_to' LIMIT 200`,
		string(id), string(id))
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	facts, err = scanFacts(rows)
	return flows, facts, err
}

// Counts 返回节点数与边数（构建报告用）。
func (r *Repo) Counts() (nodes, edges int, err error) {
	logger := zap.L()
	logger.Debug("enter (Repo).Counts")
	defer logger.Debug("exit (Repo).Counts")
	if err = r.QueryRow("SELECT COUNT(*) FROM nodes").Scan(&nodes); err != nil {
		return 0, 0, err
	}
	if err = r.QueryRow("SELECT COUNT(*) FROM edges").Scan(&edges); err != nil {
		return 0, 0, err
	}
	return nodes, edges, nil
}

// Save 保存构建元数据。
func (r *Repo) Save(meta *domain.BuildMeta) error {
	logger := zap.L()
	logger.Debug("enter (Repo).Save")
	defer logger.Debug("exit (Repo).Save")
	_, err := r.Exec(`INSERT OR REPLACE INTO build_metadata
		(build_id, commit_sha, tool_name, status, duration_ms, error_message)
		VALUES (?, ?, ?, ?, ?, ?)`,
		meta.BuildID, meta.CommitSHA, meta.ToolName, meta.Status, meta.DurationMs, meta.ErrorMsg)
	return err
}

// GetLatest 获取最近一次构建元数据。
func (r *Repo) GetLatest() (*domain.BuildMeta, error) {
	logger := zap.L()
	logger.Debug("enter (Repo).GetLatest")
	defer logger.Debug("exit (Repo).GetLatest")
	m := &domain.BuildMeta{}
	err := r.QueryRow(`SELECT build_id, commit_sha, tool_name, status, duration_ms, error_message
		FROM build_metadata ORDER BY timestamp DESC LIMIT 1`).
		Scan(&m.BuildID, &m.CommitSHA, &m.ToolName, &m.Status, &m.DurationMs, &m.ErrorMsg)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return m, nil
}
