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

// computeAliases 执行轻量别名分析，返回间接写排除集（emitSummaries 消费）。

// underLimit 每函数 alloc 上限检查（Q10）。

// collectFieldValues 收集参与字段访问的值（alias 边范围，Q53 精神）。

// mayOf 过程内值 → alloc 集（跨函数注入经 paramMay/callMay）。

// mayOfDepth 递归实现，visiting 防环（phi 环 / 互赋值 `a:=b;b:=a`），
// 深度限制防栈溢出；环截断返回空（保守：excluded 判定对空集走 fallback）。

// clearMayCache 清空函数的 may 缓存（保留顶层条目：mayOfDepth 直接对
// p.may[id][v] 赋值，删条目会让 p.may[id] 变 nil 导致 panic——S1 修复
// 踩过）。nil 安全。

// mergeMay 合并 src 到 dst（dst 为 nil 时创建），返回是否有新增。

// processCall 处理单个调用点：判定排除集并发射 alias 边。

// callArgNames 提取调用点非 const 实参的源码变量名（Q90 回连展示；
// Alloc 从标识符索引恢复，其余回退 SSA 名）。

// writeInfoOf 惰性构建被调函数的写指令静态信息（字段写 faBase + 元素写列表）。
// 语义与原 processCall 内联扫描一致；每个被调函数只扫描一次，
// 多个调用点共享缓存（避免 O(调用点×被调函数大小) 的重复遍历）。

// returnOperandsCached 惰性缓存函数的 Return 指令操作数（returns 传播复用）。

// emitAliasEdges 发射 值 → alloc 的 ALIAS 边（may，conf 0.8）。

// valueNodeID 生成并发射值节点的 canonical ID（funcID#slot，与 emitValue
// 一致：shadowing 同名附加 @行号）。alias 边 source 用（B1：此前
// funcIDOfValue 返回函数 ID，alias 边全部错挂在函数节点上——值节点
// 看不到别名关系）。值节点可能未被 emitValue 发射（如 Field 指令的
// 基值），此处保证端点存在（FK 约束）。

// objectIDOf 确保对象创建点（alloc / MakeMap / MakeSlice）的 ssa_value
// 节点发射（Q7：被别名引用的对象也发射）。

// fieldInfoFor 计算写字段指令的类型限定路径（复用 fieldInfo 语义）。

// sourceLine 读仓库文件指定行的源码（去掉缩进，供 code_snippet 展示）。
// 文件内容按路径缓存，避免每个调用点重复读盘（与 fieldExtractor.sourceLine 一致）。

// funcIDOf 函数 → canonical ID（缓存）。

// elementWritePath 生成元素写路径（间接写条目用，Q5 记号）。

// overlapMay 判断两个 may 集是否有交集。
func overlapMay(a, b map[ssa.Value]bool) bool {
	for x := range a {
		if b[x] {
			return true
		}
	}
	return false
}
