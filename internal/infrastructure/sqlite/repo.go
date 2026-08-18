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

// AllSummaries 返回全部函数字段摘要行（S4 导出用，field_trace.md §2），
// 按 field_path, access_kind 排序。

// GetFunctionFields 按函数查询字段摘要，附带 Q161 origins（间接写
// 多来源：调用点 × 被调函数，origin/confidence 由 action 层 join
// dispatch_to 边填充）。

// scanSummaries 扫描摘要查询行（GetFunctionFields/AllSummaries 共用）。

// TraceBackward 反向追溯字段产生点（S2，field_trace.md §6.3）：
// 起点为入口函数内匹配 full_path 的 field_access 节点，沿
// data_flows_to / argument / returns / alias / phi_operand 反向遍历。

// TraceBackwardIndirect 反向追溯 + 跨函数间接写（Q172 --follow-indirect）：
// 起点函数对目标字段只有 function_field_summary.indirect_write（无本函数
// 真实 field_access）时，沿 summary_origins 递归解析调用链（outer → inner
// → ... → 真实写者），收集链上全部函数的 field_access 写节点作起点，
// 再执行反向 data_flows_to 遍历（赋值来源）。

// TraceForward 正向追踪字段后续使用（S3，field_trace.md §6.4）：
// 起点同 S2，沿 data_flows_to / argument / returns / phi_operand / alias
// 正向遍历（跨函数经 argument/returns 边，不沿函数级 calls 边跳跃）；
// 遇到匹配 full_path 的 field_access 标记为使用点。

// trace 递归 CTE 实现 S2/S3；UNION 去重 + 深度限制防环（Q49）。

// lastSeg 取类型限定字段路径的字段名（example.com/m.T.FinalFee → FinalFee）。

// pkgOf 取类型限定字段路径的模块包路径（example.com/m.T.FinalFee → example.com/m）。

// GetFunctionFlows 返回函数内完整字段数据流（前端 /api/flows 用）：
// 起点 = 函数内全部 field_access 节点，双向遍历 data_flows_to / phi_operand
// （func_id 限定在函数内，到参数/返回边界即止）；Dir=0 为产生链（反向），
// Dir=1 为使用链（正向）。

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

// sortEdgeKinds 排序边类型集合（Q155：GROUP_CONCAT(DISTINCT) 无序，
// server/CLI 按 LastIndex 取末段展示——排序保证输出稳定）。

// GetValueTraceMulti 多锚点合并正向追踪（⑧ 跳板合并）：一次查询返回
// 全部锚点的下游使用链（dir=1），字段访问步按锚点字段 ctx 限定。
// trampoline 用它替代 N 次 GetValueTrace——读点多时累计查询成本
// 大幅下降（单次 CTE + UNION 去重）。

// GetIndirectWriteEdges 返回函数的 INDIRECT_WRITE 边（Q90 调用点回连：
// metadata 含 call_line / call_args）。

// GetDispatchEdges 返回接口类型的 dispatch_to 边（Q95：symbol 详情候选集）。

// GetDispatchTargets 返回全部 dispatch_to 边的候选实现 → 派发元数据
// （Q157 P1：value-trace 候选标注——链路混入多个接口候选实现时区分）。

// FindFieldReads 按 full_path 查字段读节点（③：写锚点的下游消费跳板——
// 同字段的读节点及其使用链）。

// ResetGraphTables 清空图数据表（DROP + 重建）——全量重建语义
// （orchestrator.FullBuild 用）。比 DELETE 全表快（无逐行 WAL/索引
// 维护）且释放文件空间；build_metadata（构建记录）与未来配置表
// 保留。FK 顺序：先 DROP 子表（edges/function_field_summary）
// 再 DROP 父表（nodes）。

// GetTableRelations 表间关联分析（query relations）：对该表的全部列
// 虚拟节点沿数据流边 BFS，收集命中其他表的虚拟节点（表.列，is_external）。
// P0② 一次加载全图到内存（loadRelationGraph）替代逐节点 SQL；P0③ 结果
// 按 build_id 缓存到 relation_candidates，命中直接返回（无 build_metadata
// 时跳过缓存）。mode 为 --memory（""=auto 按规模、full=强制内存图、
// sql=强制逐节点 SQL——大仓库防爆内存逃生口）。无外键依赖——纯代码使用
// 方式推断（A.x 读出值流入 B.y 过滤/写入）。

// GetTables 枚举全库外部表名（gorm/sql 虚拟节点表名去重，Q160）。

// GetAllTableRelations 全库关联聚合（Q160）：一次加载图（loadRelationGraph），
// 全部表内存 BFS 合并去重——同 from/to 列对取 hops 最小 + Type 最高
// （query > write > read）。结果按 build_id 全量写入 relation_candidates
// （--all 重建缓存，后续单表查询命中缓存）。输出按 from/to 稳定排序，
// AGENT 一次调用拿全库（query relations --all / export relations）。
// mode 同 GetTableRelations（--memory）；sql 模式逐表走 relationsForSQL。

// relTypeRank 关联类型优先级（聚合去重用）：query > write > read。

// GetTableColumns 按表名聚合列虚拟节点（query table）：Name=表（整表行）
// 或 表.列（Q97 持久化映射）；每列带写入方（summary_io 入边 source 值节点
// 的所属函数与行号）。读取方（出边）通常为空——SELECT 读路径未解析。

// shortNameFromID 从 canonical ID 提取函数短名（symbol:go:<pkg>:(T).m → (T).m）。

// GetUncalledFunctions 返回全部函数/方法（除 main/init）的调用状态
// （field_trace.md §16.2）：
//   - Called：有 calls / passes_result 入边（被调用，含嵌套调用）
//   - Referenced：有 passes_to（回调参数）/ dispatch_to（接口派发）/
//     initializes（被实例化）/ var 初始化引用（data_flows_to → var.Global）
// 返回全部函数，调用方按 Called/Referenced 过滤展示两档报告。

// edgeTargetKinds 一次查询返回指定 kind 边的全部 target_id 集合
// （unused 预聚合，替代逐行 EXISTS 子查询）。

// varInitFuncs 返回被全局变量初始化表达式引用的函数集合（data_flows_to
// → var.* 节点，source 的 func_id——Q108：包初始化调用的函数不算孤立）。

// GetIsolatedChains 返回孤立调用链（field_trace.md §16.3）：
// 链头 = 无 caller 的函数（非 main/init）；沿 callee 递归；
// 遇有链外 caller 的节点断开（该节点及下游不入链）；
// 互调环（无外部 caller）整环孤立。单节点（无 callee）自成链。
// main/init 参与构图（其调用使被调函数不算孤立），但永不作为链头或链成员。

// GetPath 节点间最短路径（field_trace.md §17.3）：
// BFS（有向 from→to，visited 防环），返回路径节点序列（TraceRow，
// EdgeKinds = 进入该节点的边类型）。viaCalls=true 用函数调用边集
// （calls/passes_to/passes_result），否则数据流边集（data_flows_to/
// argument/returns/phi_operand/summary_io）。不可达返回空切片。

// GetGrpcCalls 模块间调用原始行（field_trace.md §18.3/§18.7）：
// grpc_call 边（客户端调用方 → grpc_service）+ 经 grpc_impl 边反查
// 服务端实现类型；http_call 边（→ http_route，经 route.handler_id
// 反查服务端 handler 函数）。无实现/无 handler 时 ImplTypeID 空——
// 服务端不在仓库内（[外部服务]）。

// pkgOfID 从 canonical ID 提取包路径（symbol:go:<pkg>:<name>）。
