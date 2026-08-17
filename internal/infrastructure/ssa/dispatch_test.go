package ssa

// findDispatch 查找 source → target 的 dispatch_to 边（metadata 断言用）。

// TestDispatchRegisterPoint：注册点（MakeInterface）命中 → dispatch_to 边
// origin=register、confidence=0.9、register_site 记录注册位置（Q91/Q93/Q94）。

// TestDispatchEnumFallback：无注册点的实现者 → 枚举兜底 origin=enum、
// confidence=0.7（Q93）。

// TestDispatchMissingInfo：无法确定动态类型（函数值/外部接口）→ 缺失
// 信息类别（Q93：dynamic_call_unresolved），无 dispatch_to 边或带缺失标记。

// TestDispatchToEdgeCount：Eng 只有一个实现方法 → 恰好一条 dispatch_to 边
// （无重复发射）。

// TestInterfaceDispatchIndirectWrite：Q154——接口动态分派候选实现内的
// 字段写须回传为 wrapper 与上游调用方的 indirect_write。此前动态分支只
// 建 argument/returns 边、未追加 funcData.calls，间接写闭包（summary.go:29
// 消费 fd.calls）无消费入口——实现对 Order.FinalFee 的写断在接口调用点。

// TestDispatchValueReceiverAndSelfExclusion：⑬ 猎 bug——值接收者实现
// （候选集含 (Eng).Hello）且接口自身不进入候选集（self 排除）。

// TestDispatchEdgeCandidateMeta：Q161——动态 argument/returns 边附加
// 候选元数据（interface / candidate_origin / confidence），value-trace
// 据此区分必达/候选路径（注册点命中 register 0.9，枚举兜底 enum 0.7）。

// TestDispatchEdgeCandidateMetaGoDefer：Q161 场景树——Go/Defer 形态的
// 动态调用，argument 边同样携带候选元数据（emitCall 对 Go/Defer 共用
// 动态分支）。

// TestDispatchOriginsMultiImpl：Q161 场景树——多候选实现写同一字段时，
// wrapper 的 indirect_write 摘要保留全部来源（每个候选实现一条 origin，
// 不再折叠为单行"置零"分支）。

// TestDispatchOriginsMultiField：Q163 回归——被调函数写多个匹配字段
// 时，调用方 indirect_write 的 origins 每个字段都保留（此前 break 只
// 记第一个匹配字段，其余字段来源为空）。
