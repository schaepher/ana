package sqlite

import (
	"database/sql"
	"errors"
	"sync"

	"github.com/schaepher/codeintel/internal/domain"
	"go.uber.org/zap"
)

// 确保 DB 实现仓储接口
var _ domain.CodeRepository = (*Repo)(nil)
var _ domain.BuildMetadataRepository = (*Repo)(nil)

// Repo 实现 CodeRepository / BuildMetadataRepository。
type Repo struct {
	*DB
	relationHops domain.RelationHops // Q197：三类关系跳数上限（0=不限制），默认 4

	// 任务 #165：serve 进程内关系图缓存（cachedRelationGraph）——
	// 单表展开/全量查询复用内存图，避免每次 loadRelationGraph（go2o
	// 530ms）。图对象只读共享（BFS 纯读，Go map 并发读安全），锁只
	// 保护缓存槽本身；键 = build_id + 分析逻辑版本，构建/逻辑变化
	// 自动失效重载。
	graphMu       sync.RWMutex
	graphCacheKey string // 缓存键；空串 = 不缓存（无 build_metadata）
	graphCache    *relationGraph
}

// SetRelationHops 配置三类关系的跳数上限（--query-max-hops 等，Q197）：
// 传入 0 的类型不限制；未调用时默认 DefaultRelationHops（全部 4 跳）。
func (r *Repo) SetRelationHops(h domain.RelationHops) {
	r.relationHops = h
}

// NewRepo 基于已打开的数据库创建仓储。
func NewRepo(db *DB) *Repo {
	logger := zap.L()
	logger.Debug("enter NewRepo")
	defer logger.Debug("exit NewRepo")
	return &Repo{DB: db, relationHops: DefaultRelationHops}
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

// insertSummarySQL Q215：OR REPLACE 覆盖（原 OR IGNORE——UNIQUE 冲突
// 保留旧行，函数修改后行号/代码片段陈旧，fields 展示旧数据）。行残留
// （函数删除）由 FK ON DELETE CASCADE 保证（nodes 删除级联）。REPLACE
// 语义：DELETE 旧行 + INSERT 新行——同 UNIQUE 键内容覆盖；origins 无
// 子表依赖不受影响。
const insertSummarySQL = `
INSERT OR REPLACE INTO function_field_summary
    (function_id, access_kind, field_path, instance_path, line_start, code_snippet)
VALUES (?, ?, ?, ?, ?, ?)`

// saveBatchResult 记录批次写入的统计信息。
type saveBatchResult struct {
	// SkippedEdges 因外键冲突（端点节点不存在）被跳过的边数。
	// 注：FK 失败先进入 Failed*（构建尾部重试），重试后仍失败才计入。
	SkippedEdges int
	// FailedEdges/FailedSummaries/FailedOrigins FK 冲突项（端点节点尚未
	// 落库——并发构建跨批依赖）→ 调用方收集后于全部节点落库后重试
	// （P2：原实现静默跳过导致非确定性丢边，go2o 三次重建 156217/
	// 156214/156217）。
	FailedEdges     []*domain.Fact
	FailedSummaries []*domain.FunctionFieldSummary
	FailedOrigins   []*domain.SummaryOrigin
}

// Save 保存构建元数据。
func (r *Repo) Save(meta *domain.BuildMeta) error {
	logger := zap.L()
	logger.Debug("enter (Repo).Save")
	defer logger.Debug("exit (Repo).Save")
	_, err := r.Exec(`INSERT OR REPLACE INTO build_metadata
		(build_id, commit_sha, tool_name, status, duration_ms, error_message, nodes_count, edges_count)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		meta.BuildID, meta.CommitSHA, meta.ToolName, meta.Status, meta.DurationMs, meta.ErrorMsg,
		meta.Nodes, meta.Edges)
	return err
}

// GetLatest 获取最近一次构建元数据。
func (r *Repo) GetLatest() (*domain.BuildMeta, error) {
	logger := zap.L()
	logger.Debug("enter (Repo).GetLatest")
	defer logger.Debug("exit (Repo).GetLatest")
	m := &domain.BuildMeta{}
	// timestamp 为秒级：同一秒内多次构建须按写入顺序取最新（rowid 递增）
	err := r.QueryRow(`SELECT build_id, commit_sha, tool_name, status, duration_ms, error_message,
		COALESCE(nodes_count, 0), COALESCE(edges_count, 0)
		FROM build_metadata ORDER BY timestamp DESC, rowid DESC LIMIT 1`).
		Scan(&m.BuildID, &m.CommitSHA, &m.ToolName, &m.Status, &m.DurationMs, &m.ErrorMsg,
			&m.Nodes, &m.Edges)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return m, nil
}
