//go:build integration

package integration

// TestLoadValueTraceSelfContained：举一反三——Load 值起点（rec := *ptr
// 解引用赋值后传参）。

// TestValueTraceInterfaceSelfContained：继续查——value-trace 经接口
// argument 边进入候选实现（⑮ 只测了 trace-forward）。

// TestClosureWriteInSummarySelfContained：继续查——闭包内写入应计入
// 外层函数的字段摘要（direct_write，funcData 归外层）。

// TestInterfaceDispatchIndirectWriteSelfContained：Q154 集成固化——接口
// 动态分派候选实现内的字段写回传为 wrapper/上游调用方的 indirect_write
// （实现 → wrapper → 上游逐层传播，INDIRECT_WRITE 边指向每个候选实现）。

// TestValueTraceDedupSelfContained：Q155 集成固化——value-trace 递归
// CTE 按 (id, dir) 去重。phi 汇聚（x = phi(a, b)，两分支 alloc 汇入）：
// 从 FinalFee.write 反向，每个节点恰好一行、深度正确，两分支都出现。

// TestDispatchCandidateMetaSelfContained：Q161 集成固化——动态接口调用
// 的 argument 边携带候选元数据（interface/candidate_origin/confidence，
// 注册点命中 register 0.9），value-trace 标注且 --min-conf 可剪枝。

// TestValueTraceContainerBoundarySelfContained：Q163 集成固化——从
// Payment 分支写点（SettledFee.write）追踪，默认（候选边剪枝）不出现
// RefundSource 实现；显式 --min-conf 0 时经候选 returns 边可达且标注
// 候选（路径累计）。
