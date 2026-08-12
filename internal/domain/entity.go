// Package domain 承载核心领域模型：CodeEntity（聚合根）、Fact（实体）、
// CanonicalID（值对象），遵循 docs/TD.md 第 3 章定义。
package domain

import "errors"

import (
	"go.uber.org/zap"
)

// ErrNotFound 查询不到记录时的哨兵错误。
var ErrNotFound = errors.New("not found")

// EntityKind 代码实体种类，对应 nodes.kind 列。
type EntityKind string

const (
	KindFile      EntityKind = "file"
	KindPackage   EntityKind = "package"
	KindFunction  EntityKind = "function"
	KindMethod    EntityKind = "method"
	KindStruct    EntityKind = "struct"
	KindInterface EntityKind = "interface"
	KindCommit    EntityKind = "commit"
	KindObject    EntityKind = "object" // struct 实例化产生的对象
)

// FactKind 事实（关系）种类，对应 edges.kind 列。
type FactKind string

const (
	FactCalls       FactKind = "calls"
	FactImports     FactKind = "imports"
	FactDependsOn   FactKind = "depends_on"
	FactImplements  FactKind = "implements"
	FactModifiedBy  FactKind = "modified_by"
	FactReferences  FactKind = "references"
	FactDataFlowsTo FactKind = "data_flows_to"
	FactTests       FactKind = "tests"
	FactInitializes FactKind = "initializes" // struct 实例化（&T{} / T{} / new(T)）
	FactUses        FactKind = "uses"        // 对象的方法被调用（使用处）
	FactPassesTo    FactKind = "passes_to"   // 对象被传给其他函数（去处）
	FactOfType      FactKind = "of_type"      // 对象 → 其 struct 类型
	FactHasReceiver FactKind = "has_receiver" // 方法 → 其 receiver 类型
)

// 工具来源标识，对应 edges.tool_source 列。
const (
	ToolSCIP      = "scip"      // 符号与引用，置信度 1.0
	ToolCodeGraph = "codegraph" // 调用图与依赖图，置信度 0.8
	ToolGit       = "git"       // Git 历史，置信度 1.0
	ToolJoern     = "joern"     // 数据流（MVP 未接入）
)

// CanonicalID 是 Code Entity 的内部唯一标识（值对象）。
// 格式：symbol:go:<import_path>:<name>，方法名含接收者标识。
type CanonicalID string

// CodeEntity 聚合根：代码库中唯一可标识的概念（函数、结构体、文件、包等）。
type CodeEntity struct {
	ID        CanonicalID
	Kind      EntityKind
	Name      string
	FilePath  string // 仓库相对路径
	LineStart int
	LineEnd   int
	// Properties 自由属性：signature / doc_comment / llm_summary 等
	Properties map[string]any
}

// Property 读取 properties 中的字符串字段。
func (e *CodeEntity) Property(key string) string {
	logger := zap.L()
	logger.Debug("enter (CodeEntity).Property")
	defer logger.Debug("exit (CodeEntity).Property")
	if e.Properties == nil {
		return ""
	}
	if v, ok := e.Properties[key].(string); ok {
		return v
	}
	return ""
}

// Signature 返回符号签名（如 "func (s *Service) CreatePayment(req Request) error"）。
func (e *CodeEntity) Signature() string {
	logger := zap.L()
	logger.Debug("enter (CodeEntity).Signature")
	defer logger.Debug("exit (CodeEntity).Signature")
	return e.Property("signature")
}

// DocComment 返回符号文档注释。
func (e *CodeEntity) DocComment() string {
	logger := zap.L()
	logger.Debug("enter (CodeEntity).DocComment")
	defer logger.Debug("exit (CodeEntity).DocComment")
	return e.Property("doc_comment")
}

// Fact 实体：连接两个 Code Entity 的关系，唯一性由 (source, target, kind) 决定。
type Fact struct {
	SourceID   CanonicalID
	TargetID   CanonicalID
	Kind       FactKind
	ToolSource string
	Confidence float64 // 0.0~1.0
	Metadata   map[string]any
}

// BuildMeta 构建元数据（build_metadata 表），status 三态：success/degraded/failed。
type BuildMeta struct {
	BuildID    string
	CommitSHA  string
	ToolName   string
	Status     string
	DurationMs int64
	ErrorMsg   string
}

// BuildStatus 常量
const (
	BuildSuccess  = "success"
	BuildDegraded = "degraded"
	BuildFailed   = "failed"
)

// Repository 描述被索引的代码仓库。
type Repository struct {
	Path   string // 绝对路径
	Module string // go.mod 中的 module 路径
}
