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
	ServiceID  CanonicalID // grpc_service 节点
	Service    string      // 生成包路径 + 服务名（如 example.com/app/pb.Greeter）
	Method     string      // 客户端调用的方法名
	Line       int
	ImplTypeID CanonicalID // 服务端实现类型（grpc_impl 边 source；无实现时空）
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
