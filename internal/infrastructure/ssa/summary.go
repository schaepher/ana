// 函数字段摘要预计算（field_trace.md §6.2 / §5.2）：
//   - direct_read / direct_write：函数内字段访问节点直接收集
//   - indirect_write：沿静态调用图闭包——被调函数写字段的声明结构体类型
//     与调用点实参类型匹配（Q36 近似：无指针别名分析，类型级匹配）
//   - INDIRECT_WRITE 边：调用者函数 → 被调函数（匹配写存在时）
package ssa

import (
	"github.com/schaepher/codeintel/internal/domain"
	"go.uber.org/zap"
)

// emitSummaries 计算并发射全部函数的 function_field_summary 行与 INDIRECT_WRITE 边。
func emitSummaries(data map[domain.CanonicalID]*funcData, emit domain.EmitFunc) error {
	logger := zap.L()
	logger.Debug("enter emitSummaries")
	defer logger.Debug("exit emitSummaries")
	// 间接写闭包：迭代至稳定（有向调用图，最坏 O(V·E)）
	indirect := map[domain.CanonicalID]map[string]fieldEntry{}
	for id := range data {
		indirect[id] = map[string]fieldEntry{}
	}
	changed := true
	for changed {
		changed = false
		for fID, fd := range data {
			for _, c := range fd.calls {
				for _, e := range calleeWrites(data, indirect, c.calleeID) {
					if _, ok := indirect[fID][e.fieldPath]; ok {
						continue
					}
					if !contains(c.argStructPaths, structPathOf(e.fieldPath)) {
						continue
					}
					indirect[fID][e.fieldPath] = e
					changed = true
				}
			}
		}
	}

	// 发射摘要行与 INDIRECT_WRITE 边
	for fID, fd := range data {
		if err := emitSummaryRows(fID, domain.SummaryDirectRead, fd.directReads, emit); err != nil {
			return err
		}
		if err := emitSummaryRows(fID, domain.SummaryDirectWrite, fd.directWrites, emit); err != nil {
			return err
		}
		ind := indirect[fID]
		// 合并外部摘要的间接写（虚拟节点）
		if fd != nil {
			for _, e := range fd.indirectWrites {
				if _, ok := ind[e.fieldPath]; !ok {
					ind[e.fieldPath] = e
				}
			}
		}
		if err := emitSummaryRows(fID, domain.SummaryIndirectWrite, valuesOf(ind), emit); err != nil {
			return err
		}
		// INDIRECT_WRITE 边：f → g（本次调用存在匹配写）
		for _, c := range fd.calls {
			if calleeHasMatchingWrite(data, indirect, c.calleeID, c.argStructPaths) {
				if err := emit(domain.Item{Fact: &domain.Fact{
					SourceID:   fID,
					TargetID:   c.calleeID,
					Kind:       domain.FactIndirectWrite,
					ToolSource: domain.ToolSSA,
					Confidence: 1.0,
				}}); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// emitSummaryRows 发射单个 access_kind 的摘要行（同字段路径去重，取首条）。
func emitSummaryRows(funcID domain.CanonicalID, accessKind string, entries []fieldEntry,
	emit domain.EmitFunc) error {
	logger := zap.L()
	logger.Debug("enter emitSummaryRows")
	defer logger.Debug("exit emitSummaryRows")
	seen := map[string]bool{}
	for _, e := range entries {
		if seen[e.fieldPath] {
			continue
		}
		seen[e.fieldPath] = true
		if err := emit(domain.Item{Summary: &domain.FunctionFieldSummary{
			FunctionID:   funcID,
			AccessKind:   accessKind,
			FieldPath:    e.fieldPath,
			InstancePath: e.instancePath,
			LineStart:    e.line,
			CodeSnippet:  e.snippet,
		}}); err != nil {
			return err
		}
	}
	return nil
}

// calleeWrites 返回被调函数的全部写条目（direct + indirect）。
func calleeWrites(data map[domain.CanonicalID]*funcData,
	indirect map[domain.CanonicalID]map[string]fieldEntry, gID domain.CanonicalID) []fieldEntry {
	logger := zap.L()
	logger.Debug("enter calleeWrites")
	defer logger.Debug("exit calleeWrites")
	g := data[gID]
	var out []fieldEntry
	if g != nil {
		out = append(out, g.directWrites...)
	}
	out = append(out, valuesOf(indirect[gID])...)
	return out
}

// calleeHasMatchingWrite 判断被调函数是否存在与实参类型匹配的写条目。
func calleeHasMatchingWrite(data map[domain.CanonicalID]*funcData,
	indirect map[domain.CanonicalID]map[string]fieldEntry, gID domain.CanonicalID,
	argStructPaths []string) bool {
	for _, e := range calleeWrites(data, indirect, gID) {
		if contains(argStructPaths, structPathOf(e.fieldPath)) {
			return true
		}
	}
	return false
}

// valuesOf 取 map 的 value 列表。
func valuesOf(m map[string]fieldEntry) []fieldEntry {
	out := make([]fieldEntry, 0, len(m))
	for _, e := range m {
		out = append(out, e)
	}
	return out
}

// contains 判断字符串切片是否包含目标。
func contains(list []string, target string) bool {
	for _, s := range list {
		if s == target {
			return true
		}
	}
	return false
}
