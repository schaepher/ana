package domain

import "context"

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
	SummaryDirectRead  = "direct_read"
	SummaryDirectWrite = "direct_write"
	SummaryIndirectWrite = "indirect_write"
)

// EmitFunc 将适配器产出的数据流式交给 Canonicalizer 消费。
// 返回错误时适配器应停止产出。
type EmitFunc func(Item) error

// IndexerPort 六边形架构端口：所有外部分析工具（SCIP/CodeGraph/Git 等）
// 通过该端口接入，核心领域不依赖具体实现。
type IndexerPort interface {
	Name() string
	Index(ctx context.Context, repo *Repository, emit EmitFunc) error
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
