// Package ssa 实现字段追溯适配器（docs/field_trace.md v2.2）。
// 基于 go/packages + go/ssa 构建 SSA IR，产出字段访问节点与数据流边，
// 接替 2026-08-13 移除的 Joern 适配器（TD.md 12.7）。
//
// Phase 1（骨架）：加载 + SSA 构建，发射函数/方法节点（保证后续边端点存在）。
// Phase 2+：字段提取（field_access + data_flows_to）、跨过程边、间接写、摘要。
package ssa

import (
	"go/token"
	"go/types"

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
	// regHits Q221：注册命中预处理表（Q168 O(1) 判定）——Index 级一次，
	// 原 extractor 懒构建每函数重复遍历全部注册点方法
	regHits regHits
	// typeMapping Q211：orm.Mapping 实体类型→表名注册（Index 级收集，
	// 发射前全量扫描——规避按包处理顺序：Mapping 可能在包 A 注册、
	// 包 B 使用）
	typeMapping map[*types.Named]string
	// workers 按包并发数（Q169/Q170）：默认 1=串行；命令行 --workers N
	// 指定（orchestrator SetWorkers 注入）
	workers int
}

// assignTarget 赋值表达式区间 → 目标变量名。
type assignTarget struct {
	name  string
	start token.Pos
	end   token.Pos
	// Q193：RHS 直接调用（顶层表达式是 CallExpr）的 '(' 位置——go/ssa
	// 的 Call.Pos 语义（嵌套子调用不记录，其 Pos 无法恢复为变量名）
	topCallPos token.Pos
}
