package domain

import "errors"

import (
	"context"

	"golang.org/x/tools/go/packages"
)

// Item 是适配器流式产出的原始数据单元：节点 / 边 / 函数字段摘要行。
// json tag（Q243 JSON 契约）。
type Item struct {
	Node    *CodeEntity           `json:"node,omitempty"`
	Fact    *Fact                 `json:"fact,omitempty"`
	Summary *FunctionFieldSummary `json:"summary,omitempty"`
	Origins []*SummaryOrigin      `json:"origins,omitempty"` // Q161 摘要来源（indirect_write 多分支）
}

// SummaryOrigin 摘要来源（Q161）：某字段 indirect_write 的一个来源
// （调用点行号 + 被调函数）；落库三键 function_id/access_kind/field_path
// 与摘要行配套；origin/confidence 查询期从 dispatch_to 边 join（callee
// 是候选实现时带出 register/enum + 置信度）。
type SummaryOrigin struct {
	FunctionID CanonicalID `json:"function_id"`
	AccessKind string      `json:"access_kind"`
	FieldPath  string      `json:"field_path"`
	CallLine   int         `json:"call_line"`
	CalleeID   CanonicalID `json:"callee_id"`
	Origin     string      `json:"origin,omitempty"` // register / enum（动态候选时，查询期填充）
	Confidence float64     `json:"confidence,omitempty"`
}

// FunctionFieldSummary 函数字段摘要行（function_field_summary 表，
// 构建时预计算，加速 S1 查询，field_trace.md §5.2）。
type FunctionFieldSummary struct {
	FunctionID   CanonicalID      `json:"function_id"`
	AccessKind   string           `json:"access_kind"` // direct_read / direct_write / indirect_write
	FieldPath    string           `json:"field_path"`  // 类型限定路径（同 field_access.full_path）
	InstancePath string           `json:"instance_path"`
	LineStart    int              `json:"line_start"`
	CodeSnippet  string           `json:"code_snippet,omitempty"`
	Origins      []*SummaryOrigin `json:"origins,omitempty"` // Q161 间接写多来源（查询期填充）
}

// 摘要 access_kind 常量。
const (
	SummaryDirectRead    = "direct_read"
	SummaryDirectWrite   = "direct_write"
	SummaryIndirectWrite = "indirect_write"
)

// TraceRow 字段追溯路径上的一步（S2/S3，field_trace.md §6.3/6.4）。
type TraceRow struct {
	ID                CanonicalID `json:"id"`
	Depth             int         `json:"depth"`
	ParentID          CanonicalID `json:"parent_id,omitempty"` // 到达该节点的父节点（Q235-11：mermaid 父子链连线）
	Name              string      `json:"name"`
	EdgeKinds         string      `json:"edge_kinds"` // 到达该节点经过的边类型（逗号连接）
	Line              int         `json:"line"`
	IsUsage           bool        `json:"is_usage"` // S3：该节点是否为匹配 full_path 的使用点
	Dir               int         `json:"dir"`      // 函数内数据流方向（GetFunctionFlows）：0=产生链（反向），1=使用链（正向）
	Kind              EntityKind  `json:"kind"`
	Access            string      `json:"access,omitempty"`             // field_access 的 read/write
	FuncID            string      `json:"func_id,omitempty"`            // 所属函数 canonical ID（GetValueTrace 函数上下文分组用）
	FilePath          string      `json:"file_path,omitempty"`          // 节点文件（Q235-10：CLI 渲染源码片段用）
	FullPath          string      `json:"full_path,omitempty"`          // field_access 的类型限定路径（前端展开匹配用）
	Conditions        []string    `json:"conditions,omitempty"`         // 路径条件标注（Q92 查询期计算，不落库）
	DispatchCandidate bool        `json:"dispatch_candidate,omitempty"` // 该行所属函数是接口候选实现（Q157 P1）
	DispatchOrigin    string      `json:"dispatch_origin,omitempty"`    // 候选来源（register / enum）
	DispatchConf      float64     `json:"dispatch_conf,omitempty"`      // 候选置信度
	EdgeIface         string      `json:"edge_iface,omitempty"`         // 到达该行的边是动态候选边（Q161）：接口类型
	EdgeOrigin        string      `json:"edge_origin,omitempty"`        // 候选来源（register / enum）
	EdgeConf          float64     `json:"edge_conf,omitempty"`          // 候选置信度（--min-conf 剪枝阈值用）
}

// DispatchMeta 接口派发元数据（Q157 P1：value-trace 候选标注用）。
// json tag（Q243 JSON 契约）。
type DispatchMeta struct {
	Origin     string  `json:"origin"`     // register / enum
	Confidence float64 `json:"confidence"` // 0.0~1.0
}

// UnusedFunc 未调用分析中的一个函数（field_trace.md §16）。
// json tag（Q243 JSON 契约）。
type UnusedFunc struct {
	ID         CanonicalID `json:"id"`
	Kind       EntityKind  `json:"kind"`
	Name       string      `json:"name"`
	FilePath   string      `json:"file_path"`
	LineStart  int         `json:"line_start"`
	LineEnd    int         `json:"line_end"`
	Exported   bool        `json:"exported"`             // 首字母大写（可能被外部模块调用）
	Called     bool        `json:"called"`               // 有 calls / passes_result 入边
	Referenced bool        `json:"referenced"`           // 有 passes_to / dispatch_to / initializes / var 初始化引用
	SinceMark  string      `json:"since_mark,omitempty"` // --since 标注："" / "new"（声明行新增）/ "mod"（行号区间新增）
}

// GrpcCallRow 模块间调用原始行（field_trace.md §18.3）：grpc_call 边 +
// 服务端实现归属（grpc_impl 边反向查）。
// json tag（Q243 JSON 契约）。
type GrpcCallRow struct {
	CallerID   CanonicalID `json:"caller_id"`  // 客户端调用方函数
	ServiceID  CanonicalID `json:"service_id"` // grpc_service / http_route 节点
	Service    string      `json:"service"`    // grpc：生成包路径+服务名；http：route 名
	Method     string      `json:"method"`     // grpc：方法名；http：路径
	Line       int         `json:"line"`
	ImplTypeID CanonicalID `json:"impl_type_id,omitempty"` // 服务端实现（grpc_impl 边 source / route.handler_id；无实现时空）
	Transport  string      `json:"transport"`              // grpc_call / http_call
}

// TableEndpoint 表列数据流的端点（写入方/读取方，query table）。
type TableEndpoint struct {
	FuncID   string // 函数 canonical ID（summary_io 边 source 的值节点所属函数）
	FuncName string // 函数短名（从 ID 提取）
	Line     int    // 调用行号
}

// TableColumn 表的一列虚拟节点及数据流（query table）。
type TableColumn struct {
	Name      string          `json:"name"`              // 表.列（无列时为表名）
	Access    string          `json:"access"`            // read / write / filter
	LineStart int             `json:"line_start"`        // 定义行号
	Writers   []TableEndpoint `json:"writers,omitempty"` // summary_io 入边（值 → 虚拟节点）：谁写入该列
	Readers   []TableEndpoint `json:"readers,omitempty"` // 虚拟节点出边（消费）；SELECT 读路径未解析时为空
}

// 表关联类型（关联终点虚拟节点的 access_kind 判定）：
const (
	RelationFK = "fk" // Q218：query 的子集——值级 taint 验证通过的真实键关联
	// （链上对象字段读与起点列 lowercase 呼应，值确实从起点列流来）——
	// ER 图默认连线类型；fk 默认不限跳（值流已验证）
	RelationQuery = "query" // 终点是 WHERE 过滤列（filter）——A 的值作为 B 的查询条件（键关联，高置信；含对象字段换名型噪声）
	RelationWrite = "write" // 终点是写入列——同源/间接写入（值相关，中置信）
	RelationRead  = "read"  // 终点是读出列——间接扩散（低置信）
)

// TableRelation 表间关联（query relations）：本表某列的值沿数据流链
// 流入另一表的列（A.x 读出 → B.y 过滤/写入——代码层关联，无外键依赖）。
// RelationRule 用户连线规则（Q220c）：手工声明的表间关联——CLI
// `codeintel rule add` 或 ER 页面配置，读取期与推断关系合并
// （mergeRuleRelations）。FromTable 为空 = 模式规则（所有含 FromCol
// 列的表 → ToTable.ToCol）。生成关系 type=fk（用户声明可信）、hops=1。
type RelationRule struct {
	ID        int64  `json:"id"`
	FromTable string `json:"from_table,omitempty"` // '' = 模式规则
	FromCol   string `json:"from_col"`
	ToTable   string `json:"to_table"`
	ToCol     string `json:"to_col,omitempty"`
	CreatedAt int64  `json:"created_at,omitempty"`
}

// TableRelation 表间关联（query relations）：本表某列的值沿数据流链
// 流入另一表的列（A.x 读出 → B.y 过滤/写入——代码层关联，无外键依赖）。
type TableRelation struct {
	FromTable string `json:"from_table"` // 本表
	FromCol   string `json:"from_col"`   // 本表列
	ToTable   string `json:"to_table"`   // 关联表
	ToCol     string `json:"to_col"`     // 关联表列
	Hops      int    `json:"hops"`       // 数据流链长度（边数）
	Type      string `json:"type"`       // query（键关联）/ write（同源）/ read（间接）
}

// ErrRelationInProgress 全量 relations 计算未完成（Q228）：查询端据此
// 读 RelationProgress 返回进度（前端轮询），不现场计算——计算由
// precompute 命令或 serve 后台任务执行。
var ErrRelationInProgress = errors.New("relation compute in progress")

// RelationProgress 全量 relations 计算进度（Q228）：查询端（CLI --all /
// /api/er 全量）先查进度——done 才返回数据；running/pending 返回进度
// （前端轮询展示「计算关联中 X/Y 表」）。
type RelationProgress struct {
	Status string `json:"status"` // pending / running / done
	Done   int    `json:"done"`   // 已完成表数
	Total  int    `json:"total"`  // 总表数（0 = 未知）
}

// RelationHops 三类关系的跳数上限（Q197，0 = 不限制）：
// Query 键关联 / Write 同源写 / Read 间接读。
type RelationHops struct {
	Query int `json:"query"`
	Write int `json:"write"`
	Read  int `json:"read"`
}

// DefaultRelationHops 默认跳数上限（Q208 调整：Write=0 不限制——
// Q199/Q202 已把无值流 taint 的跨函数 write 丢弃，剩余 write 均经
// 精确判定（taint 呼应 / 外键形态），跳数上限已无降噪意义且会误伤
// 深层字段赋值链（order.id → A.order_id 6 跳）；query/read 保持 4）。
var DefaultRelationHops = RelationHops{Query: 4, Write: 0, Read: 4}

// ERTable ER 图的一个表节点（/api/er）：表名 + 列清单。
type ERTable struct {
	Name    string        `json:"name"`    // 表名
	Columns []TableColumn `json:"columns"` // 列（Name/Access/LineStart，无 writers/readers 明细）
}

// ERData 数据库 ER 图数据（/api/er）：全库外部表 + 表间关联
// （关系三级置信度：query 键关联高置信 / write 同源中置信 / read 间接低置信）。
type ERData struct {
	Tables    []ERTable        `json:"tables"`
	Relations []*TableRelation `json:"relations"`
}

// SinceInfo --since <ref> 的 diff 解析结果（field_trace.md §16.5）。
// json tag（Q243 JSON 契约）。
type SinceInfo struct {
	Ref        string                  `json:"ref"`         // git ref（--since 参数）
	NewFiles   map[string]bool         `json:"new_files"`   // 新增文件（文件内全部函数标 [new]）
	AddedLines map[string]map[int]bool `json:"added_lines"` // 每文件新增行号集合（+ 侧）
}

// EmitFunc 将适配器产出的数据流式交给 Canonicalizer 消费。
// 返回错误时适配器应停止产出。
type EmitFunc func(Item) error

// IndexerPort 六边形架构端口：所有外部分析工具（SCIP/CodeGraph/Git 等）
// 通过该端口接入，核心领域不依赖具体实现。
// pkgs 为 orchestrator 统一加载的 go/packages 结果（AST/SSA 适配器复用，
// 避免各自 Load 的类型检查翻倍——内存优化）；scip/git 适配器忽略。
type IndexerPort interface {
	Name() string
	Index(ctx context.Context, repo *Repository, pkgs []*packages.Package, emit EmitFunc) error
}

// CodeRepository 仓储接口（TD.md 4.2）：节点与边的 CRUD 及图查询。
type CodeRepository interface {
	SaveNode(node *CodeEntity) error
	SaveEdges(edges []*Fact) error
	DeleteByFile(filePath string) error
	GetSymbol(id CanonicalID) (*CodeEntity, error)
	GetCallers(id CanonicalID, depth int, minConfidence float64) ([]*Fact, error)
	GetCallees(id CanonicalID, depth int, minConfidence float64) ([]*Fact, error)
	GetImpact(id CanonicalID, depth int) ([]*CodeEntity, error)
	Counts() (nodes int, edges int, err error)
}

// BuildMetadataRepository 构建元数据仓储。
type BuildMetadataRepository interface {
	Save(meta *BuildMeta) error
	GetLatest() (*BuildMeta, error)
}
