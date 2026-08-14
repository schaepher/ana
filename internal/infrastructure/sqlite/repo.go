package sqlite

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/mattn/go-sqlite3"
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

const insertSummarySQL = `
INSERT OR IGNORE INTO function_field_summary
    (function_id, access_kind, field_path, instance_path, line_start, code_snippet)
VALUES (?, ?, ?, ?, ?, ?)`

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
	_, err := r.SaveBatchStats(nodes, edges, nil)
	return err
}

// SaveBatchStats 与 SaveBatch 相同，但返回批次统计（跳过的外键冲突边数），
// 并接受函数字段摘要行（function_field_summary）。
// 端点节点不存在的边（如 Git 追踪到 SCIP 未索引的文件）静默跳过，不中断构建。
func (r *Repo) SaveBatchStats(nodes []*domain.CodeEntity, edges []*domain.Fact,
	summaries []*domain.FunctionFieldSummary) (*saveBatchResult, error) {
	logger := zap.L()
	logger.Debug("enter (Repo).SaveBatchStats")
	defer logger.Debug("exit (Repo).SaveBatchStats")
	result := &saveBatchResult{}
	if len(nodes) == 0 && len(edges) == 0 && len(summaries) == 0 {
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
	if len(summaries) > 0 {
		stmt, err := tx.Prepare(insertSummarySQL)
		if err != nil {
			return nil, fmt.Errorf("prepare summary insert: %w", err)
		}
		for _, s := range summaries {
			// 端点函数节点不存在（如构建顺序导致）时跳过；UNIQUE 冲突（重复行）忽略
			if _, err := stmt.Exec(string(s.FunctionID), s.AccessKind, s.FieldPath,
				s.InstancePath, s.LineStart, s.CodeSnippet); err != nil {
				if isFKError(err) {
					result.SkippedEdges++
					continue
				}
				stmt.Close()
				return nil, fmt.Errorf("insert summary %s %s %s: %w", s.FunctionID, s.AccessKind, s.FieldPath, err)
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
// go-sqlite3 的 sqlite3.Error 用 ExtendedCode 存扩展错误码。
func isFKError(err error) bool {
	logger := zap.L()
	logger.Debug("enter isFKError")
	defer logger.Debug("exit isFKError")
	var e sqlite3.Error
	if errors.As(err, &e) {
		return e.ExtendedCode == 787
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
	// 排除字段追溯的内部节点（field_access / ssa_value / external_summary）：
	// 它们是字段访问点与 SSA 临时值，不是可搜索的代码符号（field_trace.md §4）
	const exclude = "kind NOT IN ('field_access','ssa_value','external_summary')"
	// 精确匹配
	rows, err := r.Query(
		"SELECT id, kind, name, file_path, line_start, line_end, properties FROM nodes WHERE name = ? AND "+exclude+" ORDER BY name LIMIT 50",
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
		"SELECT id, kind, name, file_path, line_start, line_end, properties FROM nodes WHERE (name LIKE ? OR id LIKE ?) AND "+exclude+" ORDER BY name LIMIT 50",
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
	// parameter/receiver 节点：代理到对应的 ssa_value 参数（数据流端点），
	// 使展开能返回该参数的数据流上下游（field_trace.md 参数展开）
	queryID := string(id)
	var bridgeID string // 桥边：parameter → ssa_value（不落库，仅响应）
	cur, gerr := r.GetSymbol(id)
	if gerr == nil && (cur.Kind == domain.KindParameter || cur.Kind == domain.KindReceiver) {
		queryID = paramValueID(string(id))
		if queryID != "" {
			if _, gerr := r.GetSymbol(domain.CanonicalID(queryID)); gerr == nil {
				bridgeID = queryID // ssa_value 参数节点存在才搭桥
			}
		}
	}
	// ssa_value 参数（param/receiver）：附加所属函数桥边（函数 → 参数值，
	// has_param，不落库）——链上参数可展开到其所属函数
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
	// 参数节点桥边：parameter → ssa_value（数据流链从参数声明到函数内值）
	if bridgeID != "" {
		facts = append(facts, &domain.Fact{
			SourceID:   id,
			TargetID:   domain.CanonicalID(bridgeID),
			Kind:       domain.FactDataFlowsTo,
			ToolSource: domain.ToolSSA,
			Confidence: 1.0,
		})
	}
	// ssa_value 参数桥边：所属函数 → 参数值（has_param），
	// 展开参数节点可回到所属函数继续探索
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
	// timestamp 为秒级：同一秒内多次构建须按写入顺序取最新（rowid 递增）
	err := r.QueryRow(`SELECT build_id, commit_sha, tool_name, status, duration_ms, error_message
		FROM build_metadata ORDER BY timestamp DESC, rowid DESC LIMIT 1`).
		Scan(&m.BuildID, &m.CommitSHA, &m.ToolName, &m.Status, &m.DurationMs, &m.ErrorMsg)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return m, nil
}

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

// GetFunctionFields 查询函数的字段读写摘要（S1，field_trace.md §6.2）。
// 直接查构建期预计算的 function_field_summary 表，无需动态遍历调用图。
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
	return scanSummaries(rows)
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

// TraceBackward 反向追溯字段产生点（S2，field_trace.md §6.3）：
// 起点为入口函数内匹配 full_path 的 field_access 节点，沿
// data_flows_to / argument / returns / alias / phi_operand 反向遍历。
func (r *Repo) TraceBackward(field string, funcID domain.CanonicalID, maxDepth int) ([]*domain.TraceRow, error) {
	logger := zap.L()
	logger.Debug("enter (Repo).TraceBackward")
	defer logger.Debug("exit (Repo).TraceBackward")
	return r.trace(field, funcID, maxDepth, false)
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
		maxDepth = 8 // 默认深度（Q28，--max-depth 可调）
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
    -- 起点：函数参数（调用方经 argument 进入 callee 对该字段的实际写入，
    -- 问题①：调用方函数内无该字段直接访问时仍能正向追踪）
    SELECT n.id, 0, n.name, '', n.line_start, 0, n.kind
    FROM nodes n
    WHERE n.kind = 'ssa_value'
      AND json_extract(n.properties, '$.func_id') = ?
      AND json_extract(n.properties, '$.origin_kind') IN ('param','receiver')
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
      -- （① 跳板入口：dest.Field = src.Field 拷贝链经中间读连到目标写入）；
      -- 字段访问 → 字段访问 仅限目标字段（切断嵌套展开与参数全部
      -- 字段入链的指数噪音，④ 超时防护）
      AND (n_next.kind != 'field_access'
           OR json_extract(n_next.properties, '$.full_path') = ?
           OR (d.kind != 'field_access' AND json_extract(n_next.properties, '$.access_kind') = 'read'))
      AND d.depth < ?
)
SELECT id, depth, name, edge_kinds, line, is_usage FROM fwd_trace ORDER BY depth, id`
	}
	var (
		rows *sql.Rows
		err  error
	)
	if forward {
		rows, err = r.Query(query, field, string(funcID), string(funcID), field, field, maxDepth)
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

// paramValueID 将 parameter/receiver 节点 ID 转换为对应的 ssa_value 参数 ID：
// #param.recv.<name> / #param.<name> → #<name>；非参数 slot 返回空。
func paramValueID(id string) string {
	hash := strings.LastIndex(id, "#")
	if hash < 0 {
		return ""
	}
	prefix, slot := id[:hash], id[hash+1:]
	switch {
	case strings.HasPrefix(slot, "param.recv."):
		return prefix + "#" + strings.TrimPrefix(slot, "param.recv.")
	case strings.HasPrefix(slot, "param."):
		return prefix + "#" + strings.TrimPrefix(slot, "param.")
	}
	return ""
}

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
// tbl 为递归目标节点别名（反向 n_prev / 正向 n_next）。
func valueTraceFilter(anchorCtx, anchorInst string, reverse bool, tbl string) string {
	dirAccess := "'read'"
	if !reverse {
		dirAccess = "'write'"
	}
	fp := `json_extract(` + tbl + `.properties, '$.full_path')`
	inst := `COALESCE(json_extract(` + tbl + `.properties, '$.instance_path'), json_extract(` + tbl + `.properties, '$.full_path'))`
	return fp + ` = ` + q(anchorCtx) + `
OR (` + q(anchorCtx) + ` != '' AND (instr(` + q(anchorInst) + `, ` + inst + `) = 1 OR instr(` + inst + `, ` + q(anchorInst) + `) = 1))
OR json_extract(` + tbl + `.properties, '$.is_external') = 'true'
OR (d.kind != 'field_access' AND (` + q(anchorCtx) + ` = '' OR json_extract(` + tbl + `.properties, '$.access_kind') = ` + dirAccess + `))`
}

func q(s string) string { return "'" + s + "'" }

func (r *Repo) GetValueTrace(nodeID domain.CanonicalID, maxDepth int) ([]*domain.TraceRow, error) {
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
	backFilter := valueTraceFilter(ctx, inst, true, "n_prev")
	fwdFilter := valueTraceFilter(ctx, inst, false, "n_next")
	rows, err := r.Query(`WITH RECURSIVE vt(id, depth, name, edge_kinds, line, dir, kind, access, func_id, full_path, ctx, inst) AS (
    SELECT n.id, 0, n.name, '', n.line_start, 0, n.kind,
           json_extract(n.properties, '$.access_kind'), json_extract(n.properties, '$.func_id'),
           json_extract(n.properties, '$.full_path'), `+q(ctx)+`, `+q(inst)+`
    FROM nodes n WHERE n.id = ?
    UNION
    -- 反向：流向当前节点（产生链）
    SELECT e.source_id, d.depth + 1, n_prev.name,
           CASE WHEN d.edge_kinds = '' THEN e.kind
                ELSE d.edge_kinds || ',' || e.kind END, n_prev.line_start, 0,
           n_prev.kind, json_extract(n_prev.properties, '$.access_kind'),
           json_extract(n_prev.properties, '$.func_id'),
           json_extract(n_prev.properties, '$.full_path'),
           CASE WHEN n_prev.kind = 'field_access'
                THEN json_extract(n_prev.properties, '$.full_path') ELSE d.ctx END,
           CASE WHEN n_prev.kind = 'field_access'
                THEN COALESCE(json_extract(n_prev.properties, '$.instance_path'), json_extract(n_prev.properties, '$.full_path')) ELSE d.inst END
    FROM edges e
    JOIN vt d ON e.target_id = d.id
    JOIN nodes n_prev ON e.source_id = n_prev.id
    WHERE e.kind IN ('data_flows_to','argument','returns','phi_operand','summary_io')
      AND (d.dir = 0 OR d.depth = 0) AND d.depth < ?
      AND (n_prev.kind != 'field_access' OR (`+backFilter+`))
    UNION
    -- 正向：从当前节点流出（使用链）
    SELECT e.target_id, d.depth + 1, n_next.name,
           CASE WHEN d.edge_kinds = '' THEN e.kind
                ELSE d.edge_kinds || ',' || e.kind END, n_next.line_start, 1,
           n_next.kind, json_extract(n_next.properties, '$.access_kind'),
           json_extract(n_next.properties, '$.func_id'),
           json_extract(n_next.properties, '$.full_path'),
           CASE WHEN n_next.kind = 'field_access'
                THEN json_extract(n_next.properties, '$.full_path') ELSE d.ctx END,
           CASE WHEN n_next.kind = 'field_access'
                THEN COALESCE(json_extract(n_next.properties, '$.instance_path'), json_extract(n_next.properties, '$.full_path')) ELSE d.inst END
    FROM edges e
    JOIN vt d ON e.source_id = d.id
    JOIN nodes n_next ON e.target_id = n_next.id
    WHERE e.kind IN ('data_flows_to','argument','returns','phi_operand','summary_io')
      AND (d.dir = 1 OR d.depth = 0) AND d.depth < ?
      AND (n_next.kind != 'field_access' OR (`+fwdFilter+`))
)
SELECT id, depth, name, edge_kinds, line, dir, kind, access, func_id, full_path FROM vt ORDER BY dir, depth, id`,
		string(nodeID), maxDepth, maxDepth)
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
	fwdFilter := valueTraceFilter(ctxField, ctxField, false, "n_next")
	rows, err := r.Query(`WITH RECURSIVE vt(id, depth, name, edge_kinds, line, dir, kind, access, func_id, full_path, ctx) AS (
    SELECT n.id, 0, n.name, '', n.line_start, 0, n.kind,
           json_extract(n.properties, '$.access_kind'), json_extract(n.properties, '$.func_id'),
           json_extract(n.properties, '$.full_path'), `+q(ctxField)+`
    FROM nodes n WHERE n.id IN (`+placeholders+`)
    UNION
    SELECT e.target_id, d.depth + 1, n_next.name,
           CASE WHEN d.edge_kinds = '' THEN e.kind
                ELSE d.edge_kinds || ',' || e.kind END, n_next.line_start, 1,
           n_next.kind, json_extract(n_next.properties, '$.access_kind'),
           json_extract(n_next.properties, '$.func_id'),
           json_extract(n_next.properties, '$.full_path'),
           CASE WHEN n_next.kind = 'field_access'
                THEN json_extract(n_next.properties, '$.full_path') ELSE d.ctx END
    FROM edges e
    JOIN vt d ON e.source_id = d.id
    JOIN nodes n_next ON e.target_id = n_next.id
    WHERE e.kind IN ('data_flows_to','argument','returns','phi_operand','summary_io')
      AND d.depth < ?
      AND (n_next.kind != 'field_access' OR (`+fwdFilter+`))
)
SELECT id, depth, name, edge_kinds, line, dir, kind, access, func_id, full_path FROM vt ORDER BY depth, id`,
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

// GetIndirectWriteEdges 返回函数的 INDIRECT_WRITE 边（Q90 调用点回连：
// metadata 含 call_line / call_args）。
func (r *Repo) GetIndirectWriteEdges(funcID domain.CanonicalID) ([]*domain.Fact, error) {
	logger := zap.L()
	logger.Debug("enter (Repo).GetIndirectWriteEdges")
	defer logger.Debug("exit (Repo).GetIndirectWriteEdges")
	rows, err := r.Query(`SELECT source_id, target_id, kind, tool_source, confidence, metadata
		FROM edges WHERE source_id = ? AND kind = 'indirect_write'`, string(funcID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFacts(rows)
}

// GetDispatchEdges 返回接口类型的 dispatch_to 边（Q95：symbol 详情候选集）。
func (r *Repo) GetDispatchEdges(ifaceID domain.CanonicalID) ([]*domain.Fact, error) {
	logger := zap.L()
	logger.Debug("enter (Repo).GetDispatchEdges")
	defer logger.Debug("exit (Repo).GetDispatchEdges")
	rows, err := r.Query(`SELECT source_id, target_id, kind, tool_source, confidence, metadata
		FROM edges WHERE source_id = ? AND kind = 'dispatch_to'`, string(ifaceID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFacts(rows)
}

// FindFieldReads 按 full_path 查字段读节点（③：写锚点的下游消费跳板——
// 同字段的读节点及其使用链）。
func (r *Repo) FindFieldReads(fullPath string) ([]*domain.CodeEntity, error) {
	logger := zap.L()
	logger.Debug("enter (Repo).FindFieldReads")
	defer logger.Debug("exit (Repo).FindFieldReads")
	rows, err := r.Query(`SELECT id, kind, name, file_path, line_start, line_end, properties
		FROM nodes WHERE kind = 'field_access'
		  AND json_extract(properties, '$.access_kind') = 'read'
		  AND json_extract(properties, '$.full_path') = ?`, fullPath)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanNodes(rows)
}
