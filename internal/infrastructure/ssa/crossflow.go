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

// emitCrossFlow 发射单个函数的跨过程边并记录摘要数据。

// emitPhi 发射 phi_operand 边（常量分支跳过）。

// emitCall 处理单个调用点：argument / returns 边 + 摘要调用记录。
// 仅处理静态可解析且属于项目内的被调函数。

// dispatchOriginOf 判定候选实现的派发来源（Q161）：注册点命中
// （MakeInterface 具体值 → 接口，见 emitDispatches）→ register 0.9；
// 否则枚举兜底 enum 0.7。注册点收集一次缓存（全 prog 扫描开销大）。
// Q168：注册命中按 (iface, candidateKey) 预处理成 map——原逐调用点
// 线性扫描注册点（动态调用点多时 O(调用点×注册点)）→ O(1) 查找。

// recordCallInfo 记录调用摘要条目（间接写闭包消费：emitSummaries 沿
// fd.calls 传播被调函数写）。常量实参（nil、字面量）不产生实例传递，
// 不参与类型匹配；实参类型路径用于与被调函数写字段的声明类型匹配
// （Q36；Q157 展开嵌套字段 owner 链——OrderModel 含 Order 字段时
// 实现写 Order.FinalFee 也能匹配）。

// ownerTypesOf 收集实参类型及其嵌套 struct 字段的 owner 类型路径
// （Q157：OrderModel 含 Order 字段 → [pkg.OrderModel, pkg.Order]——
// 实现写 Order.FinalFee 也能经 OrderModel 实参匹配）。深度上限 3
// 防深嵌套爆炸；指针/切片解包；同类型去重。

// resolveStaticCallee 解析静态可确定的被调函数：静态调用 / 直接函数值 / phi 链。

// returnOperands 收集函数所有 Return 指令的操作数（多返回为元组）。

// returnOperandsCached 惰性缓存函数的 Return 指令操作数（多调用点复用，
// 避免每次 emitCall 重复扫描被调函数）。

// structPathOfType 取实参类型的结构体限定路径（*T → pkg.T；非具名结构体 → 空）。

// structPathOf 从 full_path（pkg.T.f）提取结构体路径（pkg.T）。

// emitEdgeKindLine 带行号的边（query table 写入方定位用；SQL/ORM
// 虚拟节点 summary_io 边的 line_num 此前缺失，聚合时只能兜底节点行号）。

// emitEdgeKind 发射指定 kind 的边（tool_source=ssa，conf 1.0，Q69）。

// emitEdgeKindMeta 发射带元数据的边（Q161 动态候选边：
// interface/candidate_origin/confidence——value-trace 标注与过滤用）。
