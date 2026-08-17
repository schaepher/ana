//go:build integration

package integration

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

// TestRelationsAllSelfContained：Q160 集成固化——query relations --all
// 一次返回全库键关联（原生 SQL 键关联链：member.id 读出值 → account
// 按 member_id 过滤），无需逐表查询；export relations 同源输出。
