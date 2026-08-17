//go:build integration

package integration

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
