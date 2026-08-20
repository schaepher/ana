// 轻量别名分析（field_trace.md §14.8，Q80）：
//   - 过程内：值 → 指向的 alloc 集合（may 传播，SSA def-use 链）
//   - 跨函数：实参→形参、返回值→调用者，沿调用图迭代至稳定（Q11 双向）
//   - 间接写排除集：调用点实参 may 集与被调写字段 base may 集无交集
//     （或实参集为空）→ 确认不别名 → 间接写判定排除（Q4/Q5：别名优先，
//     排除"确定不别名"，其余走类型级 fallback）
//   - ALIAS 边：may 关系（值 → alloc）落库（conf 0.8，Q3/Q6 锚点式）
//   - 上限：每函数 200 alloc（Q10），超限跳过该函数（不排除不产边）
package ssa

import (
	"go/token"

	"github.com/schaepher/codeintel/internal/domain"
	"golang.org/x/tools/go/ssa"
)

// MaxAllocsPerFunc 每函数参与别名分析的 alloc 上限（Q10）。
const MaxAllocsPerFunc = 200

// aliasResult 别名分析结果。
type aliasResult struct {
	// excluded[callerID][calleeID][fieldPath]：确认无别名的间接写候选
	excluded map[domain.CanonicalID]map[domain.CanonicalID]map[string]bool
}

// callSite 静态调用点（跨函数传播单元）。
type callSite struct {
	caller  *ssa.Function
	callee  *ssa.Function
	cc      *ssa.CallCommon
	callVal ssa.Value // *ssa.Call 指令（returns 传播目标）；Go/Defer 为 nil
}

// aliasPass 别名分析状态。
type aliasPass struct {
	repo    *domain.Repository
	prog    *ssa.Program
	idents  map[token.Pos]string
	emit    domain.EmitFunc
	funcIDs map[*ssa.Function]domain.CanonicalID
	// may[funcID]：函数内 值 → alloc 集（含跨函数注入）
	may         map[domain.CanonicalID]map[ssa.Value]map[ssa.Value]bool
	allocIDs    map[ssa.Value]domain.CanonicalID
	paramMay    map[*ssa.Parameter]map[ssa.Value]bool
	callMay     map[ssa.Value]map[ssa.Value]bool
	excluded    map[domain.CanonicalID]map[domain.CanonicalID]map[string]bool
	fieldValues map[domain.CanonicalID]map[ssa.Value]bool // 参与字段访问的值（alias 边范围）
	slotSeen    map[domain.CanonicalID]map[string]bool
	funcData    map[domain.CanonicalID]*funcData    // 元素间接写条目（Q83）
	lines       map[string][]string                 // 源码行缓存（fieldInfoFor 用）
	calleeInfo  map[*ssa.Function]*calleeWritesInfo // 被调函数写指令缓存（processCall 复用）
	rets        map[*ssa.Function][][]ssa.Value     // 被调函数 Return 指令缓存（returns 传播复用）
}

// calleeWritesInfo 被调函数的写指令静态信息（每个被调函数只扫描一次，
// 多个调用点共享——避免 O(调用点×被调函数大小) 的重复遍历）。
type calleeWritesInfo struct {
	faBase map[string]ssa.Value // 字段写：fieldPath → base
	writes []writeInstr         // 元素写：map/slice/channel
}

// writeInstr 元素写指令的静态信息（path 已含元素记号）。
type writeInstr struct {
	container ssa.Value
	path      string
	pos       token.Pos
}

// overlapMay 判断两个 may 集是否有交集。
func overlapMay(a, b map[ssa.Value]bool) bool {
	for x := range a {
		if b[x] {
			return true
		}
	}
	return false
}
