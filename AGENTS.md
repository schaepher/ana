# AGENTS.md — 面向 AI 代理的开发指南

本文件供 AI 编码代理（Claude Code、Codex、Cursor 等）在修改本仓库前阅读。

## 项目一句话

`codeintel` 是一个 Go 代码库智能索引系统：对 Go module 仓库做静态分析，
产出 SQLite 代码图（`.codeintel/codeintel.db`），通过 CLI 提供符号与调用关系查询。
设计权威是 [`docs/TD.md`](docs/TD.md)（v2.0，22 个决策点均已确认）。

## 常用命令

```shell
go build ./...                      # 编译
go test ./...                       # 测试（需要 scip-go 在 PATH 或 go bin）
go build -o codeintel ./cmd/codeintel
# 对任意 Go 仓库构建索引并查询：
#   codeintel init --repo <path>
#   codeintel query symbol|callers|callees|impact ...
```

## 架构与目录

六边形架构：`internal/domain`（内核，零外部依赖）通过 Port 接口与适配器解耦。

```
internal/domain/          领域模型：CodeEntity/Fact/CanonicalID、IndexerPort/CodeRepository 端口
internal/canonicalizer/   Canonical ID 生成、SCIP symbol 解析（FromScipSymbol）
internal/logging/         ctx ↔ *zap.Logger + OpenTelemetry 链路追踪初始化。
                          Setup() 建 development logger（debug 级，stderr）与
                          stdouttrace 导出器；FromContext 在 span context 有效时
                          附加 trace_id/span_id 字段（entrylog 注入的日志用）
internal/orchestrator/    全量构建编排：并行适配器、独立超时 10min、分批 1000 条事务、降级报告
internal/infrastructure/
  scip/                   调用 scip-go 生成 SCIP 索引 → 符号节点 + IMPLEMENTS 边（conf 1.0）
  ast/                    go/packages AST 分析 → CALLS + IMPORTS 边（conf 0.8）+
                          服务入口标记（serves_http / serves_grpc）
  git/                    git log → COMMIT 节点 + MODIFIED_BY 边（conf 1.0）
  sqlite/                 nodes/edges/build_metadata 仓储；SaveBatchStats 分批提交；
                          GetRoots / Expand（图探索）
internal/server/          HTTP API：/api/roots（顶层入口）、/api/expand（点击展开）
internal/cli/             init / serve / query / clean 命令
assets/web/               AntV G6 v5 前端（go:embed 嵌入；index.html + app.js）
scripts/entrylog/         AST 日志注入工具（见下）
```

## 链路追踪（OpenTelemetry）

- 入口（main）创建 root span（`codeintel.main`），ctx 贯穿 `cli.Main` → cmdInit/cmdServe
- `logging.FromContext(ctx)` 从 ctx 提取 span context，日志自动带 trace_id/span_id
- serve 的 Server 持有带 span 的 ctx，handler 日志同链路
- **坑**：`os.Exit` 不执行 defer——span.End 与 tp.Shutdown 必须在 os.Exit 前显式调用，
  否则 span 不导出；`zap.NewDevelopment()` 返回双值需 `zap.Must` 包裹
- 导出器为 stdout（PrettyPrint）；生产环境需替换为 OTLP 等

## entrylog 日志注入工具

`go run ./scripts/entrylog -dir <module 根>`：为所有顶层函数/方法注入

```go
logger := zap.L()                 // 无 ctx 参数
logger := logging.FromContext(ctx) // 有 ctx 参数（从 ctx 取 logger，缺失回退全局）
logger.Debug("enter <name>")
defer logger.Debug("exit <name>")
```

- 幂等（已注入跳过）、函数内有 logger 标识符时跳过、排除 _test.go
- 必须跳过 `internal/logging`（FromContext 注入自身会无限递归）与 `scripts/`
- **实现要点**：AST 只读定位 + 纯文本插入（format.Node 全量重写会把游离注释
  重排进调用表达式中间——踩过坑）；单行函数体 `{ return x }` 需拆行
  （Lbrace 后插入 + Rbrace 前补换行）；import 缺失时文本补入

## 关键设计决策（修改前必读）

这些是已确认的约定，改它们先与用户确认：

1. **Canonical ID**：`symbol:go:<import_path>:<name>`。方法名统一 `(T).method`，
   值/指针接收者不区分（与 scip-go 的输出一致）。文件节点 `file:<relpath>`，
   提交节点 `commit:<sha>`，包节点名取路径末段。
2. **置信度体系**：SCIP=1.0、CodeGraph(AST)=0.8、Git=1.0。CLI 查询阈值
   **0.8**（不要改回 0.85——TD.md 决策 10 的 0.85 与 5.1 表的 CodeGraph=0.8 矛盾，
   0.85 会把全部调用边过滤掉，这是已确认的偏差）。
3. **同边合并**：edges 有 UNIQUE(source_id, target_id, kind)，UPSERT 保留最高置信度；
   节点按 id UPSERT 合并 properties（json_patch），SCIP 写入的 kind/行号不被覆盖。
4. **外键约束**：edge 端点节点必须存在；不存在的边（如 Git 追踪到未索引文件）
   在 SaveBatchStats 中静默跳过并计数，不中断构建。
5. **SCIP 引用边未实现**：scip-go 的定义 occurrence 只覆盖符号名（不含函数体），
   引用无法归属到调用者，因此没有 REFERENCES 边；引用类查询依赖 AST 的 CALLS 边。
6. **签名来源**：SCIP v0.7.1 协议不输出 signature，签名由 AST 适配器用
   `types.ObjectString` 生成（含接收者）。
7. **降级矩阵**（TD.md 9.2）：scip 失败 → 构建 failed；其他适配器失败 → degraded；
   MCP 工具永不抛错是未来 MCP 层的约定。
8. **scip-go 输出格式**：`-o <file> -q` 写文件（stdout 会混入进度日志）；
   occurrence range 为 3 值单行 `[line, start_char, end_char]`；
   子包的完整路径在 Namespace descriptor（反引号），`Package.Name` 只有 module 名。
9. **服务入口标记**（图探索顶层）：AST 适配器检测函数是否调用 net/http 或
   google.golang.org/grpc 包（含方法调用，如 `srv.ListenAndServe()`——注意方法选择器
   在 `info.Selections` 而非 `info.Uses`，本实现用 Uses 解析 Sel 已验证可行），
   写入节点 properties `serves_http` / `serves_grpc`。**坑**：外部包调用点不建 CALLS 边，
   标记 fires 时必须立即 emit 节点，否则节点永远不带标记。
10. **图探索 API**：`GetRoots` 返回 main 入口 + 服务入口（排除 `_test.go` 文件与
    `<pkg>.test:` 包）；`Expand` 返回双向 calls/implements/imports 直接邻居（上限 500 边）。
    前端在 assets/web/（G6 v5 UMD，CDN 引入），通过 addNodeData/addEdgeData 增量渲染，
    节点复用去重由前端 seen 集合保证。
    **G6 v5 布局坑**（playwright 实测）：`draw()` 不触发布局，增量数据必须显式
    `graph.layout()`；force 布局不处理孤立节点与增量新节点（位置留空会堆在原点）——
    addNode 时必须预置网格初始位置（style.x/y，固定 4 列避免 sqrt 回绕重叠）。
    位置读取用 `getNodeData(id).style.x/y`，`getData()` 读不到布局位置。
11. **serve 运维坑**：`serve` 打开的是 .codeintel/codeintel.db；重建索引
    （rm -rf .codeintel 或 init 清库）会留下持有已删除文件句柄的旧 serve 进程，
    表现为 API 返回旧数据。改库后须重启 serve。

## 已知限制

- 仅单 module 仓库；包级初始化（var x = NewFoo()）中的调用不建 CALLS 边
- sqlite-vec 向量表未创建（Semble 未接入）；schema 版本由 PRAGMA user_version=1 管理，
  版本不匹配时报错提示 `codeintel clean` 重建
- 未实现：MCP serve（explore_symbol 等 5 工具）、增量构建、LLM 摘要、Joern/Semble

## 测试

- `internal/canonicalizer`：纯单测（SCIP symbol 解析的各种形式）
- `internal/orchestrator`：端到端测试，临时 Go module → FullBuild → 校验图数据
  （需要 scip-go，缺失时自动 skip）
