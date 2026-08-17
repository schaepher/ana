// Package ssa 实现字段追溯适配器（docs/field_trace.md v2.2）。
// 基于 go/packages + go/ssa 构建 SSA IR，产出字段访问节点与数据流边，
// 接替 2026-08-13 移除的 Joern 适配器（TD.md 12.7）。
//
// Phase 1（骨架）：加载 + SSA 构建，发射函数/方法节点（保证后续边端点存在）。
// Phase 2+：字段提取（field_access + data_flows_to）、跨过程边、间接写、摘要。
package ssa

import (
	"go/token"

	"github.com/schaepher/codeintel/internal/domain"
)

var _ domain.IndexerPort = (*Adapter)(nil)

// Adapter 是 SSA 字段追溯适配器。
type Adapter struct {
	// fd 摘要收集（构建期内存态）：function_field_summary 预计算用
	fd map[domain.CanonicalID]*funcData
	// dispatchRegs 接口注册点缓存（Q161 动态边候选元数据）：Index 级
	// 共享一次扫描——放 extractor（每函数新建）会每函数全 prog 扫描
	dispatchRegs dispatchReg
	// workers 按包并发数（Q169/Q170）：默认 1=串行；命令行 --workers N
	// 指定（orchestrator SetWorkers 注入）
	workers int
}

// SetWorkers 设置按包并发数（Q170：--workers 参数；≤1 退串行）。

// Name 实现 IndexerPort。

// Index 加载仓库全部包、构建 SSA，并发射字段追溯数据。

// buildIdentIndex 收集项目内文件的所有标识符（位置 → 名字），供 Alloc 反查源码变量名。

// isModuleFunction 判断 SSA 函数是否属于项目内包。

// emitFunction 发射单个函数的全部产出（Q174：局部收集）：
//  1. 函数/方法节点（Phase 1：保证边端点存在，ID 与 AST 适配器一致）
//  2. 字段访问节点与数据流边（Phase 2：field_extractor.go）
//  3. 返回 (ownerID, 局部 funcData)——由分块 worker pool 锁内合并进
//     data（闭包归外层；块间并行时同一 funcData 不再被并发写）
//
// 仅处理有 FuncDecl 源码的顶层函数/方法——闭包（FuncLit）与合成 wrapper 跳过；
// 闭包内字段访问在 Phase 2 归入外层函数（field_trace.md Q14 适配）。

// mergeFuncData 锁内合并局部 funcData 进共享 map（Q174 分块并发）：
// direct/indirect 条目均为 append 语义，合并顺序不影响结果集。

// emitSignatureNodes 发射函数/方法签名的参数与返回节点（parameter / result）
// 及 has_param / has_result 边——签名结构展示，前端展开函数节点时可见。
// slot：参数 #param.<name>（接收者 #param.recv.<name> 防重名），
// 返回 #result（多返回 #result.<idx>）。

// funcIdentity 从 types.Func 生成 canonical ID / kind / name，与 AST 适配器 fnID 一致：
// 方法统一 (T).method（值/指针接收者不区分），匿名结构体上的方法返回空。

// relPath 将绝对路径转为仓库相对路径；仓库外文件返回空串。

// isInModule 判断包路径是否属于任一被索引 module（自身或子包；P2-3
// 多 go.mod——任一 module 前缀匹配即项目内）。

// assignTarget 赋值表达式区间 → 目标变量名。
type assignTarget struct {
	name  string
	start token.Pos
	end   token.Pos
}

// buildAssignTargets 构建 赋值表达式区间 → 目标变量名（Q83：lifting 后
// map/slice 字面量为 MakeMap/MakeSlice 寄存器，其 Pos 落在字面量内部，
// 用区间匹配恢复容器名）。按 start 排序返回，供二分查找。

// lhsIdentName 取赋值目标标识符名（多目标取第 i 个；复合目标如 x[0] 取 x）。
