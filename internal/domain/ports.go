package domain

import (
	"context"

	"golang.org/x/tools/go/packages"
)

// Item 是适配器流式产出的原始数据单元：节点 / 边 / 函数字段摘要行。
type Item struct {
	Node    *CodeEntity
	Fact    *Fact
	Summary *FunctionFieldSummary
	Origins []*SummaryOrigin // Q161 摘要来源（indirect_write 多分支）
}

// SummaryOrigin 摘要来源（Q161）：某字段 indirect_write 的一个来源
// （调用点行号 + 被调函数）；落库三键 function_id/access_kind/field_path
// 与摘要行配套；origin/confidence 查询期从 dispatch_to 边 join（callee
// 是候选实现时带出 register/enum + 置信度）。
type SummaryOrigin struct {
	FunctionID CanonicalID
	AccessKind string
	FieldPath  string
	CallLine   int
	CalleeID   CanonicalID
	Origin     string // register / enum（动态候选时，查询期填充）
	Confidence float64
}

// FunctionFieldSummary 函数字段摘要行（function_field_summary 表，
// 构建时预计算，加速 S1 查询，field_trace.md §5.2）。
type FunctionFieldSummary struct {
	FunctionID  CanonicalID
	AccessKind  string // direct_read / direct_write / indirect_write
	FieldPath   string // 类型限定路径（同 field_access.full_path）
	InstancePath string
	LineStart   int
	CodeSnippet string
	Origins     []*SummaryOrigin // Q161 间接写多来源（查询期填充）
}

// 摘要 access_kind 常量。
const (
	SummaryDirectRead    = "direct_read"
	SummaryDirectWrite   = "direct_write"
	SummaryIndirectWrite = "indirect_write"
)

// TraceRow 字段追溯路径上的一步（S2/S3，field_trace.md §6.3/6.4）。
type TraceRow struct {
	ID        CanonicalID
	Depth     int
	Name      string
	EdgeKinds string // 到达该节点经过的边类型（逗号连接）
	Line      int
	IsUsage   bool // S3：该节点是否为匹配 full_path 的使用点
	Dir       int  // 函数内数据流方向（GetFunctionFlows）：0=产生链（反向），1=使用链（正向）
	Kind      EntityKind
	Access    string // field_access 的 read/write
	FuncID    string // 所属函数 canonical ID（GetValueTrace 函数上下文分组用）
	FullPath  string // field_access 的类型限定路径（前端展开匹配用）
	Conditions []string // 路径条件标注（Q92 查询期计算，不落库）
	DispatchCandidate bool    // 该行所属函数是接口候选实现（Q157 P1）
	DispatchOrigin    string  // 候选来源（register / enum）
	DispatchConf      float64 // 候选置信度
	EdgeIface         string  // 到达该行的边是动态候选边（Q161）：接口类型
	EdgeOrigin        string  // 候选来源（register / enum）
	EdgeConf          float64 // 候选置信度（--min-conf 剪枝阈值用）
}

// DispatchMeta 接口派发元数据（Q157 P1：value-trace 候选标注用）。
type DispatchMeta struct {
	Origin     string  // register / enum
	Confidence float64
}

// UnusedFunc 未调用分析中的一个函数（field_trace.md §16）。
type UnusedFunc struct {
	ID         CanonicalID
	Kind       EntityKind
	Name       string
	FilePath   string
	LineStart  int
	LineEnd    int
	Exported   bool   // 首字母大写（可能被外部模块调用）
	Called     bool   // 有 calls / passes_result 入边
	Referenced bool   // 有 passes_to / dispatch_to / initializes / var 初始化引用
	SinceMark  string // --since 标注："" / "new"（声明行新增）/ "mod"（行号区间新增）
}

// GrpcCallRow 模块间调用原始行（field_trace.md §18.3）：grpc_call 边 +
// 服务端实现归属（grpc_impl 边反向查）。
type GrpcCallRow struct {
	CallerID   CanonicalID // 客户端调用方函数
	ServiceID  CanonicalID // grpc_service / http_route 节点
	Service    string      // grpc：生成包路径+服务名；http：route 名
	Method     string      // grpc：方法名；http：路径
	Line       int
	ImplTypeID CanonicalID // 服务端实现（grpc_impl 边 source / route.handler_id；无实现时空）
	Transport  string      // grpc_call / http_call
}

// TableEndpoint 表列数据流的端点（写入方/读取方，query table）。
type TableEndpoint struct {
	FuncID   string // 函数 canonical ID（summary_io 边 source 的值节点所属函数）
	FuncName string // 函数短名（从 ID 提取）
	Line     int    // 调用行号
}

// TableColumn 表的一列虚拟节点及数据流（query table）。
type TableColumn struct {
	Name      string // 表.列（无列时为表名）
	Access    string // read / write
	LineStart int
	Writers   []TableEndpoint // summary_io 入边（值 → 虚拟节点）：谁写入该列
	Readers   []TableEndpoint // 虚拟节点出边（消费）；SELECT 读路径未解析时为空
}

// 表关联类型（关联终点虚拟节点的 access_kind 判定）：
const (
	RelationQuery = "query" // 终点是 WHERE 过滤列（filter）——A 的值作为 B 的查询条件（键关联，高置信）
	RelationWrite = "write" // 终点是写入列——同源/间接写入（值相关，中置信）
	RelationRead  = "read"  // 终点是读出列——间接扩散（低置信）
)

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

// SinceInfo --since <ref> 的 diff 解析结果（field_trace.md §16.5）。
type SinceInfo struct {
	Ref        string             // git ref（--since 参数）
	NewFiles   map[string]bool    // 新增文件（文件内全部函数标 [new]）
	AddedLines map[string]map[int]bool // 每文件新增行号集合（+ 侧）
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
