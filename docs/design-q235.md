# Q235 设计：借鉴 GitNexus 的六项工程实践（2026-08-22）

来源：调研 GitNexus（Agent 上下文的神经系统——预计算关系智能 + AI
代理协作工程实践）后选取六项对 codeintel 有实际价值的做法。实施顺序
按本节排列；每项标注落点文件。**本文件是设计权威，实施前先 grill。**

---

## 1. impact-before-edit：改符号前强制影响分析（落点：AGENTS.md）

**借鉴**：GitNexus 强制「改符号前必跑 impact 上游分析；HIGH/CRITICAL
须警告用户；UNKNOWN 视为未解决」。我们开发中已人工做影响分析
（codegraph_impact / codeintel query impact），但未固化为规则。

**设计**：AGENTS.md 增加「强制流程」节，三条规则：

1. **修改任何被其他符号引用的符号前**（函数/方法/类型/字段/包级变量，
   尤其跨包使用、被调用者多的），先跑 `codeintel query impact <sym>`；
   仓库未索引时用 codegraph_impact
2. 影响评估为 HIGH/CRITICAL 的变更（调用者 ≥10 或跨模块/跨包边界），
   须在回复中明示影响面与回归策略（哪些测试/验证覆盖）
3. 影响面 UNKNOWN（符号未索引/索引陈旧）**视为未解决**——先
   `codeintel update --repo <path>` 或 `init` 补索引，不得直接改

**边界**：规则面向 AI 代理与开发者，靠流程审查保障（无自动化门禁；
不引入 CI 检查——本项目单仓库迭代节奏，门禁收益低）。

**验证**：文档审查 + 后续会话按规则执行。

---

## 2. asttool rename：符号感知重命名（落点：skills/line-limit/scripts/asttool）

**借鉴**：GitNexus「重命名禁用查找替换，必须用符号感知 rename 工具」。
我们的教训：Q232 move 脚本用行级查找替换误删代码行（rg_bfs.go func
行被删、action.go 变量行被删）——文本替换不感知符号边界。

**设计**：asttool 新增子命令：

```
asttool rename <file.go> <old> <new> [--scope pkg|file] [--dry-run]
```

- **作用域**：默认 `file`（只改单文件，最安全）；`--scope pkg` 同包
  全部文件（含 `_test.go`？——不含，测试文件单独处理防误伤；提供
  `--include-tests` 显式开启）
- **AST 感知**：基于 go/ast 遍历 **Ident 节点**（astutil.Apply）——
  字符串字面量、注释、import 路径天然不动
- **遮蔽处理**：函数体内维护作用域栈；局部声明（var/func 参数/短声明）
  遮蔽包级同名符号时，作用域内同名 ident **不替换**（仅对包级符号
  重命名生效）；局部变量重命名（old 是函数内局部）按最近声明匹配
- **声明跟随**：函数声明 + 函数体内对它的调用、类型声明 + 使用处、
  方法（Recv 绑定，`x.m()` 调用）、包级变量 + 引用处
- **冲突检测**：新名与同作用域已有声明冲突（包级重名 / 方法集内重名）
  → 报错不写文件
- **--dry-run**：输出将要修改的 行号:旧 → 新 清单，不写文件
- **写入**：全部修改收集后一次性写回；多文件（pkg scope）按文件分组

**实现要点**：不做完整 go/types 解析（工具定位轻量）；遮蔽判定用
词法作用域栈（Enter/Leave 节点收集声明）。方法重命名与包级函数重名
区分（Recv != nil）。**这是 asttool 里唯一会改写代码的子命令之外的
写操作**——rename 是安全的符号级替换（相对文本替换）。

**测试（先写）**：重命名包级函数+调用处、类型+使用处、方法+调用处、
局部变量（含参数）、遮蔽场景（局部 shadow 包级→不替换）、字符串/注释
含同名文本→不动、新名冲突→报错、--dry-run 不改文件、--scope pkg
跨文件、--scope file 不碰其他文件。

**验证**：asttool 单测 + 在 codeintel 自身重命名一个符号后 make test
全绿。

---

## 3. Runbook：故障模式速查表（落点：docs/runbook.md）

**借鉴**：GitNexus「Signs 故障模式表」（现象 → 处理 → 原因/预防），
把散落运维经验成文。我们目前经验散在会话记忆/交接文档里，每次踩坑
重新发现。

**设计**：docs/runbook.md——表格形式（现象 / 处理 / 原因与预防），
初版收录（全部来自本仓库真实踩坑）：

| # | 现象 | 处理 | 原因与预防 |
|---|---|---|---|
| 1 | go test 链接失败 No space left on device | `rm -rf /tmp/go-build*`；`TMPDIR=~/.tmp-build go build` | /tmp tmpfs 配额满 |
| 2 | 大仓库 reindex 后 db 损坏（WAL） | 后台运行 + 轮询日志，勿强杀 | 强杀写进程损坏 WAL |
| 3 | go2o serve 占用 8096，e2e 起不来 | 停 go2o serve 再跑 make e2e-fixture | 端口冲突 |
| 4 | pgrep -f 匹配自身自杀 | 用 `pgrep -x` + kill | -f 匹配整条命令行 |
| 5 | schema version mismatch | `codeintel clean --repo X --force` + init（Q235 §6 后：加表类自动补建） | schema 变更 |
| 6 | query relations --all 返回进度而非数据 | 先 `codeintel precompute relations --repo X` | Q228 进度协议：全量不再现场算 |
| 7 | go run 软链 skill 路径报 outside main module | 用仓库真实路径 go run | go module 从软链路径解析失败 |
| 8 | 查询结果陈旧（改了代码没反映） | `codeintel update --repo X` | 索引未增量更新 |
| 9 | sqlite busy / locked | 停冲突进程重试 | 单写者连接池 |
| 10 | python 脚本替换静默不生效 | 替换前 assert 旧串存在 | str.replace 找不到串不报错 |
| 11 | bash cwd 被重置 | 命令用绝对路径 | shell cwd 不持久 |
| 12 | ER 页面刷新后全图画线开关恢复 | 预期行为（Q227 不持久化） | 设计如此，非故障 |

**边界**：runbook 只收运维/环境类故障；代码语义类教训（BFS 清空语义
等）留在 field_trace.md 对应 Q 节。

**验证**：文档审查；后续踩坑补录。

---

## 4. DoD：交付验收清单 + 五轴自检（落点：AGENTS.md + docs/DoD.md）

**借鉴**：GitNexus DoD 9 大项 + AI 五轴自检（正确/可读/架构/安全/
性能）。我们把既有约定（测试先行、验证矩阵、形态矩阵、自建小示例、
改完验证后 push）成文为统一验收清单。

**设计**：docs/DoD.md（AGENTS.md 引用）——交付前逐项自查：

1. **测试先行**：先写测试再实现再执行（红线确认）；测试覆盖形态矩阵
   （起点 × 传递 × 写入变体）与真实路径
2. **正确性**：自建小示例（秒级）+ 真实仓库（go2o）验证；边界输入
   （空库/非法参数/超时）；幂等（重跑/增量不重复）
3. **契约兼容**：改动涉及多路径（内存 BFS / SQL 路径 / CLI / API /
   前端）时全部同步——Q212/Q218/Q234 教训：两条路径判定不一致 =
   bug
4. **架构一致**：六边形分层（domain → port → adapter）；共享逻辑放
   domain 或共享函数，不复制
5. **可读性**：文件 ≤300 行（line-limit skill）；注释说明「为什么」
   非「是什么」
6. **性能**：大仓库（go2o 15 万节点）实测；查询设超时；BFS/递归有
   深度上限
7. **安全**：无密钥/私有仓库名落库；fixture 表名脱敏
8. **可回滚**：脚本操作前 dry-run；git 提交原子（一个功能一个提交）；
   文档与代码同 commit
9. **文档落档**：field_trace.md 加 Q 节；skill/AGENTS.md 同步更新
10. **推送**：验证全绿后 git push（不留未推提交）

**五轴自检**（每项变更完成后快速过一遍）：正确性 / 可读性 / 架构
一致性 / 安全性 / 性能——任一项不确定即补验证，不自评通过。

**边界**：DoD 是交付清单不是开发流程（test-first 是硬流程，DoD 是
收尾检查）；长度控制，避免形式化空转。

**验证**：文档审查 + 后续 Q 交付按 DoD 执行。

---

## 5. query context：一次调用拿全链上下文（落点：internal/action、CLI、server、field_trace.md §64）

**借鉴**：GitNexus「预计算关系智能——一次工具调用返回完整上下文，
替代多次查询链，省 token、小模型可用」。我们现有查询分散：query
summary（主链）/ symbol（调用者被调用者）/ value-trace（值流）/
fields（字段摘要）/ 动态派发候选——AI 代理要 4+ 次调用拼上下文。

**设计**：新增聚合查询 `query context <节点> --repo <path>`（与
GitNexus context 工具同名不同实现——我们是查询编排，不是索引期
预计算）：

```json
{
  "symbol": {"id": "...", "kind": "function", "name": "(Manager).Run", "file": "manager/run.go:120"},
  "callees": [{"id": "...", "name": "loadOrder", "line": 135, "kind": "function"}],
  "callers": [{"id": "...", "name": "(App).Start", "line": 42, "kind": "function"}],
  "fields": {"direct_read": [...], "direct_write": [...], "indirect_write": [...]},
  "chain": [{"name": "...", "kind": "...", "access": "write", "line": 12, "dir": 1, "condition": "if order.Status == paid"}],
  "traces": [{"func": "...", "rows": [...]}],
  "dispatch": [{"candidate": "...", "confidence": 0.9, "register": "...", "line": 80}]
}
```

- **字段**：symbol（节点详情）→ callees/callers（depth 1）→ fields
  （函数字段摘要）→ chain（summary 跨层主链，带路径条件标注）→
  traces（值流全链，默认 depth 4）→ dispatch（动态派发候选，仅接口
  类型节点）
- **实现**：action 层新增 `Context(id)` 编排——全部复用现有 repo 查询
  （GetSymbol/GetFunctionFlows/GetValueTrace/GetDispatchEdges/
  TraceConditions 等），**不新增图查询逻辑**；一个 action = 多次查询
  的组合（可并行：无依赖查询并发）
- **入口**：CLI `query context`（默认 JSON）；server `/api/context`
  （前端信息栏可后续接入——本期不做前端）
- **错误处理**：符号不存在 → 报错；单个子查询失败（如超时）→ 该字段
  置 null + 整体成功（部分降级，参照 Q163 摘要超时降级先例）
- **性能**：单次 context 调用最多 6 个查询，均毫秒级（有索引）；
  深 trace 可参数化 `--depth` 控制

**测试（先写）**：action 层——fixture 构造（函数 + 调用者 + 被调用者
+ 字段读写 + 值流链 + 接口派发）→ Context 返回完整结构字段齐全；
部分查询失败 → 字段 null 不整体失败；CLI 输出 JSON 可解析；server
端点返回 200。**回归**：现有 summary/symbol/fields 查询不回归（复用
底层，无行为变化）。

**验证**：make test + go2o 实测 context 一次调用输出 vs 多次查询拼接
一致性。

---

## 6. schema 自动迁移：幂等补建替代手动 clean（落点：internal/infrastructure/sqlite/db.go + field_trace.md §64）

**借鉴**：GitNexus schemaVersion → schemaFingerprint 摘要驱动自动
重建。**教训**：新旧二进制交替导致反复重建——每索引固定版本。我们
的现状：SchemaVersion=4 + PRAGMA user_version 校验，v≠4 报错要求
clean；但我们的 schema 演进 v1→v4 全是**加法**（新表/新索引），
configSchema 已有幂等补建先例（Q220c）——clean 是多余的。

**设计**：加法自动迁移，减法仍手动：

1. **打开时总是幂等执行 schema DDL**（CREATE TABLE IF NOT EXISTS +
   CREATE INDEX IF NOT EXISTS——现状仅 v==0 时执行）——旧库（v1–v3）
   自动补建缺失表/索引，无需 clean
2. **结构齐全性检查**（替代「版本号相等」判断）：校验核心表存在 +
   关键列存在（nodes 9 列 / edges 8 列 / build_metadata 9 列 /
   function_field_summary 6 列 / summary_origins 6 列 /
   relation_candidates 7 列 / relation_rules 6 列 /
   relation_progress 5 列）；缺列（破坏性列变更）→ 报错要求 clean
3. **不存 fingerprint**——规避 GitNexus 交替二进制反复重建教训：
   检查是「期望结构 ⊆ 实际结构」子集判断，旧二进制打开新库同样
   通过（新表不碍旧代码），无写入状态 → 无反复
4. user_version 保留为「初次建库」标记（v==0 建全量），不再做严格
   相等校验

**边界**：列类型变更/列删除/约束变更 → 仍 clean（幂等 DDL 无法表达
减法）；分析逻辑版本（SSA 规则/relations 判定）变更仍靠
build_id/relation_progress 自动失效（已有机制，不纳入 schema 迁移）。

**测试（先写）**：构造 v1 旧库（仅建旧表 + user_version=1）→ Open →
自动补建全部新表/索引 + 查询可用；构造缺列库（nodes 缺一列）→ Open
报错提示 clean；全新库正常建表。**回归**：现有 clean/reindex 流程
测试不回归。

**验证**：make test + 用真实旧库（如 go2o 旧版本 db 或构造）验证
打开即补建。

---

## 实施顺序与依赖

1. §3 runbook + §4 DoD（纯文档，先落地建立基线）
2. §1 AGENTS.md 强制流程（文档）
3. §6 schema 自动迁移（小而独立，有测试）
4. §2 asttool rename（工具 + 测试）
5. §5 query context（功能扩展，依赖面大，最后）

每项完成：验证 + field_trace.md 落档 + git push（按 DoD §4 执行）。
