// 函数字段摘要预计算（field_trace.md §6.2 / §5.2）：
//   - direct_read / direct_write：函数内字段访问节点直接收集
//   - indirect_write：沿静态调用图闭包——被调函数写字段的声明结构体类型
//     与调用点实参类型匹配（Q36 近似：无指针别名分析，类型级匹配）
//   - INDIRECT_WRITE 边：调用者函数 → 被调函数（匹配写存在时）
package ssa

import (
	"strings"
	"time"

	"github.com/schaepher/codeintel/internal/domain"
	"go.uber.org/zap"
)

// emitSummaries 计算并发射全部函数的 function_field_summary 行与 INDIRECT_WRITE 边。
// indirectKey 间接写条目键（Q157）：字段 × 调用点粒度——同字段多处
// 调用点各自保留 callLine/callArg（此前按字段去重复用首次保存的
// 调用点，INDIRECT_WRITE 边回连错位）。
type indirectKey struct {
	fieldPath string
	callLine  int
}

// excluded（Q80 别名分析）：确认无别名的间接写候选，迭代时跳过。
func emitSummaries(data map[domain.CanonicalID]*funcData, alias *aliasResult, emit domain.EmitFunc) error {
	logger := zap.L()
	logger.Debug("enter emitSummaries")
	defer logger.Debug("exit emitSummaries")
	// 间接写闭包：迭代至稳定（有向调用图，最坏 O(V·E)）
	indirect := map[domain.CanonicalID]map[indirectKey]fieldEntry{}
	for id := range data {
		indirect[id] = map[indirectKey]fieldEntry{}
	}
	changed := true
	// Q166：迭代轮次打点——间接写闭包每轮全图遍历，轮数多时定位
	roundStart := time.Now()
	addedTotal := 0
	for round := 1; changed; round++ {
		changed = false
		added := 0
		for fID, fd := range data {
			for _, c := range fd.calls {
				for _, e := range calleeWrites(data, indirect, c.calleeID) {
					key := indirectKey{e.fieldPath, c.callLine}
					if _, ok := indirect[fID][key]; ok {
						continue
					}
					// 别名排除（Q80）：确认该调用点无别名 → 不算间接写
					if alias != nil && alias.excluded[fID][c.calleeID][e.fieldPath] {
						continue
					}
					if !contains(c.argStructPaths, structPathOf(e.fieldPath)) {
						continue
					}
					// 调用点级回连（Q90/Q157）：fID 视角的调用点 = fID 直接
					// 调用该 callee 的位置与实参（多层闭包传播时覆盖为当前层；
					// 每 (字段, 调用点) 一条——边发射按边取回连）
					e.callLine = c.callLine
					e.callArg = strings.Join(c.argNames, ", ")
					indirect[fID][key] = e
					changed = true
					added++
				}
			}
		}
		addedTotal += added
		logger.Info("indirect round",
			zap.Int("round", round), zap.Int("added", added),
			zap.Duration("elapsed", time.Since(roundStart)))
		roundStart = time.Now()
	}
	logger.Debug("indirect settled", zap.Int("total", addedTotal))

	// 发射摘要行与 INDIRECT_WRITE 边
	for fID, fd := range data {
		if err := emitSummaryRows(fID, domain.SummaryDirectRead, fd.directReads, emit); err != nil {
			return err
		}
		if err := emitSummaryRows(fID, domain.SummaryDirectWrite, fd.directWrites, emit); err != nil {
			return err
		}
		ind := indirect[fID]
		// 合并外部摘要的间接写（虚拟节点，无调用点 → callLine=0）
		if fd != nil {
			for _, e := range fd.indirectWrites {
				key := indirectKey{e.fieldPath, 0}
				if _, ok := ind[key]; !ok {
					ind[key] = e
				}
			}
		}
		if err := emitSummaryRows(fID, domain.SummaryIndirectWrite, valuesOf(ind), emit); err != nil {
			return err
		}
		// INDIRECT_WRITE 边：f → g（本次调用存在匹配写），metadata 携带
		// 调用点（Q90/Q157 回连：行号 + 实参变量名，边 × 字段粒度——每条
		// 边取该调用点自己的 indirect 条目，不再回读按字段去重后的首条）
		for _, c := range fd.calls {
			if calleeHasMatchingWrite(data, indirect, c.calleeID, c.argStructPaths) {
				meta := map[string]any{}
				// Q161：origins 聚合——每个匹配写（字段 × 调用点 × 被调
				// 函数）记一条来源；摘要行仍按字段去重，来源不折叠
				// Q163：不 break——被调函数写多个匹配字段（Fee/SettledFee/
				// Tax）时每个字段都记 origins（此前只记第一个，其余字段
				// 的间接写来源为空）；meta 的调用点信息只设一次（首个字段）
				var origins []*domain.SummaryOrigin
				for _, e := range calleeWrites(data, indirect, c.calleeID) {
					if !contains(c.argStructPaths, structPathOf(e.fieldPath)) {
						continue
					}
					got, ok := indirect[fID][indirectKey{e.fieldPath, c.callLine}]
					if !ok {
						continue
					}
					if got.callLine > 0 {
						if _, set := meta["call_line"]; !set {
							meta["call_line"] = got.callLine
						}
					}
					if got.callArg != "" {
						if _, set := meta["call_args"]; !set {
							meta["call_args"] = got.callArg
						}
					}
					origins = append(origins, &domain.SummaryOrigin{
						FunctionID: fID,
						AccessKind: domain.SummaryIndirectWrite,
						FieldPath:  e.fieldPath,
						CallLine:   got.callLine,
						CalleeID:   c.calleeID,
					})
				}
				if len(origins) > 0 {
					if err := emit(domain.Item{Origins: origins}); err != nil {
						return err
					}
				}
				if err := emit(domain.Item{Fact: &domain.Fact{
					SourceID:   fID,
					TargetID:   c.calleeID,
					Kind:       domain.FactIndirectWrite,
					ToolSource: domain.ToolSSA,
					Confidence: 1.0,
					Metadata:   meta,
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
	indirect map[domain.CanonicalID]map[indirectKey]fieldEntry, gID domain.CanonicalID) []fieldEntry {
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
	indirect map[domain.CanonicalID]map[indirectKey]fieldEntry, gID domain.CanonicalID,
	argStructPaths []string) bool {
	for _, e := range calleeWrites(data, indirect, gID) {
		if contains(argStructPaths, structPathOf(e.fieldPath)) {
			return true
		}
	}
	return false
}

// valuesOf 取 map 的 value 列表。
func valuesOf[K comparable](m map[K]fieldEntry) []fieldEntry {
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
