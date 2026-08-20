// 跨过程数据流与摘要收集（field_trace.md §6.2/§7.5）：
//   - argument 边：静态调用实参 → 被调函数形参
//   - returns 边：被调函数返回值 → 调用点结果（多返回经 tuple → Extract 拆解）
//   - phi_operand 边：Phi 每个分支输入 → Phi
//   - funcData：direct 读写条目 + 静态调用记录（间接写闭包计算用）
//
// 动态调用（接口方法/函数值/闭包）v1 不解析：接口调用由 AST 适配器 calls 边覆盖，
// 函数值/闭包参数流待摘要系统（Phase 5）。
package ssa

import (
	"github.com/schaepher/codeintel/internal/domain"
)

// funcData 单个函数的摘要收集数据（构建期内存态）。
type funcData struct {
	directReads    []fieldEntry
	directWrites   []fieldEntry
	calls          []callInfo
	indirectWrites []fieldEntry // 外部摘要写（虚拟节点，emitSummaries 合并输出）
}

// fieldEntry 单个字段访问的摘要条目。
type fieldEntry struct {
	fieldPath    string
	instancePath string
	line         int
	snippet      string
	callLine     int    // 调用点行号（间接写回连，Q90）
	callArg      string // 调用点实参变量名（间接写回连，Q90）
}

// callInfo 静态调用记录（间接写匹配用）。
type callInfo struct {
	calleeID       domain.CanonicalID
	argStructPaths []string
	callLine       int      // 调用点行号（Q90 调用点级回连）
	argNames       []string // 非 const 实参变量名（Q90）
}
