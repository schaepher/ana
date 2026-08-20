// 外部函数摘要系统（field_trace.md §7）：内置摘要 + 用户 field-summary.yaml。
// 构建器遇到带摘要的外部函数调用时：
//   - 生成虚拟 field_access 节点（is_external=1，func_id=调用者）
//   - external_summary 节点 + summary_io 边
//   - 写摘要：INDIRECT_WRITE 边（调用者 → 虚拟节点）+ data_flows_to（实参 → 虚拟节点）
//   - 读摘要：data_flows_to（虚拟节点 → 实参）
//   - 写摘要的字段进入调用者的间接写摘要表（indirectWrites）
package ssa

const (
	patternAll = "all"
)
