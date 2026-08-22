package action

import (
	"github.com/schaepher/codeintel/internal/domain"
	"go.uber.org/zap"
)

// Q235-5 query context：一次调用拿全链上下文（借鉴 GitNexus context
// 聚合查询——预计算关系智能，替代多次查询链；本实现为查询编排：
// 复用现有 repo/action 查询，无新增图逻辑）。MCP 地基——transport
// 解耦，未来 MCP 暴露直接复用本 action。子查询失败部分降级（字段
// null，不整体失败）；主字段（symbol）失败整体失败。

// CodeContext 节点全链上下文。
type CodeContext struct {
	Symbol   *domain.CodeEntity                `json:"symbol"`
	Callers  []*domain.Fact                    `json:"callers,omitempty"`  // 调用者（depth 1）
	Callees  []*domain.Fact                    `json:"callees,omitempty"`  // 被调用者（depth 1）
	Fields   *ContextFields                    `json:"fields,omitempty"`   // 函数字段读写摘要
	Chain    []SummaryStep                     `json:"chain,omitempty"`    // 生命周期主链（带条件标注）
	Traces   []*domain.TraceRow                `json:"traces,omitempty"`   // 值流全链（depth 4）
	Dispatch []*domain.Fact                    `json:"dispatch,omitempty"` // 动态派发候选（接口节点）
}

// ContextFields 字段摘要按访问类型分组。
type ContextFields struct {
	DirectRead    []*domain.FunctionFieldSummary `json:"direct_read,omitempty"`
	DirectWrite   []*domain.FunctionFieldSummary `json:"direct_write,omitempty"`
	IndirectWrite []*domain.FunctionFieldSummary `json:"indirect_write,omitempty"`
}

// Context 聚合查询：解析锚点（canonical ID / 符号名）→ 并行取各子
// 查询 → 部分失败降级。traces 深度固定 4（上下文摘要级，防输出过载；
// 深链用 query value-trace）。
func (a *Actions) Context(input string) (*CodeContext, error) {
	logger := zap.L()
	logger.Info("enter (Actions).Context", zap.String("input", input))
	defer logger.Info("exit (Actions).Context")
	id, err := a.ResolveAnchor(input)
	if err != nil {
		return nil, err
	}
	n, err := a.Symbol(id)
	if err != nil {
		return nil, err
	}
	ctx := &CodeContext{Symbol: n}
	if fs, err := a.Callers(id, 1); err == nil {
		ctx.Callers = fs
	}
	if fs, err := a.Callees(id, 1); err == nil {
		ctx.Callees = fs
	}
	if n.Kind == domain.KindFunction || n.Kind == domain.KindMethod {
		if _, sums, err := a.FunctionFields(string(id)); err == nil {
			ctx.Fields = groupFields(sums)
		}
	}
	if steps, err := a.SummaryChain(id); err == nil {
		ctx.Chain = steps
	}
	if rows, err := a.ValueTrace(id, 4, 0, false); err == nil {
		ctx.Traces = rows
	}
	if n.Kind == domain.KindInterface {
		if ds, err := a.DispatchCandidates(id); err == nil {
			ctx.Dispatch = ds
		}
	}
	return ctx, nil
}

// groupFields 字段摘要按访问类型分组（direct_read / direct_write /
// indirect_write）。
func groupFields(sums []*domain.FunctionFieldSummary) *ContextFields {
	out := &ContextFields{}
	for _, s := range sums {
		switch s.AccessKind {
		case domain.SummaryDirectRead:
			out.DirectRead = append(out.DirectRead, s)
		case domain.SummaryDirectWrite:
			out.DirectWrite = append(out.DirectWrite, s)
		case domain.SummaryIndirectWrite:
			out.IndirectWrite = append(out.IndirectWrite, s)
		}
	}
	return out
}
