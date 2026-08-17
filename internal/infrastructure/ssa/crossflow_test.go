package ssa

import (
	"strings"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

// findFact 按 (source, target, kind) 查找边。
func findFact(t *testing.T, facts []*domain.Fact, source, target, kind string) *domain.Fact {
	t.Helper()
	for _, f := range facts {
		if string(f.SourceID) == source && string(f.TargetID) == target && string(f.Kind) == kind {
			return f
		}
	}
	t.Fatalf("fact not found: %s -> %s [%s]", source, target, kind)
	return nil
}

// findFactByKindPrefix 按 kind 与 source ID 前缀查找边（SSA 临时名 tN 不稳定）。
func findFactByKindPrefix(facts []*domain.Fact, kind domain.FactKind, srcPrefix string) *domain.Fact {
	for _, f := range facts {
		if f.Kind == kind && strings.HasPrefix(string(f.SourceID), srcPrefix) {
			return f
		}
	}
	return nil
}

// findSummary 按（函数, access_kind, field_path）查找摘要行。

// TestIndirectWriteExcludedDeepChain：S1 回归——跨函数参数 may 传播
// （a→b→c 三层）须稳定生效：c 写自己内部对象（与实参无别名）时，
// a 的调用点应被别名排除（无间接写）。此前 mayOfDepth 缓存 paramMay
// 引用，参数首次 nil→新建后缓存失效，传播可能过早停滞（结果依赖
// 调用点处理顺序，不稳定）。

// TestIndirectWriteNestedOwner：Q157——嵌套对象字段传播。实现写
// Order.FinalFee，wrapper 实参是 *OrderModel（含 Order 嵌套字段）——
// 类型匹配须沿嵌套字段展开 owner 链（OrderModel → Order），wrapper
// 的 indirect_write 才能含 Order.FinalFee（现状：只比较实参结构体
// OrderModel 与字段所属 Order，不匹配）。

// TestIndirectWriteCallLinePerCall：Q157——callLine/callArg 按调用点
// 粒度。同一函数两处调用 fill（不同行）写同一字段：INDIRECT_WRITE 边
// 各带自己的 call_line（现状：按字段去重后复用首次保存的调用点，
// 两条边都指向首处调用）。
