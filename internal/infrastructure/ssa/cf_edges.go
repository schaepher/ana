package ssa

import (
	"github.com/schaepher/codeintel/internal/domain"
	"go.uber.org/zap"
)

// emitEdgeKindLine 带行号的边（query table 写入方定位用；SQL/ORM
// 虚拟节点 summary_io 边的 line_num 此前缺失，聚合时只能兜底节点行号）。
func (ext *fieldExtractor) emitEdgeKindLine(from, to domain.CanonicalID, kind domain.FactKind, line int) error {
	logger := zap.L()
	logger.Debug("enter (fieldExtractor).emitEdgeKindLine")
	defer logger.Debug("exit (fieldExtractor).emitEdgeKindLine")
	return ext.emit(domain.Item{Fact: &domain.Fact{
		SourceID:   from,
		TargetID:   to,
		Kind:       kind,
		ToolSource: domain.ToolSSA,
		Confidence: 1.0,
		Metadata:   map[string]any{"line_num": line},
	}})
}

// emitEdgeKind 发射指定 kind 的边（tool_source=ssa，conf 1.0，Q69）。
func (ext *fieldExtractor) emitEdgeKind(from, to domain.CanonicalID, kind domain.FactKind) error {
	logger := zap.L()
	logger.Debug("enter (fieldExtractor).emitEdgeKind")
	defer logger.Debug("exit (fieldExtractor).emitEdgeKind")
	return ext.emit(domain.Item{Fact: &domain.Fact{
		SourceID:   from,
		TargetID:   to,
		Kind:       kind,
		ToolSource: domain.ToolSSA,
		Confidence: 1.0,
	}})
}

// emitEdgeKindMeta 发射带元数据的边（Q161 动态候选边：
// interface/candidate_origin/confidence——value-trace 标注与过滤用）。
func (ext *fieldExtractor) emitEdgeKindMeta(from, to domain.CanonicalID, kind domain.FactKind, meta map[string]any) error {
	logger := zap.L()
	logger.Debug("enter (fieldExtractor).emitEdgeKindMeta")
	defer logger.Debug("exit (fieldExtractor).emitEdgeKindMeta")
	return ext.emit(domain.Item{Fact: &domain.Fact{
		SourceID:   from,
		TargetID:   to,
		Kind:       kind,
		ToolSource: domain.ToolSSA,
		Confidence: 1.0,
		Metadata:   meta,
	}})
}
