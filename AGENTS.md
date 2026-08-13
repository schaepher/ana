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
                          Setup() 建 development logger（debug 级，stdout）与
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
   **initializes 边**：`&T{}`/`T{}`/`new(T)` 产生 调用者→struct 的
   initializes 边（conf 0.8），仅 module 内 struct（Underlying 为
   *types.Struct 才建，排除 map/slice 复合字面量）。
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
    `<pkg>.test:` 包）；`Expand` 返回双向 calls/implements/imports/initializes 直接邻居（上限 500 边）。
    前端在 assets/web/（G6 v5 UMD，CDN 引入），通过 addNodeData/addEdgeData 增量渲染，
    节点复用去重由前端 seen 集合保证。
    **G6 v5 布局坑**（playwright 实测）：`draw()` 不触发布局，增量数据必须显式
    `graph.layout()`；force 布局不处理孤立节点与增量新节点（位置留空会堆在原点）——
    addNode 时必须预置网格初始位置（style.x/y，固定 4 列避免 sqrt 回绕重叠）。
    位置读取用 `getNodeData(id).style.x/y`，`getData()` 读不到布局位置。
    坐标转换 API 参数为数组：`getElementPosition(id)` / `getClientByCanvas([x,y])`
    返回 `[x,y,z]`，传对象会得到 null。
    **交互**：单击显示信息，双击展开/收起。收起实现要点：expandedMap 记录每次
    展开新增的 nodes 与 edges（**已存在的邻居边也要记录**，否则收起删不掉）；
    收起用 setData 全量重建（G6 v5 removeEdgeData/removeNodeData 增量删除在批处理
    时引用已删节点报 "Node not found"）；展开令牌（expandToken）使收起时飞行中的
    展开回调失效，防止已删节点复活。
    **选中染色坑**（2026-08-13 实测）：选中切换必须先更新 selectedId 再调用
    setElementState——该 API 异步绘制（内部 await element.draw），样式函数在绘制
    时才求值（读闭包 selectedId）；顺序颠倒时旧染色的异步绘制后完成覆盖新染色，
    大图中快速点击稳定复现（14 节点图 13/14 次出错），表现为切换节点后边色不重置、
    点空白才恢复。节点标签为两行：`dir/basename` + 符号名（nodeLabel）。
    **剪枝规则**：展开节点时同向剪枝（pruneSiblings + rowClass）——只移除与展开
    节点同侧的兄弟（展开 callee 保留 caller 顶行，反之亦然）；已展开兄弟保留；
    rowClass 方向分类与三行布局一致（calls/initializes 出=down、implements/
    imports 出=up、其余=mid）。曾为"移除全部兄弟"，会把唯一顶行 caller 剪掉
    导致链路断头（用户反馈后修正）。
    **展开过滤**：有父展开时"过滤其他父"只拦 calls 入边（潜在 caller），
    且**按方向区分**——down/mid 类展开过滤（链式干净），up 类（caller）
    展开不过滤（展示调用方，链向上延伸，如展开 cmdInit 显示 Main）；
    has_method/implements/initializes 等入边是关联必须展示（否则双击
    接收者展开不出方法）。**收起顺序坑**：collectCollapse 须先递归回收
    整棵子树记录再判孤儿——边回收边判断会把连到后处理兄弟新增边的边
    误判为"有其他边"而残留节点。
    **树布局**：relayoutTree 方向感知分层——行号仅以 calls 入边（isCaller）
    为上一行，其余一律下一行（implements/imports 在三行布局中是上行依赖，
    但链视图中是节点自身子项，排下一行），每行水平居中。入口节点首次选择
    显式置于画布正中（addNode 网格位置在左上角，force 不移动孤立节点）。
    **信息栏**：右侧常驻 320px 侧边栏（#sidepanel），单击节点复用
    /api/expand 渲染分组信息（基本/文档注释/提交/关系按类型）。**坑**：
    G6 v5 给容器设内联 position:relative 会覆盖样式表的 absolute——
    侧边布局必须用外层 wrapper（#main-area 绝对定位 right:320px，
    容器 100% 填充），否则 right 不生效且节点会被面板遮挡不可点。
    **方法线 has_method**：AST 适配器 emitMethodReceiver 建立
    has_method 边（接收者类型 → 方法，方向为用户确认的"由接收者指向
    方法"；曾为 method→receiver 的 has_receiver，2026-08-13 反转并更名，
    重建需 clean 清库否则旧边残留）；轻量节点模式同 createObject；
    前端边虚线 [5,2] 标注"方法"，信息栏按视角拆分（struct 出边=方法
    （N）、方法入边=接收者（N））。**坑**：三行布局中间行单个节点
    （如接收者）会落在中心节点正上方（placeRow 单个节点 start=cx）——
    须 offsetSingle 偏移到中心右侧。
    **节点配色**：KIND_COLOR 每种类型独立色（函数蓝/方法青/结构体绿/
    接口紫/包橙/文件灰/提交深灰/对象薄荷绿）。
    **源码弹窗**：函数/方法节点信息栏 Source Code 按钮 → /api/source。
    后端按需读文件 + go/parser 提取声明区间（LineStart 精确 → 行范围
    → 名称三级匹配，容忍文件修改后行号漂移）；仅 function/method，
    路径解析须验证仍在仓库根内（防目录穿越）。前端 highlight.js
    （CDN github.min.css 主题）Go 高亮，hljs 未加载时降级 textContent。
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
