//go:build integration

// Package integration 端到端集成测试：真实仓库 → CLI init → SQLite 查询 →
// HTTP serve → clean 全流程（需要 scip-go；缺失时跳过）。
// 运行：make it（= go test -count=1 -tags integration ./integration/）
package integration

// scipGoAvailable：scip-go 在 PATH 或 GOBIN/GOPATH/bin 中可用。

// TestReindexRestores：reindex 一步重建——破坏索引数据后 reindex
// 恢复完整索引（删除旧库 + 全量 init）。

// fixtureRepo 建真实 Go 模块仓库（覆盖：跨包方法调用/接口/回调/普通函数）。

// runCLI 以完整命令行入口跑 CLI，返回退出码。

// fieldAccessID 查函数内指定 instance_path + access_kind 的字段访问节点 ID
// （value-trace 锚点用；行号不硬编码，避免 fixture 行号漂移）。

// runCLIOut 同 runCLI，额外捕获 stdout（CLI 输出断言用）。

// TestCLIFullFlow：init → DB 内容验证 → query → clean 全流程。

// TestIncrementalUpdate：init → 修改文件 → update → 新符号出现、
// 旧符号保留、被删除符号消失（TD.md 5.2 增量语义）。

// gitRun 在指定目录执行 git（注入 user 配置供 commit 使用）。

// TestServerEndToEnd：init 后起 HTTP serve（真实前端资源），全 API 验证。
// TestOutputNoiseFree：真实仓库 init 后——stdout 无日志混流（日志入
// .codeintel/codeintel.log）、--json 可解析、--compact 生效、export graph
// 输出 mermaid/dot（Q88/Q89/Q96）。

// mustOpen 打开仓库 DB（测试辅助）。

// min 返回较小值（Go 1.21 无内置 min）。

// TestFieldPrecisionSelfContained：⑥ 字段精度自包含用例（不依赖 radar）——
// 对象/SSA 值锚点不再扇出全部字段读；拷贝链（dest.ID = src.ID）经
// 值来源跳板保持闭合。

// TestORMChainDAOSelfContained：⑦ 链式 ORM 自包含用例——自定义 DAO 封装
// （Model(&X{主键}).Where(...).Update("col", v)）经 field-summary.yaml 的
// orm_write 条目映射为 表.列 虚拟节点（不依赖真实 gorm 模块）。

// TestCrossFunctionTraceSelfContained：⑩ 跨函数追踪复现——多种调用方
// 形态下 trace-forward 应连到被调函数内的实际字段写入：
//
//	A. 调用方参数传递（run2(c *Cfg) → fill(c)）
//	B. 调用方局部变量传递（var c Cfg; fill(&c)，调用方无字段访问无参数）
//	C. 调用方字段读后传参（s.c → fill）

// TestORMChainFormsSelfContained：⑪ ORM 链式形态覆盖——结构体 Updates
// 链式（Model().Where().Updates(&Y{})）与无 Model 的字符串列名 Update
// （Where().Update("col", v)——表名无法溯源时跳过而非报错）。

// TestCrossFunctionNoiseSelfContained：⑩ 跨函数追踪噪音复现——A 传
// *Record 给 B，B 写 record.FinalFee 且读多个无关字段。trace-forward
// A 的 FinalFee 下游：应连到 B 的 record.FinalFee 写入，且不含
// Metadata/Status 等无关字段读（同名跳板过滤）。

// TestORMUpdateRecordScopeSelfContained：⑪ ORM——session.Where(...)
// .Update(record, scope) 对象实参形态：record 变量 → 表.列 节点 +
// 对象兜底持久化边（summary_io）。

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

// TestLoadValueTraceSelfContained：举一反三——Load 值起点（rec := *ptr
// 解引用赋值后传参）。

// TestClosureFieldTraceSelfContained：继续查——闭包内字段写入节点生成
// （闭包字段访问归入外层函数，func_id=外层——追踪可用性验证）。

// TestMapElemArgTraceSelfContained：继续查——map 元素值传参
// （m["k"] 的值传给 helper）。

// TestValueTraceInterfaceSelfContained：继续查——value-trace 经接口
// argument 边进入候选实现（⑮ 只测了 trace-forward）。

// TestClosureWriteInSummarySelfContained：继续查——闭包内写入应计入
// 外层函数的字段摘要（direct_write，funcData 归外层）。

// TestCallbackClosureArgTraceSelfContained：继续查——callback 模式
// （apply(rec, func(r){r.FinalFee=...})——闭包字面量作为实参传入后在被
// 调函数内调用）：预期为已知限制（函数值参数跨函数无法静态解析），
// 此处验证不 panic 且不误连。

// TestTraceForwardStartFilteredSelfContained：B2 集成固化——trace-forward
// 起点须与目标字段所属结构体类型匹配；无关类型参数与包级全局变量
// （string）不得入链（此前 origin_kind IN (...) 无条件放行全部起点）。

// TestDeepChainNoIndirectWriteSelfContained：S1 集成固化——三层调用链
// （a→b→c）中 c 写自己内部对象（与实参无别名）时，a 不得有 T.A 间接写
// （别名排除须经跨函数参数 may 传播稳定生效，不依赖调用点处理顺序）。

// TestUserSummaryYAMLSelfContained：S2 集成固化——field-summary.yaml 相对
// 字段路径（"user.ID"）须补全为类型限定路径（pkg.T.ID），而非错误拼成
// pkg.T.user.ID。

// TestAnonymousStructFieldLineSelfContained：B3 集成固化——匿名 struct
// （range 元素）字段访问须带行号（fieldInfo 匿名分支曾提前 return，
// line_start=0 导致 CLI 无定位）。

// TestQueryUnusedSelfContained：query unused 全量报告（field_trace.md §16）——
// 死代码/孤立链/var 初始化调用/回调注册/main 的判定。

// TestQueryUnusedSinceSelfContained：--since <ref>——git diff 区间内新增
// 函数标 [new] 并只报告本次改动（field_trace.md §16.5）。

// TestQueryPathSelfContained：query path 数据流路径断言（field_trace.md
// §17.3）——v0 → 字段写 → 参数 → 字段读 → v1 全链可达；不可达返回无路径。

// TestQuerySymbolSinceSelfContained：--since 标注推广（§17.2）——
// symbol/callers 输出对本次新增函数标注 [new]。

// TestModuleCallsSelfContained：模块间 gRPC 调用（field_trace.md §18）——
// fixture 模拟 protoc 生成代码（.pb.go）+ modules.yaml → query module-calls
// 输出 svc_a → svc_b: Greeter.SayHello；export graph --type modules 产出图。

// TestModuleCallsDirectSelfContained：§18.6 手写 client——conn.Invoke
// 方法路径调用 + const 传播 → module-calls 输出含服务端模块（impl 按
// grpc_service name 匹配，跨 symbol:go / symbol:proto 标识）。

// TestModuleCallsHTTPSelfContained：§18.7 HTTP 模块间调用——routes.yaml
// 路由表 + http.Get 客户端 → module-calls 输出 http 调用（含 transport）。

// TestInterfaceDispatchIndirectWriteSelfContained：Q154 集成固化——接口
// 动态分派候选实现内的字段写回传为 wrapper/上游调用方的 indirect_write
// （实现 → wrapper → 上游逐层传播，INDIRECT_WRITE 边指向每个候选实现）。

// TestValueTraceDedupSelfContained：Q155 集成固化——value-trace 递归
// CTE 按 (id, dir) 去重。phi 汇聚（x = phi(a, b)，两分支 alloc 汇入）：
// 从 FinalFee.write 反向，每个节点恰好一行、深度正确，两分支都出现。

// TestGofRepositoryInterfaceSelfContained：Q156 集成固化——gof 框架
// fw.Repository[M] 接口摘要（真实外部依赖）：init 前置 go mod tidy
// （模块缓存有 gof，离线可解析），断言表.列虚拟节点生成（表名取实体
// TableName）+ where filter 列。

// TestRelationsAllSelfContained：Q160 集成固化——query relations --all
// 一次返回全库键关联（原生 SQL 键关联链：member.id 读出值 → account
// 按 member_id 过滤），无需逐表查询；export relations 同源输出。

// TestDispatchCandidateMetaSelfContained：Q161 集成固化——动态接口调用
// 的 argument 边携带候选元数据（interface/candidate_origin/confidence，
// 注册点命中 register 0.9），value-trace 标注且 --min-conf 可剪枝。

// TestValueTraceContainerBoundarySelfContained：Q163 集成固化——从
// Payment 分支写点（SettledFee.write）追踪，默认（候选边剪枝）不出现
// RefundSource 实现；显式 --min-conf 0 时经候选 returns 边可达且标注
// 候选（路径累计）。
