# AGENTS.md — 面向 AI 代理的开发指南

本文件供 AI 编码代理（Claude Code、Codex、Cursor 等）在修改本仓库前阅读。

## 文档导航

- [`docs/DoD.md`](docs/DoD.md)：交付验收清单（10 条）+ 五轴自检——**每个
  改动交付前逐项自查**（Q235）
- [`docs/runbook.md`](docs/runbook.md)：故障模式速查表（/tmp 满、WAL、
  端口冲突、schema 迁移等 14 条，Q235）
- [`docs/design-q235.md`](docs/design-q235.md)：Q235 六项借鉴设计（实施
  完成后归档进 field_trace.md）
- [`docs/field_trace.md`](docs/field_trace.md)：逐 Q 实现记录（§63 起为
  Q234/Q235 系列）

## 项目一句话

`codeintel` 是一个 Go 代码库智能索引系统：对 Go module 仓库做静态分析，
产出 SQLite 代码图（`.codeintel/codeintel.db`），通过 CLI 提供符号与调用关系查询。
设计权威是 [`docs/TD.md`](docs/TD.md)（v2.0，22 个决策点均已确认）；
**图数据模型总表（节点/边/属性/置信度/表结构）见 [`docs/data-model.md`](docs/data-model.md)**——
理解本项目优先读它，再读 field_trace.md 各功能 §。

## 常用命令

```shell
go build ./...                      # 编译
go test ./...                       # 测试（需要 scip-go 在 PATH 或 go bin）
go build -o codeintel ./cmd/codeintel
# 对任意 Go 仓库构建索引并查询：
#   codeintel init --repo <path>          # 全量构建
#   codeintel reindex --repo <path>       # 重建（删旧库绕过 schema 检查 + init）
#   codeintel query symbol|callers|callees|impact|table ...
```

## 解析新项目的配置（目标仓库根目录的可选 YAML）

全部可选（不配置也能 init），配置后 `init`/`reindex` 生效；示例见本仓库根
`*.example.yaml`（复制为目标仓库根目录的对应文件名）：

| 文件 | 作用 | 何时配置 |
|---|---|---|
| `field-summary.yaml` | 外部函数/接口调用的读写语义（→ 表.列 虚拟节点） | query table/relations 缺表或缺写入方（自研 ORM/DAO、非 GORM 框架） |
| `modules.yaml` | 多模块 monorepo 模块划分（模块图） | 多 go.mod 或需模块级调用图 |
| `routes.yaml` | HTTP 路由人工表（模块图 http 边） | 需 HTTP 调用识别 |

关键点：
- `field-summary.yaml` 两类条目：`func`（静态调用：`orm_write`/`orm_read`/
  `reads`/`writes`/`reads_all`/`writes_all`/`param_index`）与 `iface`
  （接口方法动态 invoke：`method` + `kind`（write/read/filter/sql）+
  `obj_arg`/`where_arg`/`id_arg`/`sql_write`）。内置已覆盖 gof 全家
  （fw.Repository 两路径、db.Connector 原生 SQL、db/orm.Orm.Get、
  orm.Save）；其他框架（xorm/sqlx/自研接口）用 iface 自定义
- SQL 摘要支持 `?` 与 `$N` 占位符、多行 WHERE、ORDER BY 剥离
- 表名优先取实体 `TableName()`（否则类型名 snake_case）
- 改配置后须重新 `init`/`reindex`（增量 update 不含配置变更）

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
    **树布局**：relayoutTree 方向感知分层——**箭头始终向下**：isUp(parent,
    child) 判断 child 是否通过任意关系（calls/has_method/implements 等）
    指向 parent（child 是 source）→ child 在上一行，否则下一行；每行
    水平居中。三行布局/rowClass/tail 定位同一原则（source 在上、target
    在下）。入口节点首次选择显式置于画布正中（addNode 网格位置在左上角，
    force 不移动孤立节点）。
    **增量布局**：展开时 relayoutTree(root, prevY)——已有节点保持 y，
    新节点行插值；prevY 必须在 addNode 前收集（否则新节点网格位置被当
    已有位置）；updateNodeData 无返回（非 promise），fitView 须 setTimeout
    等动画完成（否则包围盒按旧位置算，缩放无效）；收起走全量重排。
    **无记录节点定位**：不在展开树中的节点（剪枝后作为邻居重新出现的
    父）按边定位——calls 出边（该节点是 caller）在相邻节点上一行、其余
    下一行，保证箭头始终向下；无法定位才追加最后一行（曾一律丢最后行
    导致父节点掉到底部、箭头朝上）。
    **全量重排兜底**：relayoutTree 无 prevY（收起/全量）时按 minD 偏移
    分层（startY + (d-minD)*rowGap）——曾写坏为全部行落 startY=80，
    收起后所有节点堆一行、箭头全向上（用 audit 探针：每步检查所有
    calls 边 source.y < target.y）。
    **边方向修正**：BFS/tail 后对所有边做 source<target 循环修正（共享
    节点方向冲突）；修正触发（depthChanged）时 prevY 错位，须整树按新
    深度干净分层（否则 rowY[0] 被旧位置节点占据导致同层）；行超画布
    底部也要 fitView。**剪枝隐藏可配置**：hideKinds（默认 {calls}，
    localStorage codeintel.hideKinds）——只隐藏"同侧且属于勾选关系"
    的兄弟（edgeKind 检查）。
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
    **implements 方向**：SCIP 适配器 is_implementation 关系输出
    接口 → 实现者（2026-08-13 反转，用户确认"接口指向实现"）；
    前端三行布局/rowClass 的 implements 分类对调（接口出边=下行、
    实现者入边=上行），信息栏按视角拆分（接口=实现者（N）、
    实现者=实现（N））。重建需 clean 清库。
    **嵌套调用**：参数位置的调用（isArgCall 判定）不建 calls——
    handleNestedArg 递归建 passes_result（"持有返回参数"）：A(B(C()))
    → A→B、B→C；callee 的非调用实参（函数引用）走 argFuncRef 持有
    参数（passes_to）。
    **接口整体节点**：接口方法（desc[2] 为 Term）不建独立节点——
    canonicalizer 标记 IsInterfaceMethod；SCIP 适配器跳过节点与方法级
    implements；AST 适配器 isInterfaceMethod（接收者类型是接口）跳过
    调用边与节点。重建需 clean 清库。
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

## 开发操作坑（踩过，勿重蹈）

- **`pkill -f "codeintel-e2e serve"` 会匹配自身自杀**（2026-08-14 复发两次）。
  清理 e2e 进程用 `pkill -x codeintel-e2e`（精确进程名）；杀完 sleep 0.5 再起新进程。
- **git 命令务必在 codeintel 仓库目录执行**：曾在 `/home/schaepher/Codes/验证仓库`（验证仓库）
  误执行 `git add -A` 把 `.codeintel/` 文件提交进 验证仓库（已撤销）。验证仓库只读不改。
- **schema 无自动迁移**（`PRAGMA user_version=4`）：改动表结构后验证仓库必须
  `codeintel clean` + `init` 重建，否则旧库 schema 不匹配报错或数据形态过时
  （曾因旧库缺 GORM 虚拟节点误判功能未生效）。
- **日志已切文件**：所有带 `--repo` 的命令日志写入 `.codeintel/codeintel.log`，
  stdout 只承载查询结果——排查问题看日志文件，不要从 stdout 找日志。
- **验证矩阵**：make test（12 包）/ make it（integration，需 scip-go）/
  make e2e（端口 8096，E2E_REPO 指定）——改完代码三件套都要过。
- **工作流约定**（用户明确）：每个功能先写测试再实现（测试先行）；开发中的
  疑问必须先向用户确认（设计树访谈模式）；改完验证后必须 git push。
- **验证形态矩阵**（2026-08-17 XORM 教训）：框架适配/查询类功能，测试必须覆盖
  **真实调用形态**——具体类型静态调用（如 *xorm.Session，用 replace 本地模块
  模拟真实包路径）+ 接口动态调用，自建 fixture 只算第一层；缓存/优化必须测
  **读命中闭环**（写入→读取→命中→失效），不只测写入；性能改动带优化前后
  实测对比（数字验收）。integration/fixtureapp 提供固定真实形态代码库，
  适配器改动后须跑它验证（CLI 全管道回归）。
- **schema 与语义变更需重建验证**：适配器改动后验证仓库必须 `init` 全量重建
  （增量 update 只更新变更文件，可能残留旧形态数据）。
- **relations 推断变更必须递增 relationsAlgoVersion**（rg_cache.go，当前 q208）：
  缓存键 = build_id + 版本，改 rg_*.go/relationsFor* 不递增会残留旧缓存
  （Q199 教训；Q205/Q208 各复发一次）。快速验证手段：
  `sqlite3 <repo>/.codeintel/codeintel.db "DELETE FROM relation_candidates"`
  强制重算；缓存存**未过滤全量**（Q208），hops 过滤是读取期行为——窄参数
  查询不会污染缓存，放宽参数始终可见长链。
- **前端截图验证**（2026-08-18 ER 图教训）：playwright 视口宽度必须 ≥ SVG
  宽度（默认 1440 会横向截断——用 2600 或元素级截图）；截图后必须用 PIL
  像素解析验证内容范围与线色（本环境 Read 工具读不了图片）；fullPage 只
  截视口宽；SVG 高度要预留行间隙（绕障线垂直段超出会被裁剪）。

## 已知限制

- **多 go.mod 已支持**（2026-08-15 P2-3）：递归扫描根下所有 go.mod，
  每 module 独立加载/独立 scip-go；go.work 根（无根 go.mod）仍不支持
  （报错提示进入模块目录）
- sqlite-vec 向量表未创建（Semble 未接入）；schema 版本由 PRAGMA user_version=4 管理，
  版本不匹配时报错提示 `codeintel clean` 重建
- 未实现：LLM 摘要、Semble；**MCP serve 已取消**（2026-08-15 Q135：AI 直接
  使用 CLI 查询命令，--json 即结构化契约）

## 测试

测试先行（先写失败测试再实现）；验证矩阵：make test（12 包）/ make it（-tags integration，需 scip-go）/ make e2e（27 项）。

**基建速查**（写测试前先读对应包）：
- ssa：`indexFixture(t, files)` → (nodes, facts)；`indexFixtureFull` → (nodes, facts, summaries)——临时 module（`moduleGoMod`）跑完整 Adapter.Index（含别名/摘要/派发），fixture 可加 `external/` 子包与 `field-summary.yaml`；helper `findFieldAccess`/`findSSAValue`（slot 前缀匹配）/`factsFrom`
- sqlite：`newTestRepo` + `save` + `node`；TraceForward 测试的参数节点须补 `type_string`（B2 后起点须类型匹配）
- action：`seedRepo` → (a, dir)；TraceConditions 测试须自行写真实 main.go（seedRepo 只建节点不写文件）
- integration：`go:build integration` 标签；`runCLIOut` 跑 CLI；`scipGoAvailable` 跳过

**断言坑**（踩过）：SSA 临时名 tN/lifting 提升不稳定 → 节点 ID/名称用前缀或通配匹配；fixture 行号数错、断言提前 return 会掩盖失败 → 先收集后判断；依赖 map 迭代顺序的不稳定 bug 用 `-count=10` 复现；alias 边 source 是值节点（funcID#slot），expand 函数节点不返回 alias。
**SQLite 坑**：单连接上外层 rows 迭代中开新 Query 会**挂起死锁**（GetTableColumns 踩过）——先收完外层行、Close 后再开内层查询；Metadata 数字断言注意 int vs float64（emit 用 int，SQLite json_extract 回读是 float）。

- `internal/canonicalizer`：纯单测（SCIP symbol 解析的各种形式）
- `internal/orchestrator`：端到端测试，临时 Go module → FullBuild → 校验图数据
  （需要 scip-go，缺失时自动 skip）

### 验证环境教训

- **WAL 模式 SQLite 构建中强杀会损坏 DB**：reindex/init 跑大仓库（go2o 3 分钟）时用 timeout 强杀 → WAL 未 checkpoint → `database disk image is malformed`（下次操作报错）。验证用**后台运行 + 轮询**（run_in_background），或 timeout 给足余量；损坏后删 db/-wal/-shm 重建。serve 与 reindex 并发操作同一 DB 也要避免。

## Q221 构建期性能教训

- **懒初始化兜底掩盖初始化遗漏**（同模式两次）：`ext.dispatchRegs == nil` /
  `ext.regHits == nil` 本意防 nil，因上层从未初始化变成"每函数全量预处理"
  ——dispatchRegs（每函数全图扫描 305s CPU）与 regHits（每函数遍历注册点
  ~180s）修复后 go2o 构建 5m16s → 15.6s。**每函数新建的 extractor 里
  出现"全量预处理 + nil 兜底"组合时，检查是否该提到 Index 级一次**
- **对象池收益要实测**：GC ~40% CPU 时对象池看似合理，实测零贡献（CPU
  不变）——regHits 修复后 GC 已非瓶颈。收益不足且有 use-after-free
  生命周期风险，回滚。**性能优化以 CPU profile 重采样为准，不靠直觉**
- **SQLite 驱动切换实测否决**：mattn（cgo）vs modernc（纯 Go）批量写
  基准——modernc 慢 38%（cgocall 边界开销 < 纯 Go 翻译引擎执行劣势）。
  性能判断用基准数据，不假设"无 cgo 更快"
