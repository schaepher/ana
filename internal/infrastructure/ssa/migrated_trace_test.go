package ssa

import (
	"strings"

	"github.com/schaepher/codeintel/internal/domain"
)

// 本文件由 tmp/ast_split/migrate.go 从 integration/ 迁移（2026-08-17）：
// fixture 自建、断言全为 SSA/sqlite 产物——单元测试化，脱离 scip/CLI
// 管道用 indexFixtureRepo 落库后直接 repo 断言。

// traceHas 检查追溯行中 Name/FullPath 含 substr。
func traceHas(rows []*domain.TraceRow, substr string) bool {
	for _, r := range rows {
		if strings.Contains(r.Name, substr) || strings.Contains(r.FullPath, substr) ||
			strings.Contains(r.FuncID, substr) {
			return true
		}
	}
	return false
}

// vtCandHas 检查追溯行中是否含动态候选边标注（Q161 EdgeOrigin）。
func vtCandHas(rows []*domain.TraceRow) bool {
	for _, r := range rows {
		if r.EdgeOrigin != "" || r.DispatchOrigin != "" {
			return true
		}
	}
	return false
}

// ffsHas 检查函数字段摘要行中 FieldPath 含 substr（fields 查询断言）。
func ffsHas(rows []*domain.FunctionFieldSummary, substr string) bool {
	for _, r := range rows {
		if strings.Contains(r.FieldPath, substr) {
			return true
		}
	}
	return false
}

// TestLocalObjectTraceSelfContained：⑭ 局部对象追踪——DAO 返回对象 →
// 局部变量 → helper 传参（起点须纳入与目标字段同类型的 local/phi 值）。

// TestInterfaceCallTraceSelfContained：⑮ 接口动态派发——接口方法调用
// 传参（无静态 callee）须经候选实现建立 argument 边，追踪进入实现。

// TestGlobalObjectTraceSelfContained：举一反三 A1——全局变量对象传参
// （var g Record; helper(&g)）trace-forward 起点（global 值来源格）。

// TestPhiObjectTraceSelfContained：举一反三 A2——phi 值传参
// （if 分支各自赋值后传 helper）。

// TestFuncValueCallTraceSelfContained：举一反三 B4——函数值调用
// （f := getHandler(); f(record)——f 来自返回值，调用点无静态 callee）。

// TestInterfaceReturnTraceSelfContained：举一反三——动态调用返回值贯通：
// err := w.Write(&Record{})——value-trace 从返回值节点应连到候选实现的
// Return 值（⑮ 只建了 argument，returns 边待验证）。

// TestClosureFieldTraceSelfContained：继续查——闭包内字段写入节点生成
// （闭包字段访问归入外层函数，func_id=外层——追踪可用性验证）。

// TestMapElemArgTraceSelfContained：继续查——map 元素值传参
// （m["k"] 的值传给 helper）。

// TestCallbackClosureArgTraceSelfContained：继续查——callback 模式
// （apply(rec, func(r){r.FinalFee=...})——闭包字面量作为实参传入后在被
// 调函数内调用）：预期为已知限制（函数值参数跨函数无法静态解析），
// 此处验证不 panic 且不误连。

// TestTraceForwardStartFilteredSelfContained：B2 集成固化——trace-forward
// 起点须与目标字段所属结构体类型匹配；无关类型参数与包级全局变量
// （string）不得入链（此前 origin_kind IN (...) 无条件放行全部起点）。

// TestLoadValueTraceSelfContained：举一反三——Load 值起点（rec := *ptr
// 解引用赋值后传参）。

// TestValueTraceInterfaceSelfContained：继续查——value-trace 经接口
// argument 边进入候选实现（⑮ 只测了 trace-forward）。

// TestValueTraceDedupSelfContained：Q155 集成固化——value-trace 递归
// CTE 按 (id, dir) 去重。phi 汇聚（x = phi(a, b)，两分支 alloc 汇入）：
// 从 FinalFee.write 反向，每个节点恰好一行、深度正确，两分支都出现。

// TestValueTraceContainerBoundarySelfContained：Q163 集成固化——从
// Payment 分支写点（SettledFee.write）追踪，默认（候选边剪枝）不出现
// RefundSource 实现；显式 --min-conf 0 时经候选 returns 边可达且标注
// 候选（路径累计）。

// TestClosureWriteInSummarySelfContained：继续查——闭包内写入应计入
// 外层函数的字段摘要（direct_write，funcData 归外层）。

// TestInterfaceDispatchIndirectWriteSelfContained：Q154 集成固化——接口
// 动态分派候选实现内的字段写回传为 wrapper/上游调用方的 indirect_write
// （实现 → wrapper → 上游逐层传播，INDIRECT_WRITE 边指向每个候选实现）。

// TestDispatchCandidateMetaSelfContained：Q161 集成固化——动态接口调用
// 的 argument 边携带候选元数据（interface/candidate_origin/confidence，
// 注册点命中 register 0.9），value-trace 标注且 --min-conf 可剪枝。

// TestORMChainDAOSelfContained：⑦ 链式 ORM 自包含用例——自定义 DAO 封装
// （Model(&X{主键}).Where(...).Update("col", v)）经 field-summary.yaml 的
// orm_write 条目映射为 表.列 虚拟节点（不依赖真实 gorm 模块）。

// TestORMChainFormsSelfContained：⑪ ORM 链式形态覆盖——结构体 Updates
// 链式（Model().Where().Updates(&Y{})）与无 Model 的字符串列名 Update
// （Where().Update("col", v)——表名无法溯源时跳过而非报错）。

// TestORMUpdateRecordScopeSelfContained：⑪ ORM——session.Where(...)
// .Update(record, scope) 对象实参形态：record 变量 → 表.列 节点 +
// 对象兜底持久化边（summary_io）。
