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

// SaveBatch 在单个事务中保存节点与边（节点必须先于边插入以满足外键）。

// SaveBatchStats 与 SaveBatch 相同，但返回批次统计（跳过的外键冲突边数），
// 并接受函数字段摘要行（function_field_summary）。
// 端点节点不存在的边（如 Git 追踪到 SCIP 未索引的文件）静默跳过，不中断构建。

// marshalProps 序列化节点属性；nil 映射为空对象（json_patch 需要对象操作数）。

// isFKError 判断是否为外键约束错误（SQLITE_CONSTRAINT_FOREIGNKEY = 787）。
// go-sqlite3 的 sqlite3.Error 用 ExtendedCode 存扩展错误码。

// SaveNode 保存单个节点（TD.md 4.2 接口）。

// SaveEdges 保存边列表（TD.md 4.2 接口）。

// DeleteByFile 删除某个文件的所有节点及其边（级联），用于增量构建。

// GetSymbol 按 Canonical ID 查询符号。

// GetSymbolByName 按名称查找：先精确匹配，无结果时退化为模糊匹配
// （CLI 按名查找用）。

// GetCallers 返回调用 id（或更上层）的边，深度 ≤ depth，置信度 ≥ minConfidence。
// 递归 CTE 沿 source 方向向上遍历（TD.md ImpactAnalysisSpecification）。

// GetCallees 返回 id 调用（或更下层）的边，深度 ≤ depth，置信度 ≥ minConfidence。

// walkEdges 沿单向方向递归遍历 CALLS 边。
//
//	callers: edges 从 id 向上（e.target_id 为已到达节点）
//	callees: edges 从 id 向下（e.source_id 为已到达节点）

// GetImpact 计算变更影响范围：从 id 出发沿任意方向遍历，深度 ≤ depth（TD.md 决策 10）。

// GetRoots 返回顶层入口节点（前端初始视图）：
//   - main 入口函数（排除测试包生成的 main，其 id 形如 <pkg>.test:main）
//   - HTTP 服务入口（serves_http 标记）
//   - gRPC 服务入口（serves_grpc 标记）
//   - 框架回调 struct：方法未被当前 module 其他文件调用（由框架调用）
//
// 约束：入口必须落在当前 module 内的文件（file_path 非空、非 _test.go、
// 非仓库外路径）。

// GetFrameworkStructs 返回"方法未被当前 module 其他文件调用"的 struct
// （无跨文件 caller → 推测由框架通过注册/回调机制调用），标记为顶层。

// shortStructID 压缩 struct ID 便于日志（保留 pkg 末段与类型名）。

// shortMethodID 压缩方法 ID 便于日志（保留类型名与方法名）。

// structIDFromMethod 将方法 ID（symbol:go:<pkg>:(T).M）还原为所属 struct ID
// （symbol:go:<pkg>:T）。

// Expand 返回节点的直接邻居（前端点击展开）：
//   - 双向的 calls / implements / imports 边（含方向）
//   - 邻居节点（去重）
//
// 上限 500 条边防止超大数据拖垮前端。

// Counts 返回节点数与边数（构建报告用）。

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
