// Package action 是 CLI 与 HTTP 共享的查询用例层（应用层）：
// 命令行和 HTTP 接口本身只负责参数解析与结果展示，全部图查询
// 经此层调用仓储。依赖方向：action → Reader 窄接口 ← *sqlite.Repo。
package action

import (
	"errors"
	"fmt"
	"strings"

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
	TraceForward(field string, funcID domain.CanonicalID, maxDepth int) ([]*domain.TraceRow, error)
	GetValueTrace(nodeID domain.CanonicalID, maxDepth int) ([]*domain.TraceRow, error)
	GetFunctionFlows(funcID domain.CanonicalID, maxDepth int) ([]*domain.TraceRow, error)
	GetRoots() ([]*domain.CodeEntity, error)
	Expand(id domain.CanonicalID) (facts []*domain.Fact, nodes []*domain.CodeEntity, err error)
	AllSummaries() ([]*domain.FunctionFieldSummary, error)
	GetIndirectWriteEdges(funcID domain.CanonicalID) ([]*domain.Fact, error)
	Counts() (nodes int, edges int, err error)
	GetLatest() (*domain.BuildMeta, error)
}

// Actions 是 CLI 与 HTTP 共享的查询用例集合。
type Actions struct {
	repo Reader
}

// New 创建 Actions。
func New(repo Reader) *Actions {
	return &Actions{repo: repo}
}

// ResolveSymbol 将用户输入解析为符号：canonical ID 直接命中，否则按名称查找；
// 多匹配时返回错误并列出候选 ID（原 CLI 语义，供符号类 action 复用）。
func (a *Actions) ResolveSymbol(input string) (*domain.CodeEntity, error) {
	if strings.HasPrefix(input, "symbol:") || strings.HasPrefix(input, "file:") || strings.HasPrefix(input, "commit:") {
		n, err := a.repo.GetSymbol(domain.CanonicalID(input))
		if err == nil {
			return n, nil
		}
		if !errors.Is(err, domain.ErrNotFound) {
			return nil, err
		}
	}
	matches, err := a.repo.GetSymbolByName(input)
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("符号 %q 不存在", input)
	}
	if len(matches) > 1 {
		return nil, fmt.Errorf("符号 %q 有 %d 个匹配，请使用 canonical ID:\n  %s",
			input, len(matches), joinIDs(matches))
	}
	return matches[0], nil
}

// joinIDs 拼接候选 ID（多匹配错误提示用）。
func joinIDs(nodes []*domain.CodeEntity) string {
	ids := make([]string, 0, len(nodes))
	for _, n := range nodes {
		ids = append(ids, string(n.ID))
	}
	return strings.Join(ids, "\n  ")
}

// Symbol 按 canonical ID 查询符号（HTTP expand 的存在性检查用）。
func (a *Actions) Symbol(id domain.CanonicalID) (*domain.CodeEntity, error) {
	return a.repo.GetSymbol(id)
}

// SymbolDetail 符号详情：基本信息 + 调用者/被调用者摘要（query symbol）。
type SymbolDetail struct {
	Node    *domain.CodeEntity
	Callers []*domain.Fact
	Callees []*domain.Fact
}

// SymbolDetail 解析符号并返回其详情（调用者/被调用者深度 1）。
func (a *Actions) SymbolDetail(input string) (*SymbolDetail, error) {
	n, err := a.ResolveSymbol(input)
	if err != nil {
		return nil, err
	}
	callers, err := a.repo.GetCallers(n.ID, 1, MinConfidence)
	if err != nil {
		return nil, err
	}
	callees, err := a.repo.GetCallees(n.ID, 1, MinConfidence)
	if err != nil {
		return nil, err
	}
	return &SymbolDetail{Node: n, Callers: callers, Callees: callees}, nil
}

// Callers 返回调用 id 的边（深度 ≤ depth，置信度 ≥ MinConfidence）。
func (a *Actions) Callers(id domain.CanonicalID, depth int) ([]*domain.Fact, error) {
	return a.repo.GetCallers(id, depth, MinConfidence)
}

// Callees 返回 id 调用的边（深度 ≤ depth）。
func (a *Actions) Callees(id domain.CanonicalID, depth int) ([]*domain.Fact, error) {
	return a.repo.GetCallees(id, depth, MinConfidence)
}

// Impact 返回变更影响范围（深度 ≤ depth）。
func (a *Actions) Impact(id domain.CanonicalID, depth int) ([]*domain.CodeEntity, error) {
	return a.repo.GetImpact(id, depth)
}

// FunctionFields 解析函数并返回其字段读写摘要（S1，field_trace.md §6.2）。
func (a *Actions) FunctionFields(input string) (*domain.CodeEntity, []*domain.FunctionFieldSummary, error) {
	n, err := a.ResolveSymbol(input)
	if err != nil {
		return nil, nil, err
	}
	rows, err := a.repo.GetFunctionFields(n.ID)
	if err != nil {
		return nil, nil, err
	}
	return n, rows, nil
}

// TraceParams 字段追溯参数（S2/S3）。
type TraceParams struct {
	Field    string
	Func     string // 函数符号输入（canonical ID 或名称）
	MaxDepth int
	Forward  bool // true=trace-forward（S3 后续使用），false=trace-backward（S2 产生点）
}

// Trace 字段产生点反向追溯 / 后续使用正向追踪；返回解析后的函数符号
// 与追溯路径（符号供展示层输出函数名）。
func (a *Actions) Trace(p TraceParams) (*domain.CodeEntity, []*domain.TraceRow, error) {
	n, err := a.ResolveSymbol(p.Func)
	if err != nil {
		return nil, nil, err
	}
	if p.Forward {
		rows, err := a.repo.TraceForward(p.Field, n.ID, p.MaxDepth)
		return n, rows, err
	}
	rows, err := a.repo.TraceBackward(p.Field, n.ID, p.MaxDepth)
	return n, rows, err
}

// ValueTrace 数据值全链追踪（field_trace.md §14.2）。
func (a *Actions) ValueTrace(nodeID domain.CanonicalID, maxDepth int) ([]*domain.TraceRow, error) {
	return a.repo.GetValueTrace(nodeID, maxDepth)
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
func (a *Actions) Flows(funcID domain.CanonicalID, maxDepth int) ([]*domain.TraceRow, error) {
	return a.repo.GetFunctionFlows(funcID, maxDepth)
}

// ExportField 双层索引中的一个字段条目（S4，field_trace.md §2）。
type ExportField struct {
	Producers []ExportEntry `json:"producers"`
	Consumers []ExportEntry `json:"consumers"`
}

// ExportEntry 单个产生者/消费者条目。
type ExportEntry struct {
	Function string `json:"function"`
	Access   string `json:"access,omitempty"` // producers 的写类型（direct/indirect）
	Line     int    `json:"line"`
	Instance string `json:"instance"`
	Code     string `json:"code"`
}

// Counts 返回节点数与边数（构建健康检查，serve 启动校验用）。
func (a *Actions) Counts() (nodes, edges int, err error) {
	return a.repo.Counts()
}

// Latest 返回最近一次构建元数据（serve 启动校验用）。
func (a *Actions) Latest() (*domain.BuildMeta, error) {
	return a.repo.GetLatest()
}

// IndirectWriteSites 返回函数的 INDIRECT_WRITE 边（Q90 调用点回连：
// metadata 含 call_line / call_args，fields 展示用）。
func (a *Actions) IndirectWriteSites(funcID domain.CanonicalID) ([]*domain.Fact, error) {
	return a.repo.GetIndirectWriteEdges(funcID)
}

// ExportIndex 生成 字段 → 产生者/消费者 的双层索引（S4）。
// direct_read 为消费者；direct_write / indirect_write 均为产生者。
func (a *Actions) ExportIndex() (map[string]*ExportField, error) {
	rows, err := a.repo.AllSummaries()
	if err != nil {
		return nil, err
	}
	index := map[string]*ExportField{}
	for _, s := range rows {
		ef := index[s.FieldPath]
		if ef == nil {
			ef = &ExportField{Producers: []ExportEntry{}, Consumers: []ExportEntry{}}
			index[s.FieldPath] = ef
		}
		entry := ExportEntry{
			Function: string(s.FunctionID),
			Line:     s.LineStart,
			Instance: s.InstancePath,
			Code:     s.CodeSnippet,
		}
		switch s.AccessKind {
		case domain.SummaryDirectRead:
			ef.Consumers = append(ef.Consumers, entry)
		default: // direct_write / indirect_write 均为产生者
			entry.Access = s.AccessKind
			ef.Producers = append(ef.Producers, entry)
		}
	}
	return index, nil
}
