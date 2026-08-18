// Package action 是 CLI 与 HTTP 共享的查询用例层（应用层）：
// 命令行和 HTTP 接口本身只负责参数解析与结果展示，全部图查询
// 经此层调用仓储。依赖方向：action → Reader 窄接口 ← *sqlite.Repo。
package action

import (
	"github.com/schaepher/codeintel/internal/domain"
)

// MinConfidence 调用关系查询默认置信度阈值（业务规则，TD.md 5.1：
// CALLS 边置信度 0.8；0.85 会过滤掉全部调用边）。
const MinConfidence = 0.8

// Reader 是 action 层依赖的仓储窄接口（*sqlite.Repo 实现）。
type Reader interface {
	GetSymbol(id domain.CanonicalID) (*domain.CodeEntity, error)
	GetSymbolByName(name string) ([]*domain.CodeEntity, error)
	GetCallers(id domain.CanonicalID, depth int, minConfidence float64) ([]*domain.Fact, error)
	GetCallees(id domain.CanonicalID, depth int, minConfidence float64) ([]*domain.Fact, error)
	GetImpact(id domain.CanonicalID, depth int) ([]*domain.CodeEntity, error)
	GetFunctionFields(funcID domain.CanonicalID) ([]*domain.FunctionFieldSummary, error)
	TraceBackward(field string, funcID domain.CanonicalID, maxDepth int) ([]*domain.TraceRow, error)
	TraceBackwardIndirect(field string, funcID domain.CanonicalID, maxDepth int) ([]*domain.TraceRow, error) // Q172 --follow-indirect
	TraceForward(field string, funcID domain.CanonicalID, maxDepth int) ([]*domain.TraceRow, error)
	GetValueTrace(nodeID domain.CanonicalID, maxDepth int, minConf float64, includeContainer bool) ([]*domain.TraceRow, error) // Q161/Q163
	GetValueTraceMulti(anchors []domain.CanonicalID, ctxField string, maxDepth int) ([]*domain.TraceRow, error)
	GetFunctionFlows(funcID domain.CanonicalID, maxDepth int) ([]*domain.TraceRow, error)
	GetRoots() ([]*domain.CodeEntity, error)
	Expand(id domain.CanonicalID) (facts []*domain.Fact, nodes []*domain.CodeEntity, err error)
	AllSummaries() ([]*domain.FunctionFieldSummary, error)
	GetIndirectWriteEdges(funcID domain.CanonicalID) ([]*domain.Fact, error)
	GetDispatchEdges(ifaceID domain.CanonicalID) ([]*domain.Fact, error)
	GetDispatchTargets() (map[domain.CanonicalID]domain.DispatchMeta, error) // Q157 P1
	FindFieldReads(fullPath string) ([]*domain.CodeEntity, error)
	GetTableColumns(table string) ([]*domain.TableColumn, error)
	GetAllTableColumns() ([]*domain.TableColumn, error) // ER 图：全库外部列（无 writers/readers 明细）
	GetTableRelations(table, memoryMode string) ([]*domain.TableRelation, error)
	GetAllTableRelations(memoryMode string) ([]*domain.TableRelation, error) // Q160 全库聚合
	GetUncalledFunctions() ([]*domain.UnusedFunc, error)
	GetIsolatedChains() ([][]*domain.UnusedFunc, error)
	GetPath(from, to domain.CanonicalID, maxDepth int, viaCalls bool) ([]*domain.TraceRow, error)
	GetGrpcCalls() ([]*domain.GrpcCallRow, error)
	Counts() (nodes int, edges int, err error)
	GetLatest() (*domain.BuildMeta, error)
	RepoPath() string
}

// Actions 是 CLI 与 HTTP 共享的查询用例集合。
type Actions struct {
	repo     Reader
	modNames []string // 全部 module 路径缓存（P2-3 多 go.mod；modules() 填充）
}

// New 创建 Actions。
func New(repo Reader) *Actions {
	return &Actions{repo: repo}
}

// ResolveSymbol 将用户输入解析为符号：canonical ID 直接命中，否则按名称查找；
// 多匹配时返回错误并列出候选 ID（原 CLI 语义，供符号类 action 复用）。

// joinIDs 拼接候选 ID（多匹配错误提示用）。

// ResolveAnchor 解析摘要/生命周期锚点（③⑤）：canonical ID 直连、符号
// 名称解析，类型限定字段路径（example.com/m.T.A）回退到同字段读节点
// （FindFieldReads 首个）——此前字段路径被误报"不存在的符号"。

// 写锚点跳板限界（④ 超时防护）：读节点数上限 + 子追溯深度——避免
// 同字段大量读节点各自跑一遍深度 8 双向全链。
const (
	trampolineMaxReads = 8
	trampolineDepth    = 4
)

// downstreamTrampoline 写锚点下游跳板（③⑤⑧）：写节点无出边——经同
// full_path 读节点接入使用链（读节点行 + dir=1 子链行）；非写锚点返回 nil。
// 读锚点的下游用单次合并 CTE 查询（GetValueTraceMulti），替代逐读节点
// 递归——读点多时累计成本大幅下降（⑧ 超时防护）。

// Lifecycle 生命周期图行（⑤）：value-trace 全链 + 写锚点的下游跳板
// （同字段读节点的使用链），行按 ID 去重（首个保留）。供
// export graph --type lifecycle 与前端展示使用。

// Symbol 按 canonical ID 查询符号（HTTP expand 的存在性检查用）。

// SymbolDetail 符号详情：基本信息 + 调用者/被调用者摘要（query symbol）。

// SymbolDetail 解析符号并返回其详情（调用者/被调用者深度 1）。

// Callers 返回调用 id 的边（深度 ≤ depth，置信度 ≥ MinConfidence）。

// Callees 返回 id 调用的边（深度 ≤ depth）。

// Impact 返回变更影响范围（深度 ≤ depth）。

// FunctionFields 解析函数并返回其字段读写摘要（S1，field_trace.md §6.2）。

// TraceParams 字段追溯参数（S2/S3）。
type TraceParams struct {
	Field          string
	Func           string // 函数符号输入（canonical ID 或名称）
	MaxDepth       int
	Forward        bool // true=trace-forward（S3 后续使用），false=trace-backward（S2 产生点）
	FollowIndirect bool // Q172：trace-backward --follow-indirect（跨函数间接写链）
}

// Trace 字段产生点反向追溯 / 后续使用正向追踪；返回解析后的函数符号
// 与追溯路径（符号供展示层输出函数名）。

// ValueTrace 数据值全链追踪（field_trace.md §14.2）。
// Table 表级数据流聚合（query table）：表名 → 列虚拟节点 + 写入方。

// Relations 表间关联分析（query relations）：表名 → 沿数据流链关联
// 的其他表.列（代码层推断，无外键依赖）。memoryMode：--memory 参数
// （""=auto/full/sql，见 repo.GetTableRelations）。

// RelationsAll 全库表间关联聚合（query relations --all / export relations，Q160）：
// 一次遍历全部表返回所有表对关联（合并去重）。memoryMode 同 Relations。

// markDispatchCandidates 标注候选派发（Q157 P1）：value-trace 行所属
// 函数是接口 dispatch_to 边 target（候选实现）时标记来源与置信度——
// 链路混入多个接口候选实现时可区分。
func (a *Actions) markDispatchCandidates(rows []*domain.TraceRow) ([]*domain.TraceRow, error) {
	targets, err := a.repo.GetDispatchTargets()
	if err != nil {
		return nil, err
	}
	if len(targets) == 0 {
		return rows, nil
	}
	for _, r := range rows {
		if r.FuncID == "" {
			continue
		}
		if m, ok := targets[domain.CanonicalID(r.FuncID)]; ok {
			r.DispatchCandidate = true
			r.DispatchOrigin = m.Origin
			r.DispatchConf = m.Confidence
		}
	}
	return rows, nil
}

// Roots 返回顶层入口节点（前端初始视图）。
func (a *Actions) Roots() ([]*domain.CodeEntity, error) {
	return a.repo.GetRoots()
}

// Search 全库符号搜索（名称/ID 模糊匹配，上限由仓储实现决定）。
func (a *Actions) Search(q string) ([]*domain.CodeEntity, error) {
	return a.repo.GetSymbolByName(q)
}

// Expand 返回节点的直接邻居（facts + 邻居节点）；返回当前节点供存在性检查。
func (a *Actions) Expand(id domain.CanonicalID) (cur *domain.CodeEntity, facts []*domain.Fact, nodes []*domain.CodeEntity, err error) {
	cur, err = a.repo.GetSymbol(id)
	if err != nil {
		return nil, nil, nil, err
	}
	facts, nodes, err = a.repo.Expand(id)
	if err != nil {
		return nil, nil, nil, err
	}
	return cur, facts, nodes, nil
}

// Flows 返回函数内完整字段数据流（前端 /api/flows 用）。

// ExportField 双层索引中的一个字段条目（S4，field_trace.md §2）。

// ExportEntry 单个产生者/消费者条目。

// Counts 返回节点数与边数（构建健康检查，serve 启动校验用）。

// Latest 返回最近一次构建元数据（serve 启动校验用）。

// IndirectWriteSites 返回函数的 INDIRECT_WRITE 边（Q90 调用点回连：
// metadata 含 call_line / call_args，fields 展示用）。

// DispatchCandidates 返回接口类型的候选实现（Q95：symbol 详情展示）。

// ExportIndex 生成 字段 → 产生者/消费者 的双层索引（S4）。
// direct_read 为消费者；direct_write / indirect_write 均为产生者。

// SummaryStep 跨层摘要的一步（Q100）。

// SummaryChain 提取字段生命周期主链（Q100）：从锚点双向取最长路径
// （产生链到源头 + 使用链到消费），每 depth 层取首个节点（value-trace
// 结果按 dir/depth/id 有序）。步骤类型标注：源头=entry、sql/metric/
// 字段写=write、末端=consume、其余=compute。
// 写锚点的下游（③）：写节点无出边——经"同 full_path 的读节点"跳板
// 接入读的使用链（字段级关联：写入 → 后续读取消费）。

// shortFuncNameX 从 canonical ID 取函数短名（action 层展示用）。

// Path 节点间最短路径（field_trace.md §17.3）：两端经 ResolveAnchor
// 解析（canonical ID / 符号名 / 字段路径）；viaCalls=true 用函数调用
// 边集，否则数据流边集。不可达返回空切片。

// UnusedReport 未调用分析报告（field_trace.md §16.4）。

// Unused 未调用函数与孤立链分析（field_trace.md §16）：
//   - 未调用 = 无 calls/passes_result 入边（Called=false）
//   - 无引用 = 且无 passes_to/dispatch_to/initializes/var 初始化引用
//   - 孤立链：链头无 caller，链内 caller ⊆ 链，有链外 caller 断开，环整环孤立
//   - --since：标注 [new]（声明行在新增行）/ [mod]（行号区间命中新增行）
//     并只保留标注过的函数（流程衔接检查）；since 为 nil 时全量报告

// MarkSince 标注函数在 --since 中的状态：new（声明行命中 diff 新增行或
// 新增文件）/ mod（行号区间命中新增行）/ ""（未改动）。纯函数，
// UnusedFunc 与 CodeEntity（--since 标注推广，§17.2）共用。

// sinceMark UnusedFunc 版（--since 标注）。
