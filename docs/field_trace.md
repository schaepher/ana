# codeintel 字段级数据追溯（Field Trace）设计文档（v2.2 适配版）

**项目名称**：codeintel（module `github.com/schaepher/codeintel`）——Go 代码库智能索引系统
**能力**：字段级数据追溯（Field Trace）
**版本**：v2.2（由 go-cpg v1.0 设计文档适配，2026-08-13）
**状态**：适配完成，进入实现阶段
**变更说明**：原 go-cpg 设计（独立 CLI 工具 + SSA 构建器 + 自有 SQLite 存储 + 自有 CLI）整体适配为 codeintel 六边形架构下的**新增适配器**（IndexerPort 实现），复用现有 nodes/edges 存储、canonical ID、置信度与降级机制；接替 2026-08-13 已移除的 Joern 数据流适配器（docs/TD.md §12.7）。凡与 go-cpg 原设计冲突处，以本版为准。

---

## 目录

1. [项目背景与目标](#1-项目背景与目标)
2. [核心用户场景](#2-核心用户场景)
3. [总体架构](#3-总体架构)
4. [数据模型（节点/边/Canonical ID）](#4-数据模型节点边canonical-id)
5. [存储层：SQLite 设计](#5-存储层sqlite-设计)
6. [核心算法](#6-核心算法)
7. [外部依赖与摘要系统](#7-外部依赖与摘要系统)
8. [模块划分与目录结构](#8-模块划分与目录结构)
9. [性能与降级策略](#9-性能与降级策略)
10. [实现路线图](#10-实现路线图)
11. [测试策略](#11-测试策略)
12. [附录：决策记录](#12-附录决策记录)
14. [实现补充记录（v2.3）](#14-实现补充记录v23)

---

## 1. 项目背景与目标

### 1.1 动机

codeintel 已提供符号导航（SCIP）、调用图与影响分析（AST/go/packages）、Git 历史（TD.md），但缺少**字段级别的数据流向**能力：结构体字段的读取、修改、传递是代码审查、重构与故障排查的核心需求，跨函数追踪字段来源（产生点）与去向（使用点）正是当前缺失的一环。

此前数据流方案为 Joern（joern-parse gosrc2cpg + joern-slice），**已于 2026-08-13 移除**：外部 CLI 依赖重、仅产出方法内 REACHING_DEF（跨方法参数流无法覆盖）、radar 全量耗时 8-10 分钟。本设计以纯 Go 实现（`go/ssa` + `go/pointer`，x/tools 已在依赖中）接替，与 codeintel 现有技术栈一致，无新增第三方依赖。

### 1.2 目标

- **核心能力**：
  ① 给定任意函数，列出其直接/间接读取和编辑的所有结构体字段（全路径 `a.b.c`，类型限定）；
  ② 给定任意字段，反向追溯其所有产生点（赋值来源），正向追溯其返回后所有使用点（消费位置）。
- **v1 非目标**：不提供漏洞扫描、安全规则匹配、污点传播、反射分析；channel 元素收发不追踪（map/slice/array 元素追踪见 §14.11）。
- **v2 计划**：增量更新、map/slice 等复合类型元素追踪（MCP serve 已取消
  ——AI 代理直接使用 CLI 查询命令，§19）。

### 1.3 适用规模

- 目标代码库：**10 万～50 万行 Go 代码**（中型项目，约 200～500 个包）。
- 分析入口：**单个 Go Module**（含 `go.mod`）；`go.work` 场景下报错并提示用户进入具体模块目录（与现状一致）。
- 与现有能力的关系：字段追溯是**独立分析维度**（`tool_source="ssa"`），与 SCIP 符号/AST 调用/Git 历史**视角互补、共存**（TD.md 5.3）。

---

## 2. 核心用户场景

| 场景 ID | 用户动作 | 期望结果 |
| :--- | :--- | :--- |
| S1 | `codeintel query fields --func symbol:go:github.com/x/payment:(Service).Process` | 列出该函数直接/间接读写的所有字段，按 `direct_read` / `direct_write` / `indirect_write` 分组，显示类型路径、实例路径、行号、代码片段。 |
| S2 | `codeintel query trace-backward --field github.com/x/payment.Request.Amount --func symbol:go:github.com/x/payment:(Service).Process` | 追溯该字段在 `Process` 函数中的来源（产生点），输出树形路径（缩进 + 边类型 + 节点名 + 行号）。 |
| S3 | `codeintel query trace-forward --field github.com/x/payment.Request.Amount --func symbol:go:github.com/x/payment:(Service).Process` | 以字段对象/引用为追踪目标，追溯该字段在 `Process` 返回后（调用方）的后续读写，输出调用链缩进树。 |
| S4 | `codeintel export --out=analysis.json` | 生成双层索引 JSON（函数→字段，字段→函数），用于 IDE 或脚本二次分析。 |
| S5 | ~~交互式 shell~~（**取消**） | 交互入口 = AI 代理直接使用 CLI 查询命令（MCP serve 已取消，§19）。 |

---

## 3. 总体架构

字段追溯实现为六边形架构中的**新增适配器**，挂载到 orchestrator 并行管道，与现有适配器共享存储与查询层：

```
┌───────────────────────────────────────────────────────────────────┐
│                          codeintel CLI                            │
│            init（全量构建）/ serve / query / export / clean        │
└───────────────────────────────┬───────────────────────────────────┘
                                ▼
┌───────────────────────────────────────────────────────────────────┐
│              Orchestrator（并行适配器管道，TD.md 5.2）              │
│   ┌─────────────┐ ┌─────────────┐ ┌─────────────┐ ┌─────────────┐ │
│   │ SCIP 适配器 │ │ AST 适配器   │ │ Git 适配器   │ │ SSA 适配器   │◀┼── 本次新增
│   │ 符号 conf1.0│ │ 调用 conf0.8│ │ 历史 conf1.0│ │ 字段追溯     │ │
│   └─────────────┘ └─────────────┘ └─────────────┘ └──────┬──────┘ │
└──────────────────────────────────────────────────────────┼────────┘
                                                           ▼
┌───────────────────────────────────────────────────────────────────┐
│                       Canonicalizer + SQLite                      │
│   nodes / edges / function_field_summary（新增）/ build_metadata   │
└───────────────────────────────┬───────────────────────────────────┘
                                ▼
┌───────────────────────────────────────────────────────────────────┐
│              Query（internal/cli + sqlite.Repo 递归 CTE）           │
│   fields / trace-backward / trace-forward / export                │
└───────────────────────────────────────────────────────────────────┘
```

**SSA 适配器内部流程**：
1. `go/packages` 加载 workspace（复用现有 AST 适配器的加载模式，`--skip-tests` 语义一致）。
2. `go/ssa` + `ssautil` 构建完整 SSA IR（Program/Packages/Functions），保留源映射（行号、文件路径）。
3. 遍历 SSA 指令提取 `Field` / `FieldAddr` / `Store`，生成 `field_access` 节点与 `data_flows_to` 边。
4. 轻量别名分析（§14.8，Q80）：过程内 may 传播 + 跨函数参数/返回直通，生成 `alias` 边与间接写排除集。
5. 跨过程 `argument` / `returns` 边、间接写分析与 `indirect_write` 边。
6. 内置/用户摘要应用（外部函数），生成虚拟字段节点。
7. 预计算 `function_field_summary` 表。
8. 流式 emit `Item{Node, Fact}`，由 orchestrator 分批（1000 条/事务）写入 SQLite。

失败时由 orchestrator 标记降级（degraded），不影响其他适配器数据（TD.md 9.2）。

---

## 4. 数据模型（节点/边/Canonical ID）

### 4.1 节点（Node）

复用现有 `nodes` 表（`id TEXT PRIMARY KEY` = Canonical ID，`kind`、`name`、`file_path`、`line_start/end`、`properties JSON`）。v2.2 新增节点类型：

| `kind` 值 | 说明 | 关键属性（properties JSON） |
| :--- | :--- | :--- |
| `field_access` | 结构体字段访问（实例槽） | `full_path`（类型限定路径，如 `github.com/x/payment.Request.Amount`）、`instance_path`（如 `req.Amount`）、`access_kind`（`read`/`write`）、`type_string`、`code_snippet`、`func_id`（所属函数 canonical id）、`is_external` |
| `ssa_value` | 所有 SSA 值（参数、接收者、局部、全局、字面量、Phi、Alloc、Call 返回值等） | `origin_kind`（`param`/`local`/`receiver`/`global`/`literal`/`phi`/`alloc`/`call_result` 等）、`ssa_op`、`type_string`、`func_id` |
| `external_summary` | 外部库摘要函数 | `summary_json`（声明读/写字段模式） |
| `parameter` | 函数/方法签名参数（含接收者） | `type_string`、`index`（接收者为 -1）、`receiver`、`func_id` |
| `result` | 函数/方法返回值（多返回按索引） | `type_string`、`index`、`func_id` |

**不新增、复用现有**：`FILE` / `PACKAGE` / `FUNCTION` / `METHOD` / `STRUCT`（原设计的 `TYPE` 由 struct 承担，字段列表已由 AST 适配器写入 `properties.fields`）。

**废弃原设计节点**：
- `CALL_SITE` → 调用点信息并入 `calls` 边 metadata（调用类型、行号、可能目标列表），不建独立节点。
- `TYPE` → 由现有 struct 节点承担。

**Canonical ID 规则（新增决策 Q68）**：
- 函数作用域内的实例节点（`field_access` / `ssa_value`）：`symbol:go:<import_path>:<func_name>#<slot>`
  - `field_access` 的 slot = 实例路径（如 `req.Amount`）
  - `ssa_value` 的 slot = SSA 名（如 `t0`）；展示名用 `instancePath` 还原源码变量链（Q73 补充）
  - `parameter` 的 slot = `param.<name>`（接收者 `param.recv.<name>` 防重名）
  - `result` 的 slot = `result`（多返回 `result.<idx>`）
  - `func_name` 与函数节点一致（方法统一 `(T).method`，值/指针接收者不区分）
  - 例：`symbol:go:github.com/x/payment:(Service).Process#req.Amount`
- `external_summary`：`symbol:go:<import_path>:<func_name>`（外部函数不建 FUNCTION 节点，同格式不同 kind，无冲突）。
- 实例节点在 `properties.func_id` 冗余记录所属函数 canonical id，查询定位用（替代原 `FUNCTION_CONTAINS` 边）。

**字段访问节点**：每个 SSA `Field` / `FieldAddr` 指令生成一个 `field_access` 节点；`access_kind` 根据指令性质确定（见 6.1）。同一源码位置的复合读写（如 `x.a = x.a + 1`）生成两个独立节点，分别标记 `read` 和 `write`。

### 4.2 边（Edge）

复用现有 `edges` 表（`source_id`/`target_id` TEXT、`kind`、`tool_source`、`confidence`、`metadata JSON`）。v2.2 新增边类型：

| `kind` 值 | 方向 | 含义 | 置信度 |
| :--- | :--- | :--- | :--- |
| `data_flows_to` | 定义 → 使用 | SSA Def-Use 链（直接值传递，含 extract 拆解）。**复用现有 kind**，`tool_source="ssa"` 与 Joern 时代的 conf 0.7 语义区分 | 1.0 |
| `argument` | 实参节点 → 形参节点 | 调用点实参 → 被调函数形参（跨过程） | 1.0 |
| `returns` | 被调函数返回值 → 调用点接收变量 | 跨函数返回赋值；多返回值返回 tuple `ssa_value`，extract 经 `data_flows_to` 拆解 | 1.0 |
| `alias` | 源变量 → 目标变量 | 指针分析结果（`property='may_alias'`），仅存储参与字段访问的变量 | 0.8 |
| `indirect_write` | 调用者函数 → 被调函数/虚拟字段节点 | 调用者通过被调函数间接修改字段；项目内函数指向被调函数，外部摘要指向虚拟字段节点 | 1.0 |
| `phi_operand` | Phi 节点 → 前驱值 | SSA Phi 的每个分支输入 | 1.0 |
| `summary_io` | 外部摘要函数 → 字段路径 | 声明该函数读/写某字段（摘要传播用） | 0.8 |
| `has_param` | 函数 → 签名参数节点 | 参数/返回在图内展开（接收者含在内） | 1.0 |
| `has_result` | 函数 → 返回值节点 | 同上 | 1.0 |

**复用现有、不新增**：
- `calls`：SSA 解析出的调用关系并入现有 calls 边（调用类型 `static`/`interface`/`function_value`/`closure`/`goroutine`/`defer` 标注于 metadata；动态调用可为多条可能目标边）。
- `FUNCTION_CONTAINS` **不实现**：节点 `properties.func_id` 直接关联所属函数，查询无需包含边。
- `FIELD_CONTAINS` **不实现**：struct 字段列表已由 AST 适配器写入 `properties.fields`（TD.md 12.4）。

**同义边合并**：SSA 适配器与 AST 适配器都产出 `calls` 边时，按现有 UNIQUE(source, target, kind) 保留最高置信度（TD.md 5.3）；SSA 的 `data_flows_to` 与 AST 的 `uses`/`passes_to` 是不同维度，共存不视为冲突。

---

## 5. 存储层：SQLite 设计

### 5.1 数据库与驱动

- 数据库：`.codeintel/codeintel.db`（现有，`--repo` 定位）。
- 驱动：`github.com/mattn/go-sqlite3`（现有；不引入 modernc.org/sqlite）。
- 已启用：WAL、外键、`_busy_timeout`（db.go 直连参数）。
- 全量构建语义与现状一致：`codeintel init` 清库重建（无 `--rebuild` 开关）。

### 5.2 表定义

现有 `nodes` / `edges` / `build_metadata` 表不动。新增：

```sql
-- 函数字段摘要表（构建时预计算，加速 S1 查询）
CREATE TABLE function_field_summary (
    function_id TEXT NOT NULL,       -- nodes.id（函数 canonical id）
    access_kind TEXT CHECK(access_kind IN ('direct_read','direct_write','indirect_write')),
    field_path TEXT NOT NULL,        -- 类型限定路径（同 field_access.full_path）
    instance_path TEXT,              -- 冗余列：加速 S1 输出（对齐原设计 6.2）
    line_start INTEGER,
    code_snippet TEXT,
    FOREIGN KEY(function_id) REFERENCES nodes(id) ON DELETE CASCADE
);
CREATE INDEX idx_summary_func_access ON function_field_summary(function_id, access_kind);
CREATE INDEX idx_summary_field ON function_field_summary(field_path);

-- 表达式索引：full_path / func_id 存于 properties JSON，为 S2/S3 起点定位建索引
CREATE INDEX idx_nodes_field_path ON nodes(json_extract(properties, '$.full_path'));
CREATE INDEX idx_nodes_func_id ON nodes(json_extract(properties, '$.func_id'));
```

**Schema 版本管理**：`PRAGMA user_version` 1 → 2。无自动迁移（TD.md 10.2），版本不匹配时报错提示 `codeintel clean` 重建。

### 5.3 并发与事务

- 构建阶段批量插入（1000 条/事务），单写者——现有 orchestrator 机制不变。
- 查询阶段只读，递归 CTE 带深度限制。

---

## 6. 核心算法

### 6.1 SSA 指令到 FIELD_ACCESS 映射规则

构建器遍历 SSA 指令，按下述规则生成 `field_access` 节点和 `data_flows_to` 边（**与原 go-cpg 设计一致，保留**）：

| SSA 指令 | 生成节点 | access_kind | 边连接 |
| :--- | :--- | :--- | :--- |
| `FieldAddr`（取字段地址） | `field_access` 节点 | `write`（通常用于后续 Store） | `data_flows_to` 从基地址 `ssa_value` 连到该字段节点 |
| `Field`（读字段） | `field_access` 节点 | `read` | `data_flows_to` 从字段节点连到指令结果 `ssa_value` |
| `Store`（写入） | 不新建节点 | 若目标为已有 `field_access`（FieldAddr 生成）确保 `access_kind='write'`；目标非字段访问则忽略 | `data_flows_to` 从写入值 `ssa_value` 连到目标字段节点 |
| 复合读写（如 `x.a = x.a + 1`） | `Field` 与 `FieldAddr` 各生成一个节点 | `read` 和 `write` | 分别对应上述边 |

**实现注（2026-08-13，x/tools v0.26）**：当前 go/ssa 表示中字段读也经 `FieldAddr` 取址 + `UnOp(MUL)` 解引用（`Field` 指令仅出现在非可寻址值，如调用结果）。读写按 `FieldAddr` 的**使用方式**判定（被 `Store` 使用→write；被 `UnOp(MUL)` 解引用→read；两者同时→复合读写两个节点），与表中三指令映射等价。

- `full_path` 生成：基于 SSA 值/表达式的**静态类型**解析类型声明包路径和类型名，拼接字段链（如 `pkg.Request.Amount`）。嵌套字段递归解析中间结构体类型。静态类型解析失败时回退源码字面量路径并记录警告。
- `instance_path` 生成：基于源码变量名和字段链（如 `req.Amount`，或 `a.b.c`）。全局变量的 `full_path` 与 `instance_path` 均为 `pkg.VarName`。
- 嵌入字段：`full_path` 始终使用**声明字段的结构体类型路径**；`instance_path` 保留源码访问形式。
- 类型别名：`go/types.Unalias` 解析为原始类型后生成路径。
- 未导出字段：与导出字段同等对待。
- 生成代码：识别 `// Code generated ... DO NOT EDIT.` 标记，`properties.generated=true`，默认仍分析。

### 6.2 函数 → 字段读取/编辑（场景 S1）

**输入**：函数 canonical id（`--func`）。  
**输出**：按 `direct_read`、`direct_write`、`indirect_write` 分组的字段列表。

**实现**：直接查询构建期预计算的 `function_field_summary`，无需动态遍历调用图。查询步骤：

1. `function_field_summary WHERE function_id = ?`。
2. `access_kind` 映射为输出分组。
3. 冗余列（`instance_path`/`line_start`/`code_snippet`）直接输出，无需二次 join。

**间接写范围**：任意深度调用链——从调用者函数出发沿调用图可达的所有被调函数，若其内部存在 `write` 的字段访问节点，且该字段通过指针别名与调用者作用域内变量关联，标记为间接写。构建时预计算并写入摘要表。

**输出格式**（CLI 表格，对齐 Q18）：`GROUP / TYPE_PATH / INSTANCE_PATH / LINE / CODE`。

### 6.3 字段 → 产生点追溯（反向，场景 S2）

**输入**：字段全路径（`--field`），入口函数 canonical id（`--func`）。  
**输出**：从产生点到该字段的完整路径树（缩进格式，每条路径包含节点和边类型）。

**SQL 递归 CTE**（参数化模板，递归 `UNION` 去重 + 深度限制，风格对齐现有 repo.go 模板）：

```sql
WITH RECURSIVE def_trace(id, depth, path_nodes, edge_kinds) AS (
    -- 起点：入口函数内的目标字段节点
    SELECT n.id, 0, json_extract(n.properties, '$.instance_path'), ''
    FROM nodes n
    WHERE n.kind = 'field_access'
      AND json_extract(n.properties, '$.full_path') = ?
      AND json_extract(n.properties, '$.func_id') = ?
    UNION
    -- 反向遍历 data_flows_to / argument / returns / alias / phi_operand
    SELECT e.source_id, d.depth + 1,
           d.path_nodes || ' -> ' || n_prev.name,
           d.edge_kinds || ',' || e.kind
    FROM edges e
    JOIN def_trace d ON e.target_id = d.id
    JOIN nodes n_prev ON e.source_id = n_prev.id
    WHERE e.kind IN ('data_flows_to', 'argument', 'returns', 'alias', 'phi_operand')
      AND d.depth < ?   -- 默认 8，--max-depth 可调
)
SELECT id, depth, path_nodes, edge_kinds
FROM def_trace
ORDER BY depth DESC;
```

**输出格式化**：按深度渲染为缩进树，每行 `缩进 + 边类型前缀（如 ← data_flows_to）+ 节点名 + (行号)`。多条路径分行显示，路径间空行；不合并重复前缀。

### 6.4 字段 → 后续使用追踪（正向，场景 S3）

**追踪对象**：字段对象/引用（而非仅返回值）。从入口函数返回后，继续沿 `alias`/`data_flows_to`/`returns`/`calls` 正向，直到下一次 `field_access`（读或写）。

**输入**：字段全路径，入口函数 canonical id。  
**输出**：从函数返回后到调用链下游的使用路径（缩进树）。

**实现**：递归 CTE 正向遍历。起点为入口函数内匹配 `full_path` 的 `field_access` 节点（同 S2 起点）；沿 `data_flows_to`（正向 source→target）、`alias`、`returns`、`calls` 向外扩展，同时追踪承载该字段的变量/指针。路径中遇到 `field_access` 且 `full_path` 与目标匹配，作为使用点输出。深度默认 8（`--max-depth` 可调）。递归 `UNION` 去重防环。

---

## 7. 外部依赖与摘要系统

### 7.1 使用的库（均为现状已有或 x/tools 既有）

- `golang.org/x/tools v0.26.0`（已在 go.mod）：`go/packages`、`go/ssa`、`go/ssa/ssautil`、`go/callgraph`。
- `github.com/mattn/go-sqlite3`（已有）。
- **无新增第三方依赖**（不引入 modernc.org/sqlite）。
- **注意**：`go/pointer` 已于 x/tools v0.26 移除。Phase 3 的别名分析（Q5 的精确/快速模式）改为自研轻量方案：`callgraph.RTA`（仍在）+ 过程内 must-alias 近似，不降级 x/tools。

### 7.2 项目内外划分

以 `go.mod` 的 `module` 路径为前缀（复用现有 `isInModule` 逻辑）：以该前缀开头的包视为**项目内**，精确分析；其余视为**外部依赖**，仅使用摘要或跳过。

### 7.3 内置摘要（标准库常见模式）

对以下标准库函数提供手写摘要，声明它们会读/写传入结构体的哪些字段：

- `encoding/json.Unmarshal(data []byte, v any)`：写入 `v` 的所有字段（递归）。
- `fmt.Printf(format string, args ...any)`：读取所有 `args` 的字段（保守策略）。
- `net/http` 相关：`Request` 的 `Body`、`Header`、`Form` 等字段的读/写模式。
- `database/sql` 的 `Rows.Scan(dest ...any)`：写入 `dest` 的指向值。
- `context.Context`：视为透明传递，不分析内部。

### 7.4 用户自定义摘要

用户可在模块根目录放置 `field-summary.yaml`（原 go-cpg 的 `cpg-summary.yaml` 更名），格式不变：

```yaml
summaries:
  - func: "github.com/mycorp/internal/db.InsertUser"
    reads: ["user.ID", "user.Name"]        # 读取这些字段（类型限定路径）
    writes: ["user.CreatedAt"]             # 写入这些字段
    param_index: 1                         # 操作第几个参数（0 为接收者）
```

解析规则（修订 Q59）：
- 文件必须位于模块根目录，文件名固定为 `field-summary.yaml`。
- 若文件不存在，使用内置摘要。
- **若存在但 YAML 解析失败：跳过摘要并输出警告，构建降级（degraded），不中止**（对齐 TD.md 9.2 降级矩阵，替代原"退出码 1 中止"）。
- 同一函数重复定义视为错误（跳过该函数定义并警告）。
- 字段路径使用类型限定路径；与实际参数类型不匹配时输出警告并忽略该条摘要，不中断构建。

### 7.5 摘要应用机制

构建器遇到调用带摘要的外部函数（项目外）时：
1. 根据摘要 `param_index` 和实际参数类型，在调用点生成**虚拟 `field_access` 节点**（`is_external=1`，`access_kind` 按摘要声明为 `read`/`write`）。
2. 生成 `external_summary` 节点（若尚未存在）与 `summary_io` 边（摘要节点 → 虚拟字段节点）。
3. 摘要声明写入时生成 `indirect_write` 边（调用者函数 → 虚拟字段节点），保证间接写可查询。

项目内函数间接写**不生成虚拟节点**，通过 `indirect_write` 边从调用者函数指向被调函数（见 6.2），查询时沿该边收集被调函数内实际写入的字段节点。

---

## 8. 模块划分与目录结构

```
codeintel/
├── cmd/codeintel/
│   └── main.go                 # CLI 入口（现状）
├── internal/
│   ├── domain/                 # 现状 + 新增 EntityKind/FactKind/ToolSSA 常量
│   ├── orchestrator/           # 现状；适配器列表加入 ssa.Adapter
│   ├── infrastructure/
│   │   ├── scip/  ast/  git/   # 现状
│   │   ├── sqlite/             # 现状；repo.go 增加 S1/S2/S3 查询、summary 写入
│   │   └── ssa/                # ★ 新适配器（替代已删除的 joern/）
│   │       ├── adapter.go           # IndexerPort 实现：go/packages + go/ssa 构建，主流程
│   │       ├── field_extractor.go   # Field/FieldAddr/Store → field_access
│   │       ├── alias_builder.go     # go/pointer → alias 边
│   │       ├── indirect_writer.go   # 间接写分析与 indirect_write 边
│   │       ├── summary_applier.go   # 内置/用户摘要 → 虚拟节点
│   │       ├── function_summary.go  # function_field_summary 预计算
│   │       └── testdata/            # 测试用小型 Go 模块（对齐 ast 适配器测试方式）
│   ├── cli/                    # 现状；query 增加 fields/trace-backward/trace-forward
│   │   └── export.go           # 新增 S4 导出命令
│   └── server/                 # /api/expand 图探索 + /api/flows 字段数据流文本树
├── integration/                # 现状；扩展字段追溯端到端
├── docs/TD.md                  # v2.0 设计文档 + §12 补充记录（v2.2 追加本能力）
```

**不保留**：`pkg/cpg` 公共 API（原 go-cpg §8.1）——查询入口为 CLI（MCP 已取消，§19），无独立 pkg 导出层。

---

## 9. 性能与降级策略

| 问题 | 策略 |
| :--- | :--- |
| **别名分析成本** | 轻量自研（§14.8）：每函数 200 alloc 上限，超限跳过该函数；无 pointer/RTA 选项（go/pointer 已移除） |
| **节点数量膨胀**（SSA_VALUE 每指令一个） | 仅保留参与字段访问的 `ssa_value`（def-use 链两端），与 alias 粒度一致（Q53 同思路）；全量保留会图爆炸 |
| **SQLite 文件过大** | 全量构建后执行 `VACUUM`（cmdInit）；`code_snippet` 限长 500 字符；表达式索引而非冗余列（摘要表冗余列除外，其为查询加速设计） |
| **递归 CTE 深度爆炸** | 深度限制（默认 8，`--max-depth` 可调）；递归 `UNION` 去重防环 |
| **构建错误** | 适配器失败 → degraded（TD.md 9.2 矩阵 Joern 行替换为 SSA 行）；不中止其他适配器数据 |
| **SSA/指针缓存** | 不引入序列化缓存（现状无缓存机制；增量构建实现时再评估） |

---

## 10. 实现路线图（v2.2，约 4 周）

| 阶段 | 里程碑 | 主要工作 |
| :--- | :--- | :--- |
| **Phase 1** | 适配器骨架 | go/packages + go/ssa 加载，FUNCTION/`ssa_value` 节点（func_id 属性），orchestrator 挂载 |
| **Phase 2** | 字段提取 | Field/FieldAddr/Store → `field_access` + `data_flows_to` 边，full_path/instance_path 规则 |
| **Phase 3** | 跨过程 | `argument`/`returns`/`alias`/`phi_operand` 边、间接写分析、`function_field_summary` 预计算 |
| **Phase 4** | 查询 CLI | `query fields` / `trace-backward` / `trace-forward`（递归 CTE）、`export` 命令 |
| **Phase 5** | 摘要与收尾 | 内置摘要、`field-summary.yaml`、测试（单测 + 集成）、性能验证 |

### v2 计划（与 TD.md 对齐）
- ~~MCP serve~~（已取消，§19：AI 直接使用 CLI）
- 增量更新（`--update` / Git Hook）
- map/slice/array/channel 元素追踪
- 泛型实例化完整支持

---

## 11. 测试策略

- **单元测试**：`internal/infrastructure/ssa/` 用 `testdata/` 小型 Go 模块（对齐 ast 适配器：临时模块 + go/packages，不依赖外部工具），覆盖映射规则、full_path/instance_path、嵌入字段、跨过程、摘要应用。
- **集成测试**：integration 套件扩展——init 构建后执行 `query fields` / `trace-backward` / `trace-forward` / `export` 端到端断言（对齐现有 TestCLIFullFlow 模式）。
- **SQL 查询测试**：单独测试递归 CTE 在 go-sqlite3 上的正确性（深度、去重、环、深度上限）。
- **性能基准**：入口可达子图模式下的构建时间与 DB 大小记录于 TD.md §12 补充记录。
- **前端 e2e（playwright）**：`make e2e E2E_REPO=<仓库>`（默认 ../radar）——参数/返回展开、节点配色、字段数据流文本树、定义顺序、所属函数显示、桥边跳转等 22 项断言（e2e/field-trace-e2e.mjs）。

---

## 12. 附录：决策记录

### 12.1 保留的原设计决策（go-cpg Q1–Q67，未修订）

SSA 语义与映射类决策全部保留：Q1（SSA_VALUE 统一建模）、Q2（函数作用域关联——以 func_id 属性实现）、Q3（full_path+instance_path）、Q4（access_kind）、Q5（pointer 默认/quick 回退）、Q6（S3 正向语义）、Q7（Go 版本与构建约束）、Q11–Q16、Q18（CLI 表格列）、Q21–Q26（泛型、嵌入字段、函数标识符）、Q28（树形输出）、Q29–Q32（读写分组、间接写深度、项目内判定）、Q34（全局变量路径）、Q35（摘要不匹配忽略）、Q36（间接写机制）、Q38（TYPE→struct 承担）、Q40（预计算摘要表）、Q41（缓存——见 Q33 修订）、Q42（schema 版本）、Q43–Q46、Q48–Q58、Q61–Q67（多返回值、defer、函数值、platform、Unalias 等）。

### 12.2 修订的决策（适配 codeintel 现状）

| 决策编号 | 原选择 | 修订为 | 理由 |
| :--- | :--- | :--- | :--- |
| Q8 | modernc.org/sqlite | `mattn/go-sqlite3`（现有） | 现状依赖 |
| Q9 | v1 仅 --rebuild | 对齐现状：`init` 清库重建 | 现有全量构建语义 |
| Q10 | `go-cpg analyze/trace/export` | `codeintel query fields/trace-backward/trace-forward` + `export` | 现有 CLI 形态 |
| Q17 | JSON 导出 | `codeintel export --out=analysis.json`，结构不变 | 命令入口调整 |
| Q33 | `.cpg-cache/` gob 缓存 | 不引入缓存 | 现状无缓存机制 |
| Q47 | 退出码 0/1/2/3/4 | 0/1/2（现状） | 对齐现有 CLI |
| Q59 | `cpg-summary.yaml`，解析失败中止 | `field-summary.yaml`，解析失败降级（degraded） | 对齐降级矩阵 |
| Q60 | testdata 五项目 | `ssa/testdata` 模块 + integration 扩展 | 对齐现有测试方式 |
| Q20 | 无 S5 | S5 取消，交互入口 = CLI 查询命令（MCP 已取消，§19） | 2026-08-15 修订 |

### 12.3 新增决策（Q68–Q73）

| 决策编号 | 决策 | 选择 | 理由/说明 |
| :--- | :--- | :--- | :--- |
| Q68 | 实例节点 canonical ID | `symbol:go:<pkg>:<func>#<slot>`；`properties.func_id` 冗余所属函数 | 字段访问/SSA 值非全局唯一符号，需函数限定 |
| Q69 | SSA 事实置信度 | def-use/argument/returns/phi/indirect_write = 1.0；alias/summary_io = 0.8；`tool_source="ssa"` | 确定性事实与 SCIP 同级权威；指针/摘要为推断 |
| Q70 | 边 kind 复用 | `data_flows_to` 复用（tool_source 区分语义）；`calls` 复用（metadata 标调用类型）；不建 `FUNCTION_CONTAINS`/`FIELD_CONTAINS` | 避免边类型膨胀；func_id 与 properties.fields 已覆盖 |
| Q71 | 实现形态 | IndexerPort 适配器（六边形），非独立 CLI | 适配器并行/降级/存储全复用 |
| Q72 | 节点精简 | 废弃 `CALL_SITE`/`TYPE`；调用信息入 calls metadata；类型导航用 struct properties.fields | 与现有模型对齐 |
| Q73 | SSA_VALUE 范围 | 参与字段访问**或跨过程数据流**的值（实参/形参/返回值/Phi 等管线值也保留） | 控制节点规模（全保留会图爆炸；仅 def-use 两端则跨过程链断裂，S2/S3 不可用） |

---

## 13. 与 TD.md 的关系

- 本设计作为 TD.md §12 实现补充记录的延续（v2.2 能力），Joern 已移除（§12.7），数据流由本适配器接替。
- 与 TD.md v2.0 正文冲突处，以 TD.md §12 与本文件为准。

---

## 14. 实现补充记录（v2.3，2026-08-14）

实现阶段（Phase 1–4 + 前端增强）的需求增补。凡与正文冲突处，以本节为准。

### 14.1 签名结构节点（Q74）

前端需求：函数/方法节点在图内展开**入参与返回节点**。SSA 适配器按签名**静态发射**（不依赖 SSA 值裁剪）：

- `parameter`：每个签名参数一个节点（含接收者，`#param.recv.<name>`）；`types.Signature.Params()` **不含接收者**（接收者在 `Recv()`），接收者须单独发射。
- `result`：单返回 `#result`，多返回 `#result.<idx>`，节点名即类型。
- 边：`has_param` / `has_result`（函数 → 节点，conf 1.0），进 expand 白名单。
- 定义顺序：Expand 查询按 `properties.index` 排序（参数组 → 返回组 → 其他边），接收者（index -1）最前。

### 14.2 图内数据流展开（Q75）

- **字段数据流文本树**：`/api/flows?id=<函数>`（repo.GetFunctionFlows）——起点 = 函数内全部 `field_access`，双向递归 CTE（data_flows_to/phi_operand，func_id 限定函数内），Dir=0 产生链 / Dir=1 使用链；信息栏"字段数据流"按钮渲染缩进树。
- **参数节点展开数据流**：expand 对 `parameter` 节点**代理**到对应 `ssa_value`（`#param.[recv.]X → #X`），附加桥边 parameter→ssa_value（data_flows_to，不落库仅响应）；expand 白名单加入 `data_flows_to/argument/returns/phi_operand/alias`——参数 → 调用方实参（argument 上游）→ 函数内字段访问（data_flows_to 下游）逐级可展开。
- **链上参数回到所属函数**：expand 对参数类 `ssa_value`（origin_kind=param/receiver）附加桥边 函数→参数值（has_param，不落库）——追溯链上出现上游函数参数时，双击可回到所属函数继续探索。
- **节点展示**：字段访问节点标签显示所属函数（funcName）+ `[读]/[写]:行号`；信息栏显示所属函数与字段路径。

### 14.3 展示名还原（Q76）

`ssa_value` 节点 name 用 `instancePath` 还原源码变量链（局部变量 x、解引用、字段链 x.a）；仅纯临时值（Phi/Call/BinOp 结果）保留 SSA 名 tN。**ID 保持稳定**（slot 仍为 SSA 名，展示名存 name 字段）。CLI trace 输出与前端文本树均用展示名。

### 14.4 符号搜索隔离（Q77）

`GetSymbolByName`（CLI `query symbol` 与前端 `/api/search`）排除 `field_access` / `ssa_value` / `external_summary`——字段访问点与 SSA 临时值不是可搜索的代码符号。

### 14.5 前端配色与布局

- `field_access` 酸橙 `#7cb305`、`ssa_value` 浅灰 `#bfbfbf`、`parameter` 金 `#d48806`、`result` 粉 `#f759ab`；argument/returns/phi_operand/alias 线型与信息栏分组。
- 数据流边归三行布局 mid 类（layout.js default 分支，无需改行分类）。

### 14.7 摘要系统实现（Phase 5，Q79）

- 内置摘要：`encoding/json.Unmarshal`（写 v 全部字段，递归展开深度 ≤4）、
  `fmt.Printf`（从参数 1 起读全部实参字段）、`database/sql.(Rows).Scan`
  （写 dest 指向值）、`net/http.(Request).ParseForm`（写 Form）、
  `FormValue`（读 Form）；`context.Context` 透明无条目。
- 用户摘要：`field-summary.yaml`（模块根目录），解析失败/重复定义警告降级，
  **用户条目可覆盖同名内置**。
- 应用机制：外部调用点生成虚拟 `field_access`（`is_external=1`，func_id=调用者，
  ID `#ext.<path>.<access>@<line>`）+ `external_summary` 节点 +
  `summary_io` 边；写摘要另加 `INDIRECT_WRITE`（调用者→虚拟节点）、
  `data_flows_to`（实参→虚拟节点），并进入调用者的间接写摘要表；
  读摘要加 `data_flows_to`（虚拟节点→实参）。
- **SSA 表示坑**：`any` 形参实参被 `MakeInterface` 装箱（Type()=any）、
  `...any` 变参被包装成 `[]any` 的 Slice 指令（alloc→IndexAddr→Store 链）——
  应用前须解包取真实值/真实元素。
- 依赖：`gopkg.in/yaml.v3`（纯 Go，无 CGO）。

### 14.8 轻量别名分析（Q80，2026-08-14 设计树确认）

替代已移除的 go/pointer（x/tools v0.26 无此包），目标为**间接写精度**。
设计树 12 项决策（用户确认）：

| 决策 | 选择 |
| :--- | :--- |
| Q1 动机 | 间接写精度：修类型级误报（被调写自己内部对象却被算作调用者间接写） |
| Q2 范围 | 过程内 must + 跨函数参数/返回直通 |
| Q3 产出 | 落库 ALIAS 边（may，conf 0.8），仅 expand 可见（不进 value-trace/S3） |
| Q4 语义 | 分离：间接写判定用 must（消除误报）；落库边用 may（文档契约） |
| Q5 间接写 | 别名优先 + 类型级 fallback（分析不出时兜底，宁多不漏） |
| Q6 形态 | 锚点式：变量 → alloc 节点（非变量对 O(N²)，跨函数天然收敛） |
| Q7 锚点标识 | 复用 ssa_value（origin_kind=alloc），发射条件扩展为"参与字段访问或被别名引用" |
| Q8 must 粒度 | 按调用点实例化（funcData.calls 现成数据） |
| Q9 可见范围 | alias 仅 expand 白名单（已加入），前端图上按需展开 |
| Q10 上限 | 每函数 200 alloc，超限跳过该函数（fallback 类型级） |
| Q11 传播方向 | 参数 + 返回值双向（工厂函数返回对象被内部初始化算间接写） |
| Q12 置信度 | 不区分（function_field_summary 表结构不动） |

**算法概要**：
1. 过程内：变量（ssa.Value）→ 指向的 alloc 集合；must = 值传递链
   （Phi/UnOp/FieldAddr 无分叉汇聚单一 alloc），may = 可达性。
2. 跨函数：实参→形参、返回值→调用者，沿调用图传播（visited 去重防环）。
3. 间接写判定（emitSummaries 闭包迭代内）：调用点 f→g，g 写字段的
   base 变量 must 集 ∩ 该调用点实参 must 集 ≠ ∅ → 计入；空 → 类型级 fallback。
4. ALIAS 边：may 别名（变量→alloc）落库（conf 0.8，UNIQUE 去重）。

**构建流程**：emitFunction 单遍发射后，Index 末尾新增别名 pass
（computeAliases，产出 must 集 + may 边），emitSummaries 消费 must 集
并发射 alias 边——避免改动单遍流式结构。

### 14.6 测试与验证（Q78）

- 单元：ssa 适配器（映射/跨过程/签名/摘要）、sqlite（递归 CTE、expand 顺序、参数代理桥边）。
- 集成（make it）：fixture 含字段访问，覆盖 `query fields`/`trace-backward`/`trace-forward`/`export` 与 `/api/flows`、has_param/has_result 展开。
- 前端 e2e（make e2e，playwright，19 项断言）：见 §11。

### 14.9 数据值全链追踪（Q81，2026-08-14）

需求：追踪一个数据在整条链路上如何被处理（以函数为上下文）。

- repo.GetValueTrace：任意数据节点（field_access/ssa_value/parameter）
  为锚点，双向遍历 data_flows_to/argument/returns/phi_operand（跨函数
  无界），行带 func_id 供函数上下文分组。
- CLI：`query value-trace <节点ID>`——按【函数】分组输出（方向箭头 +
  边类型 + 节点 + [读/写] + 行号）。
- 前端：数据节点信息栏"追踪此数据"按钮 → 函数上下文分段文本树
  （/api/value-trace）。
- 展示名：ssa_value 节点 name 用 instancePath 还原源码变量链
  （局部变量/解引用/字段链），纯临时值保留 SSA 名 tN；ID 保持稳定
  （slot 仍为 SSA 名，展示名存 name 字段）。

### 14.11 map/slice/array 元素追踪（Q83，2026-08-14 设计树确认）

需求：v1 非目标取消，实现容器元素访问追踪。设计树决策：

| 决策 | 选择 |
| :--- | :--- |
| Q1 粒度 | 常量 key 敏感（`m["a"]` / `s[0]`）；变量 key 回退容器级（`[key]`）；Range 迭代 `[*]` |
| Q2 范围 | map + slice + array（channel 排除：收发非读写语义） |
| Q3 建模 | 复用 field_access 节点，full_path 用 `[...]` 记号（`pkg.T.M["a"]`）；字段路径 > named 容器类型 > 回退 instance |
| Q4 集成 | 提取 + 摘要（S1）+ 数据流链（trace/value-trace）；元素间接写只走别名命中（Q7a-②，无类型级 fallback）；外部摘要后置 |
| Q5 标识 | 字符串 key 带引号 `["a"]`、int 索引 `[0]`、变量 `[key]`、Range `[*]` |
| Q6 查询 | trace 精确匹配（现状），元素路径需完整输入 |
| Q7 间接写 | 被调写元素（MapUpdate/IndexAddr+Store）→ 容器 base may ∩ 实参 may → 调用者间接写条目（field_path 为元素路径） |

**指令映射**：`Lookup` / `Index` → read；`MapUpdate` / `IndexAddr`+Store → write；
`Range` → read（channel 跳过）；`v, ok := m[k]`（CommaOk）→ read。

**SSA 表示坑**：lifting 后 map 字面量是 `MakeMap` 寄存器、`make([]int, n)` 是
`Alloc+Slice` 包装——容器名从赋值语句反查（buildAssignTargets：表达式区间
→ 目标变量名，MakeMap.Pos 落在字面量内部须区间匹配）；别名锚点扩展为
对象创建点（alloc / MakeMap / MakeSlice，may 集泛化为 ssa.Value）。

radar 实测：790 元素访问节点（`data["Active"]` 等）、736 行元素间接写。

### 14.10 已确认 backlog（非 v1 范围）

- 入口可达子图优化（§9：默认只分析入口可达，当前为全 module 构建）
  ——**最低优先级**（Q135，2026-08-15 降级：待性能基准 benchmarks/ 数据
  支撑后再评估；unused 分析 §16 已提供死代码量化手段作预判）
- 性能基准 benchmarks/（§11：pprof 构建时间/内存记录）
- v2 计划：泛型完整支持（channel 元素追踪 4f21ef3 已实现；
  增量更新 7dec073 已实现；MCP serve 已取消——§19）

---

**文档结束**。本版由 go-cpg v1.0 设计文档（2026-08-13 之前版本）整体适配而来：保留全部 SSA 语义与映射规则，重塑为 codeintel 适配器形态；§14 为 2026-08-14 实现阶段需求增补记录。

## 15. 优化路线图（设计树决策 Q84–Q102，2026-08-14）

9 项优化方向经四轮设计树访谈确认（Q84–Q102，对应设计树 Round 1–4 的
Q1–Q19）。决策编号延续 §14 的 Q 体系。实现顺序见 15.7 里程碑。

### 15.1 顶层决策（Q84–Q87）

- **Q84 交付范围**：三阶段——阶段 A（分析内核）：跨函数可变参数回连、
  接口动态派发、路径条件标注、可解释不确定性；阶段 B（语义层）：持久化
  识别、全局/DI 建模；阶段 C（表达层）：生命周期图、跨层摘要、输出降噪。
  输出降噪（纯 CLI 基建）提前实施
- **Q85 实现层**：混合——候选集与动态派发落库（schema v3 一次变更），
  条件标注与持久化映射查询期计算（视图增强，不改变图结构）
- **Q86 输出通道**：CLI 主通道（--json/--compact 全命令通用、Mermaid/DOT
  导出子命令）；HTTP 仅透出落库的结构化数据；前端渲染留阶段 C 后评估
- **Q87 框架绑定**：框架无关启发式识别（不绑定 GORM/langchaingo 等），
  通用模式优先，特定框架适配器后续可加

### 15.2 阶段 A 分支（Q88–Q93）

- **Q88 日志文件**：日志（zap + OTel span）写入 `.codeintel/codeintel.log`
  （与 db 同目录），stdout 只承载查询结果。机制：logging.Setup 早期调用
  保持 stdout 兜底，新增 logging.ToFile(dir) 延迟切换（各命令解析 --repo
  后调用）；无轮转（MVP，serve 长期运行建议定期清理）
- **Q89 Mermaid/DOT 导出**：`export graph --type value-trace|callees
  [--format mermaid|dot]`——value-trace 默认 mermaid（flowchart 子图表达
  函数分组），callees 默认 dot；生命周期图（阶段 C）复用同一导出器
- **Q90 调用点级回连**：间接写摘要/边 metadata 补充调用点行号与实参
  变量名（`run:16 fillParam(c) → c.Key 被写`）——零 schema 变更
  （metadata JSON 列已有）；持久化识别（Q98）复用此粒度
- **Q91 动态派发候选集**：注册点识别（Register/Add/New 调用、全局变量
  持有、构造器参数）优先 + 全量实现枚举兜底；dispatch_to 边落库
- **Q92 路径条件标注**：三类条件——常量可传播分支（`if cfg.APIKey == ""`）、
  类型断言/switch 分支、env/flag 读取；标注在数据流边上（conditions 列表），
  查询期由 action 层计算；异常返回路径并入分支标注不单独建模
- **Q93 置信度与缺失**：三档置信度——静态注册命中 0.9、类型匹配枚举 0.7、
  启发式猜测 0.5；缺失信息枚举三类——dynamic_call_unresolved（动态调用
  未解析）、config_unknown（配置值无法静态确定）、generic_uninstantiated
  （泛型实例缺失）

### 15.3 schema v3 与查询契约（Q94–Q96）

- **Q94 dispatch_to 边**：source = 接口方法（与现有 calls 边链式组合：
  调用函数 →calls→ 接口方法 →dispatch_to→ 实现方法），target = 候选实现
  方法；metadata `{origin: register|enum|guess, confidence, register_site}`
  （register_site 仅注册点命中时）；Expand 边白名单加 dispatch_to；候选
  实现方法节点不存在（外部包）时跳过
- **Q95 查询契约**：条件叠加在 trace-backward/forward、value-trace 树形
  输出（`[条件: ...]`）与 --json 的 conditions 字段；symbol 接口方法详情
  列出候选实现（置信度 + 注册点）；--json 输出 candidates + missing；
  HTTP 仅 /api/expand 透出 dispatch_to 边（conditions 暂不进 HTTP）
- **Q96 CLI 参数**：--json/--compact 为 query 全部子命令通用 flag；
  导出为 `export graph` 子命令

### 15.4 阶段 B 分支（Q97–Q98）

- **Q97 持久化识别**（查询期计算）：`*sql.DB`/db.Exec/Query/Prepare 调用链
  → 参数数据流回连字段路径（复用 Q90 调用点回连）；SQL 字面量字符串启发
  提取表名与列名（`INSERT INTO users(key,..)`、`UPDATE users SET key=?`）；
  事务边界（Begin/Commit/Rollback）标注 `[事务内]`；状态前置（写前 if
  条件）复用 Q92 条件标注；不做复杂 SQL 解析（JOIN/子查询）与 ORM 反射
  映射（GORM 标签）
- **Q98 全局/DI 建模**：Go 原生模式——全局变量初始化点溯源（value-trace
  已有 origin_kind=global 节点）、init()/main 构造链（NewXxx 工厂返回接口
  与 Q91 派发候选连接）、配置驱动（os.Getenv/flag 读取影响分支，与 Q92
  条件标注连接）；不做 DI 容器框架（wire/fx/dig）

### 15.5 阶段 C 分支（Q99–Q100）

- **Q99 端到端生命周期图**：聚合 value-trace 全链 + persist_to（Q97 存储
  标注）+ conditions（Q92）→ 来源/转换/派生/存储/跨模块/外部调用一图展示；
  派生字段用既有 data_flows_to 链标注；观测指标识别纳入 v1（Inc/Observe/
  WithLabelValues 三模式启发）；交付 `export graph --type lifecycle --target
  <字段>`（mermaid）
- **Q100 跨层摘要**：`query summary <字段>`——入口 → 计算 → 写入 → 消费
  简洁链路，每步带 file:line；--json 输出结构化 steps；mermaid 同链路由
  图；主链算法 = value-trace 结果上的最高置信度最长链（复用 Q93 置信度），
  分支处取最高置信度路径、其余标注分支

### 15.6 里程碑（Q101）

- **M1（阶段 A）**：8 输出降噪（日志文件 + --json/--compact + export graph）
  → 2 调用点级回连 → 1+9 dispatch_to 边 + 置信度/缺失 → 3 条件标注
- **M2（阶段 B）**：4 持久化识别 → 7 全局/DI 建模
- **M3（阶段 C）**：5 生命周期图 → 6 跨层摘要
- 每项独立提交（可回滚可审查）；验收 = 单元测试 + 集成测试场景 + radar
  实测 + 测试矩阵全绿 + git push

### 15.7 实现记录（2026-08-14 全部交付，测试先行）

9 项优化按里程碑 M1→M3 全部实现并交付（提交 fcf8ddf 起，git log 可查）。
实现要点与 go/ssa 事实（避免回退踩坑）：

- **M1-1 输出降噪（6ce9cb9）**：日志入 `.codeintel/codeintel.log`（zap +
  OTel 从创建起写文件——main 粗解析 --repo 传 logging.Setup(logDir)，
  root span 才不泄漏 stdout）；query 全命令 --json/--compact；
  export graph（value-trace 默认 mermaid、callees 默认 dot）
- **M1-2 调用点回连（7e62838）**：callInfo/fieldEntry 带 callLine/argNames，
  INDIRECT_WRITE 边 metadata {call_line, call_args}（零 schema 变更）；
  元素间接写（别名分析）同步回连
- **M1-3 动态派发（7a3f840）**：dispatch_to 边 source=接口类型节点
  （接口方法无独立节点是 AST 既有决策，Q94 按此适配）、target=候选
  实现方法；注册点 = SSA MakeInterface（动态值字面量位置，Mi.Pos 为
  合成位置）；枚举兜底（types.Implements 须同时检查值与指针方法集、
  排除接口自身——Implements 自反）；置信度 register 0.9 / enum 0.7
- **M1-4 条件标注（753761f）**：查询期 AST 提取节点所在 if/类型 switch
  条件（嵌套取最内层），叠加追溯输出 [条件: ...] 与 --json conditions
- **M2-1 持久化（b9a97a1）**：database/sql 内置摘要（Exec/Query/QueryRow/
  Prepare + Begin/Commit/Rollback 事务边界）；SQL 字符串启发提取表列
  （INSERT INTO t(a,b)/UPDATE t SET a=?/DELETE|SELECT FROM）；值实参
  按 ? 映射列 → 虚拟节点表.列 + summary_io 边；trace 边类型加 summary_io。
  坑：CallCommon.Args 含接收者（SQL 字符串在 args[1]）；variadic 实参
  被 Slice 打包（variadicElems 解包）
- **M2-2 全局/DI（78f80c8）**：全局变量跨函数共享节点
  （symbol:go:<pkg>:var.<name>，emitValue 特判 *ssa.Global）；emitGlobalInit
  覆盖隐式 init（无 FuncDecl 不被 emitFunction 处理）。坑：var G = T{...}
  初始化是字段级 Store（&G.A）而非 Store→Global；v0.26 Global 无 Init
  字段；init$guard 等内部全局含 $ 需过滤
- **M3-1 生命周期图（8826d81）**：export graph --type lifecycle
  （value-trace 聚合 + 类型标注 [存储]/[观测]/[读]/[写] + 条件）；
  prometheus 观测内置摘要（Counter/Histogram/Gauge 等 ReadArgsAll）
- **M3-2 跨层摘要（fcf8ddf）**：query summary <节点>——SummaryChain
  双向最长链（每 depth 层取首个）+ 步骤类型 entry/compute/write/consume
  + file:line；--json steps / --format mermaid

**测试约定**：每项测试先行（先写单测+集成 → 实现 → 单测 → 集成 →
radar 实测 → e2e 22/22 → push）；集成 fixture 覆盖全部场景
（TestCLIFullFlow 含派发/持久化/元素/别名/嵌套读链，TestIncrementalUpdate、
TestOutputNoiseFree、TestServerEndToEnd）。

---

## 16. 未调用函数与孤立链分析（Q104–Q113，2026-08-14）

### 16.1 需求与形态（Q104–Q105）

需求驱动（用户场景）：一次需求开发完后的两项检查——
① **流程衔接**：本次新增的函数是否都被调用（避免流程没衔接上）；
② **冗余代码**：是否写了没人用的代码（占用代码库空间）。

形态：**CLI 查询命令 `query unused`**（查询期计算，基于现有 DB 边，
零构建改动）。不裁剪不可达代码（裁剪会破坏现有查询语义，且收益需先
经分析确认——后续可独立立项）。

### 16.2 判定语义：两档报告（Q106–Q108）

| 档位 | 判定（入边为空即命中） | 对应需求 |
| :--- | :--- | :--- |
| `无调用` | calls ∪ passes_result 入边为空 | ① 流程衔接 |
| `无任何引用` | + passes_to（回调参数）+ dispatch_to（接口实现被派发）+ initializes（被 &T{} 实例化）+ var 初始化引用（data_flows_to → var.Global）入边为空 | ② 冗余代码 |

- **永不报告**：main() / init()（运行时入口）
- **exported 函数**（首字母大写）：报告但标注 `[exported]`（单模块分析看不到外部 caller，用户自行判断）
- **盲区（2026-08-15 P2 收敛后）**：
  - 函数值赋值（`f := g; f()`）——**已解决**（P2-1：AssignStmt 追踪
    varFuncs，f() 调用点解析到 g 建 calls 边；方法值 `fn := obj.M; fn()`
    同）；跨函数函数值传递仍盲区
  - 外部实参嵌套调用（`fmt.Errorf("%v", joinIDs(x))`）——**已解决**
    （P2-2：外层外部 callee 也建轻量节点 + passes_result，joinIDs 有
    入边不误报）
  - 嵌入提升方法（`db.Exec` → `(DB).Exec`）——**已解决**（Selection
    解析正确处理提升，calls 边直达声明方法；有测试固化）
- **嵌套调用**（`A(B(C()))`）：B/C 无 calls 边但有 passes_result 入边 → 不误报（纳入"被调用"）
- **接口实现**：实现方法无 calls 入边但有 dispatch_to 入边 → 不算孤立（有引用）
- **构建改动（Q108）**：AST 适配器补**包级 var 初始化调用边**——`var x = NewFoo()` 的 rhs 为模块内函数调用时建 calls 边（消除构造函数"写了没人调"的最大误报源；此前 AGENTS.md 已知限制"包级初始化中的调用不建 CALLS 边"）

### 16.3 孤立链（Q109–Q110）

- 链头：无 caller 的函数（calls ∪ passes_result 入边为空）
- 沿 callee 递归；遇**有链外 caller 的节点**在该节点断开（该节点及下游正常）
- 互调环（A→B→A 无外部 caller）整环孤立
- 无调用但有引用（回调/接口实现/被实例化）不视为孤立链，标"有引用"
- 输出：按链头分组，链内节点带行号

### 16.4 命令契约（Q111–Q112）

```
codeintel query unused [--since <ref>] [--json|--compact] [--fail-on unused|isolated] --repo <path>
```

- 默认表格：函数 / 包 / 文件:行 / 状态（无调用 / 无引用 / [exported] / [new] / [mod]）
- 孤立链单独分组（链头在最前，⚠ 标注新增函数在孤立链中——流程可能未衔接）
- `--json`：`{unused: [...], isolated_chains: [...], since: {ref, files, new_functions}}`
- `--fail-on <unused|isolated>`：存在未调用函数/孤立链时退出码 1（CI 拦截"需求没衔接"）；默认退出码 0

### 16.5 `--since` 函数级 [new]/[mod] 判定（Q113）

- 范围：`git diff --unified=0 <ref>`（ref 到当前工作区，含未提交；非 git 仓库报错）
- 解析：`new file mode` → 新增文件；`@@ -a,b +c,d @@` 累加 + 侧新增行号集合；rename 按修改处理
- 判定（对 DB 中 file_path ∈ 变更文件的 function/method 节点）：
  - 新增文件：全部函数 → `[new]`
  - 修改文件：函数**声明行**（LineStart）∈ 新增行号 → `[new]`；声明行不在但**行号区间**（LineStart..LineEnd）∩ 新增行 → `[mod]`；两者不中 → 不标注
- 行号一致性：diff 与索引都基于当前文件，直接对齐
- 无 --since：全量报告（冗余检查）；有 --since：报告只含 [new]/[mod] 函数（流程衔接检查）

### 16.6 报告对象（Q114）

function + method 参与报告；interface 方法不单独报（接口是契约不是实现）；
struct/package/file 节点不参与。

---

## 17. --since 标注推广与节点间路径查询（Q115–Q120，2026-08-14）

### 17.1 背景（Q115）

AGENT 检查新业务需求实现的工作流分析：`query unused --since` 已覆盖
"本次改动的函数调用情况"；另有三个高频检查——① 需求涉及的函数在
其他查询中无法一眼看出哪些是本次新增/修改；② "数据应从 A 到达 B"
（需求数据流断言）需人工判读 value-trace 输出。本节省此补两项：
**--since 标注推广** 与 **query path**。

### 17.2 --since 标注推广（Q116–Q117）

- `--since <ref>` 从 unused 推广到 `query symbol / fields / callers /
  callees / impact`：**输出标注而非过滤**（不改变查询语义，追溯链不
  因标注断链）
- 标注对象：输出中的**函数/方法节点**（symbol 详情头部、callers/callees/
  impact 的邻居列表、fields 的头部）——`[new]`（声明行命中 diff 新增行 /
  新增文件）/ `[mod]`（行号区间命中新增行）
- 实现：gitdiff 解析（§16.5）复用；标注判定 `MarkSince(file, start, end,
  since)` 为纯函数（UnusedFunc 与 CodeEntity 共用）
- trace/value-trace/summary（行级输出，节点是字段访问/ssa_value）不做
  标注——函数上下文标注价值低、侵入大

### 17.3 query path：节点间路径查询（Q118–Q120）

```
codeintel query path <from> <to> [--max-depth N] [--kind data|calls] [--json] --repo <path>
```

- 输入：两端节点（canonical ID / 符号名 / 字段路径——ResolveAnchor 解析）
- 语义：BFS 最短路径（有向 from→to）；防环（visited 天然）；深度上限
  --max-depth（默认 50）；不可达输出"无路径"（退出码 0，--json 空 paths）
- 边集：
  - `--kind data`（默认）：data_flows_to / argument / returns /
    phi_operand / summary_io——字段级数据流（"值是否真的到达"）
  - `--kind calls`：calls / passes_to / passes_result——函数调用关系
    （"调用链是否连通"）
- 输出：路径节点序列（节点 + 边类型 + 行号）与路径长度；--json
  `{path: [...], length: N, reachable: bool}`
- 用途：需求断言"X 的值应到达 Y"——AGENT 直接判定 reachable，替代
  人工判读 value-trace

---

## 18. 大仓模块间调用关系分析（Q121–Q128，2026-08-14）

### 18.1 需求与形态（Q121–Q122）

大仓（monorepo）中模块间通过 gRPC 跨进程调用（如服务 A 调服务 B 的
`Greeter.SayHello`）。目标：分析模块间调用关系——谁调用了谁、调用的
服务与方法、服务端实现归属。

- 模块边界：**仅配置驱动**（Q123）——仓库根 `modules.yaml` 定义
  前缀→模块名映射（无默认规则）：
  ```yaml
  modules:
    - prefix: "internal/svc_a"   # 包路径前缀（module 相对）
      name: "svc_a"
    - prefix: "pkg/common"
      name: "common"
  ```
  未匹配前缀的包归 `_root` 模块（查询期计算，改配置无需重建索引）
- 传输范围：仅 gRPC（Q124，protoc 生成惯例；HTTP/消息队列二期，
  设计预留框架无关启发式扩展位）

### 18.2 识别模式（Q125–Q126）

- **服务端**：`pb.RegisterXxxServer(s, impl)`（protoc 惯例，AST 适配器
  serves_grpc 已识别）→ 服务 `Xxx` 由 impl 类型实现
- **客户端**：`c := pb.NewXxxClient(conn)` → 客户端对象 c 记入 objVars
  （复用 `x := &T{}` 对象追踪机制，Q3）；函数内 `c.Method(ctx, req)`
  经 objVars 归属 → 客户端调用服务 `Xxx` 的 `Method`
- **ServiceDesc 动态注册**（grpc 反射服务）：标"未知实现"（缺失信息，
  Q93 精神）
- **跨函数客户端传递**（`handle(c)` 内 `c.Method()`）：一期盲区（仅
  函数内追踪），文档记录

### 18.3 数据模型（Q127）

- 新增节点 `grpc_service`：ID `symbol:go:<生成包>:svc.<Xxx>`（服务标识 =
  生成包路径 + protoc 服务名）；properties `{service_name}`
- 新增边：
  - `grpc_impl`：服务实现类型 → grpc_service 节点（服务端归属，
    conf 1.0）
  - `grpc_call`：客户端调用方函数 → grpc_service 节点（conf 1.0，
    metadata `{method, line_num}`——客户端调用服务 Xxx 的 Method）
- 匹配键（Q128）：服务名（生成包路径+服务名）+ 方法名双键；
  客户端调用无仓库内服务端实现 → 标"服务端未在仓库内"

### 18.4 产出（Q129）

```
codeintel query module-calls [--module <name>] [--json] --repo <path>
codeintel export graph --type modules [--format mermaid] --repo <path>
```

- `query module-calls`：模块级调用表——调用方模块 → 被调模块：
  服务.方法 + 行号 + 调用方函数；`--module` 过滤单模块；
  `--json` 结构化 `{calls: [{from_module, to_module, service, method,
  caller, line}]}`；服务端未在仓库内的调用标 `[外部服务]`、
  未知实现标 `[未知实现]`
- `export graph --type modules`：mermaid 模块调用图（模块节点 +
  grpc 边，边标注 服务.方法 计数）

### 18.5 一期/二期范围（Q130）

**一期已交付（bc51b5a）**：
- gRPC 客户端/服务端识别（NewXxxClient / RegisterXxxServer，protoc 惯例）
- modules.yaml 模块边界（查询期计算，改配置无需重建索引）
- grpc_service 节点 + grpc_call / grpc_impl 边
- query module-calls（含 --module / --json）+ export graph --type modules

**二期（backlog，设计已预留扩展位）**：
- **HTTP（REST）模块间调用**：http.Client 调用点识别 + 服务端路由
  （URL 字符串/配置驱动，模式杂）——Q124 传输范围扩展
- **消息队列**（kafka/rabbit 发布订阅）——Q124 同上
- **跨函数客户端传递**（`handle(c)` 内 `c.Method()`）：当前仅函数内
  追踪（grpcClients 为 processFile 局部 map）；需 AST 参数流扩展
  （实参→形参关联客户端对象）——Q125 盲区
- **ServiceDesc 动态注册**（grpc 反射服务）：当前标"未知实现"——
  需识别 `grpc.ServiceDesc` 注册表——Q127 盲区
- **多 go.mod 大仓（2026-08-15 P2-3 已实现）**：递归扫描仓库根下所有
  go.mod（跳过 .git/.codeintel/vendor/node_modules；module 目录内不再
  嵌套扫描）→ `Repository.Modules`/`ModuleDirs`（根在前）；orchestrator
  每 module 单独 packages.Load（按 PkgPath 去重合并）+ scip-go 每 module
  独立索引（index-N.scip）；isInModule 改多前缀判定（任一匹配即项目内）；
  action.ModuleOf 剥离所在 module 前缀（最长前缀匹配 modules.yaml）。
  发现方式：`orchestrator.DiscoverModules`（cli init/update/serve 经
  buildRepo 构造 Repository）。仍不支持：go.work 根（无根 go.mod 时报错
  提示进入模块目录）
- **模块图进前端图探索**（serve 页面模块视图）：当前仅 CLI/export
  输出，无 module 节点落库——Q129 备注

### 18.6 手写 client + gRPC 方法路径调用（Q131–Q134，2026-08-14）

一期（§18.2）识别 `NewXxxClient` 生成 client；另一种常见形态是**手写
client**——不经过生成代码，直接以 gRPC 方法路径发起调用：

```go
conn.Invoke(ctx, "/example.com.pb.Greeter/SayHello", req, resp)  // ClientConn.Invoke：路径在第 2 参
conn.NewStream(ctx, desc, "/example.com.pb.Greeter/SayHello")    // NewStream：路径在第 3 参
grpc.Invoke(ctx, target, "/example.com.pb.Greeter/SayHello", ...) // 旧版顶层：路径在第 3 参
```

- **识别范围（Q131）**：方法路径**字符串字面量**直接提取；**一层赋值链**
  常量传播（`const method = "/..."` / `method := "..."` 后传入）提取；
  更深/动态来源不产边（盲区标注）
- **调用形态（Q132）**：识别 `Invoke`（路径第 2 参）/ `NewStream`（路径
  第 3 参）/ 顶层 `grpc.Invoke`（路径第 3 参）调用点本身——与 conn 来源
  无关、与所在函数无关（自定义封装 client 方法内自动覆盖，零配置）；
  方法名 + 路径格式 `/.../...` 双条件判定（框架无关启发式，Q87）
- **服务标识（Q133）**：路径 `"/<proto包>.<服务名>/<方法>"` 中
  `<proto包>.<服务名>` 为服务标识（proto 包与 go 生成包路径可能不同）；
  grpc_service 节点 ID `symbol:proto:<proto包>:svc.<服务名>`（与一期的
  `symbol:go:<go包>:svc.<服务名>` 区分）；**服务端匹配按服务名末段**
  （路径末段 == RegisterXxxServer 的服务名，protoc 惯例服务名相同）
- **落库（Q134）**：复用 grpc_call 边（kind 不变）——metadata 加
  `method_path`（完整路径）+ `method`（末段方法名）+ line_num；
  `module-calls` 聚合对两种形态统一按 `服务名.方法名` 展示；impl 匹配
  子查询改为按 grpc_service 节点 name 相等（`svc.<服务名>`，跨 ID 前缀）

---

## 19. 路线调整（Q135，2026-08-15）

- **MCP serve 取消**：AI 代理直接使用 CLI 查询命令——全部 query 子命令
  的 `--json` 结构化输出即机器接口契约；TD.md §7 MCP 工具契约不再实现
- **入口可达子图优化降为最低优先级**：构建性能优化待性能基准
  （benchmarks/）数据支撑后再评估；`query unused`（§16）已提供死代码
  量化手段，可作为是否值得裁剪的预判依据

---

## 20. 增量自动触发与性能基准（Q136–Q137，2026-08-15）

### 20.1 增量构建自动触发（Q136）

CLI `update`（全量分析+增量写入）已有；补**自动触发闭环**（TD.md
§5.2/6.2 降级项推进）：

- `serve` 新增 `POST /incremental`：无负载（serve 已绑定 repo）→
  **202 Accepted** + 异步执行 IncrementalBuild（goroutine，变更检测
  复用 `update` 的 git 逻辑）；执行中再请求返回 409（busy）——
  单写者（SQLite）
- 构建结果：写 build_metadata（tool_name=incremental，已有 Save）+
  日志文件（.codeintel/codeintel.log）；不中断 serve
- **Git hook**：`scripts/install-git-hook.sh` 安装 **post-commit**
  hook（本地开发写完即更新索引——post-receive 是推送语义，本地
  主场景 post-commit 更实用）——`curl -s -X POST
  http://localhost:<addr>/incremental` 触发；serve 需先启动
- 索引未构建时（serve 启动校验）不响应 /incremental（404 提示先 init）

### 20.2 性能基准 benchmarks/（Q137）

- `benchmarks/bench_test.go`：对指定仓库（`-bench-repo`，默认 radar）
  跑**进程内** orchestrator.FullBuild，记录：
  - 各适配器耗时（AdapterResult.Duration）+ 总耗时
  - 峰值内存（runtime.MemStats，构建前/后采样）
  - DB 大小（.codeintel/codeintel.db 字节）
- 输出：表格 + `-bench-json` 结构化（供入口可达优化等后续决策的
  基线数据）
- 与验证矩阵分离（`make bench`）；构建目标仓库索引为副作用
  （允许——基准即重建）

### 20.3 触发失败提示与陈旧检测（Q138–Q139，2026-08-15）

- **hook 失败提示（Q138）**：post-commit hook 触发失败不再静默——
  curl 模式连接失败（serve 未启动）在 `git commit` 输出提示
  "⚠ codeintel 索引未更新（serve 未启动？）"；`--direct` 模式
  （install-git-hook.sh --direct）post-commit 直接运行
  `codeintel update --repo <repo>`（不依赖 serve，确定性更新；
  代价 = 每次提交跑全量分析，大仓提交变慢——update 分析成本
  ≈ init，git commit 增量无法裁剪：类型检查/SCIP/SSA 构建均为
  全仓库粒度，文件级增量会破坏跨函数数据流完整性）
- **查询陈旧检测（Q139）**：query 命令启动时对比 build_metadata
  最新 timestamp 与 `git log -1 --format=%ct`——索引早于 HEAD →
  stderr 提示"⚠ 索引可能过期（构建于 X，HEAD 更新于 Y）；运行
  codeintel update"——无论 hook 是否触发都能发现陈旧（兜底）

### 18.7 HTTP（REST）模块间调用（Q140–Q143，2026-08-15）

- **服务端路由表 = 人工配置文件**（Q140）：仓库根 `routes.yaml`（与
  modules.yaml/field-summary.yaml 并列）——路由不靠代码注册调用识别，
  由人维护服务接口清单：
  ```yaml
  routes:
    - path: "/api/orders"
      handler: "internal/svc_orders:(Handler).ListOrders"  # 符号名，构建期解析
      method: "GET"                                        # 可选
  ```
  构建期读取 → 生成 `http_route` 节点（`symbol:go:<handler包>:route.<path>`，
  properties 带 handler 函数 ID + path + method）；handler 解析失败
  （符号不存在）→ 跳过并警告
- **客户端识别（Q141，P1-3 扩展 2026-08-15）**：`http.Get(url)`（URL 第 1 参）/
  `http.NewRequest(method, url, ...)`（URL 第 2 参）/ 
  `http.NewRequestWithContext(ctx, method, url, ...)`（URL 第 3 参——P1-3
  补，同签名此前完全漏识别）——URL 字面量 + 常量传播（复用 §18.6 的
  methodVars/const 机制）+ **常量字符串拼接**（`const base = "https://x"` +
  `base+"/y"`，P1-3 extractStringArg 加 BinaryExpr）；`client.Do(req)`：
  req 由本函数 `req := http.NewRequest(...)` 赋值时追踪（reqVars），
  Do 消费建边但 NewRequest 已建同 URL 边时跳过防重复——请求发出点
  语义仍以 NewRequest 行号为准；req 跨函数来源仍盲区。URL 解析出
  host + path（query 剥离）
- **目标模块判定（Q142）**：**仅路由表路径匹配**（无 hosts.yaml——
  host 由服务部署配置管理，代码不硬编码域名；host 仅记录于 metadata
  作展示）。路径匹配 = 精确 + 前缀（路由 path 以 `/` 结尾或含 `{id}`
  通配 → 前缀/通配匹配，Q143）；匹配成功 → `http_call` 边
  （调用方函数 → http_route 节点，metadata `{url, host, path, method,
  line_num}`）；匹配失败 → http_call 到外部虚拟节点
  （`symbol:http:<host>:route.<path>`，handler 空）→ module-calls
  标 `[外部服务]`（与 gRPC 对称）
- **module-calls 扩展**：查询合并 grpc_call + http_call，输出带
  transport 字段（grpc / http）

---

## 21. P2：跨函数客户端、ServiceDesc、模块图前端（Q144–Q147，2026-08-15）

**包级循环依赖检测不做**（Q147：其他工具职责，如 go list 自带）

### 21.1 跨函数 gRPC 客户端传递（Q144）

一期（§18.2）grpcClients 为函数内局部 map——`handle(c)` 内 `c.Method()`
不归属。扩展为**形参类型识别**（比值流简单可靠）：
- 函数形参类型是模块内 pb 包的 `XxxClient` 接口（类型名匹配
  `New<Xxx>Client` 同款服务名提取）→ 函数内该形参名记入
  grpcClients（服务 Xxx）→ 函数内 `c.Method()` 归属服务
- 实参侧（NewXxxClient 返回值）已有一期覆盖；两条路径合并

### 21.2 ServiceDesc 动态注册（Q145）

`grpc.RegisterService(s, &grpc.ServiceDesc{ServiceName: "pb.Greeter", ...})`
（反射服务/动态注册）不经过 RegisterXxxServer：
- 识别 `grpc.ServiceDesc{ServiceName: "..."}` 复合字面量（ServiceName
  为字符串字面量）→ 发射 grpc_service 节点——标识与手写 client
  合并（symbol:proto:<proto包>:svc.<服务名>，ServiceName 即 proto
  全名）
- module-calls 中该服务 impl 缺失 → 标 `[动态注册]`（与 `[外部服务]`
  区分——服务在本仓库声明但实现未静态识别）

### 21.3 模块图前端展示（Q146）

- serve 新增 `/api/module-calls`（HTTP JSON 透出 action.ModuleCalls）
- 前端新增**模块视图**（assets/web/modules.html + 轻量 G6 渲染：
  模块节点 + grpc/http 边，边标注服务.方法）；serve 首页导航进入

---

## 22. query table 表级数据流聚合（Q148，2026-08-15）

**动机**：理解项目时从"数据库表"反推数据流——表.列虚拟节点（Q97 持久化
映射 + GORM ②）已有，但无表级聚合查询。

**命令**：`codeintel query table <表名> [--json]`；`codeintel query relations <表名> [--json|--format mermaid]`（表间关联）

**表间关联（query relations，Q149）**：无外键时从代码使用方式推断——
该表列虚拟节点沿数据流边（data_flows_to/argument/returns/summary_io/
alias/phi_operand）BFS（上限 12 跳），收集命中其他表（type_string
sql/gorm）的列：A.x 读出 → Scan 写入变量 → 变量作为 B 查询的 WHERE
实参 → B.y 过滤列——数据流链贯通即关联。依赖三块底层：
- parseSQLStmt 提取 WHERE 过滤列（`列 = ?` 按 ? 顺序）
- SELECT 读路径产 filter 虚拟节点（值实参 → 过滤列）
- Scan 摘要（(Row)/(Rows).Scan：接收者值 → out 实参变量，variadic
  解包 + MakeInterface 解包；局部变量读取归一为变量名 ID）
- emitValue 的 Extract 归一到 tuple 值（row 与调用点返回值同 ID）

radar 实测：sq_lite_atom ↔ sq_lite_knowledge_graph（140 条列关联，
6 跳——ingest 同源写入的原子与知识图谱）。

**精度分级（Q150）**：关联按终点虚拟节点 access_kind 分三级——
`query`（终点是 WHERE 过滤列：A 的值作为 B 的查询条件 = 键关联，高置信）、
`write`（同源/间接写入，中）、`read`（间接扩散，低）。GORM Where 摘要：
`(DB).Where("col = ?", v)` 字符串列名剥离 " = ?" 后缀产 filter 节点
（链式 Model 范围对象溯源）。输出排序 query 优先、跳数升序。
**--format mermaid**：列级图（表为子图、列节点、列间边，query 类型
粗线 ==\>）。
**盲区（Q151 已实现部分）**：GORM 读路径（Find/First/Take/Last）已
映射——对象读出产 表.列 read 虚拟节点 + 边（读出值 → 对象，与写反向）；
radar 实测 ListSessions 的 session.id.read 节点产且 s.ID → filter 边
贯通。**查询关联落地（Q152）**：range 循环链已贯通——SSA 层 Field 值字段
读取补基值边 + UnOp MUL 归一放宽（IndexAddr/FieldAddr）+ Where
variadic 实参解包；sqlite 层循环读出桥（BFS 到 ssa_value 桥接同函数
同类型 read 字段节点）+ 同列多节点 Type 取最高。radar 实测：
session.id → chat_message.session_id [查询关联 4 跳]（ListSessions
读出 → Where session_id = s.ID）。已知权衡：同函数同类型字段读被桥
接，session.title/created_at → session_id 也标查询关联（保守语义）。
- 数据源：`kind='field_access' AND is_external=true AND (name=表 OR name LIKE 表.%)`
  （Q97 字符串 SQL + GORM 结构体写路径共用形态）
- 输出：按列名**聚合**（同列多调用点合并一行），每列列出入写入方
  （summary_io 入边 source 值节点剥 slot → 函数 + 行号）
- 行号来源：summary_io 边 metadata line_num（Q148 补 emitEdgeKindLine；
  旧索引缺失时兜底虚拟节点 LineStart）
- 读取方（P0-2 已实现）：**SELECT 读路径解析**——parseSQLStmt 提取
  SELECT 列（去表前缀/AS 别名；`SELECT *` → 表级）；Query/QueryRow/
  Prepare 调用点产 read 虚拟节点（access_kind=read）+ 读边
  （**虚拟节点 → 返回的 rows/row 值**，与写边值→节点反向）；query
  table 输出每列读取方（读虚拟节点出边的目标函数 + 行号）
- radar 实测：sq_lite_atom 30 个写节点聚合为 10 列，写入方
  (sqliteAtomStore).Create:417 / DeleteOrphaned:492/499

**确认（Q148）**：GORM 结构体写映射（②⑦ applyORMWrite）此前已生效
（radar 216 个 gorm 节点）——早前"radar 无虚拟节点"结论是查询时用
kind 过滤排除了 field_access 的误判，非功能缺失。

---

## 23. 动态派发补 indirect_write 摘要（Q154，2026-08-16）

**动机**（用户 review 发现）：接口动态分派候选实现内的字段写不回传为
调用方摘要。`Process` 调用 `FeeCalculator.Calculate`（动态 invoke），
实现对 `Order.FinalFee` 的写入不出现在 `Process`（及上游调用方）的
indirect_write——字段写断在接口调用点。

**根因**：动态分支（crossflow.go ⑮）已建候选实现的 argument/returns
边，但**未追加 `funcData.calls`**；间接写闭包（emitSummaries）只消费
`fd.calls`（summary.go:29），因此动态调用的候选实现写无传播入口。
dispatch_to 边在摘要之后生成且不被摘要消费（adapter.go emitDispatches
晚于 emitSummaries）——即使时序反转也缺 callInfo 结构。

**修复**：提取 `recordCallInfo(cc, calleeID)`（原静态路径尾部内联逻辑，
含 argStructPaths/argNames/callLine），动态分支为**每个候选实现**追加
callInfo（calleeID = 候选实现 funcID；实参类型路径与静态路径同源——
动态 invoke 的 cc.Args 即接口方法形参，类型解析一致）。间接写闭包
迭代至稳定：实现 direct → wrapper indirect → 上游 indirect 逐层回传。
INDIRECT_WRITE 边 wrapper → 每个候选实现（动态派发语义：均可能被调用）。

**测试**：TestInterfaceDispatchIndirectWrite——接口 + 双实现（StdCalc/
ExpCalc 写 Order.FinalFee）+ wrapper（接口调用）+ 上游（静态调 wrapper）；
断言实现 direct_write、wrapper/上游 indirect_write、INDIRECT_WRITE 边
wrapper→双实现与上游→wrapper。验证矩阵全绿（12 包 + it + e2e 27 项）。

**权衡**：与 dispatch_to 边一致，所有候选实现都进摘要——真实分派是
运行时选择，摘要层面保守全列（Q93 候选集语义）。

---

## 24. value-trace 递归 CTE 按 (id, dir) 去重（Q155，2026-08-16）

**动机**（用户 review 建议 P1）：value-trace 递归 CTE 的递归行携带
depth/edge_kinds，同一节点每条到达路径产一行并各自展开——汇聚点与环使
行数随深度/路径数放大（同节点多深度行各展开一次，最坏指数）。

**SQLite 限制**：递归 SELECT 禁止子查询引用递归表（multiple recursive
references），无法在展开处按 (id,dir) 去重；UNION 按整行去重，含
depth 的行无法按节点唯一。

**修复（单递归 CTE，depth 入去重键）**：
- `vt(id, dir, depth, kind, seed)`：递归行带 depth，UNION 去重键
  (id, dir, depth)——每节点每深度一行（行数 ≤ 节点数×maxDepth 有界），
  `depth < maxDepth` 截断即递归终止（环安全：每圈 depth+1 直到上限）；
  锚点（seed）双向可展开，锚点输出保持 dir=0 一行；最短深度语义 =
  BFS 队列序首次到达
- go2o 实测修正：初版双 CTE（vt 去重集 + dp 深度）的 vt 无深度限制
  → 148K 节点图上全图扩散（热点 param 节点 2-4 分钟）不可行——深度
  必须留在递归行内限制扩散
- 外层 `GROUP BY dp.id, dp.dir` 取 MIN(depth)；edge_kinds 由
  "路径边序列" 弱化为 "入边类型集合（GROUP_CONCAT DISTINCT + Go 侧
  sortEdgeKinds 排序稳定——server LastIndex 取末段展示不受影响）"
- GetValueTraceMulti（跳板合并）同构改造（vt 全 dir=1 单方向）

**测试**：TestValueTraceConvergeDedup——汇聚图（v0→x 直接 + v0→a→x
绕行）断言 x/y 各一行 + 最短深度；TestValueTraceCycle 保持（环行数
从 ~16 收敛为 2）；既有链/多锚点测试不变。验证矩阵全绿。

**查询计划修正（go2o 实测）**：递归步的 `JOIN vt d ON e.target_id =
d.id` 被计划器选成 `idx_edges_kind`（kind IN 5 值命中 ~10 万行）而非
`idx_edges_target`——每轮 10 万×N 次比较，深度 3 即 2 分钟。加
`INDEXED BY idx_edges_source/target` 强制走端点索引 → 热点节点从
4 分钟降到 0.13 秒（1800×）。三个递归分支（GetValueTrace 反向/正向 +
Multi 正向）均加。

**go2o 实测（Q154/Q155 验证）**：init 27s（148K 节点/152K 边）；
`(profileManagerImpl).SaveProfile` indirect_write 22 个字段（动态
派发回传 ✓）；`(serviceUtil).errorV2#err`（207 入边）value-trace
1549 行、重复 (id,dir)=0、0.13s。go2o 无 go.sum（非 git 仓库）——
tidy 补齐后 init。顺带修复 ⑮ 存量 bug：多返回候选实现无 Return
指令（桩函数）时 `rets[0]` 越界 panic。

**权衡**：同节点多路径从多行变一行（最短深度）——展示更干净；
edge_kinds 无路径顺序（展示用途可接受）。

---

## 25. unused 大仓库性能：EXISTS 子查询 → 预聚合（go2o 实测，2026-08-16）

**动机**：go2o 验证（Q155 延续）发现 `query unused` 全量在 13K 函数
节点上 150 秒超时。瓶颈：GetUncalledFunctions/GetIsolatedChains 每行
4 个 EXISTS 子查询（calls/passes_result + passes_to/dispatch_to/
initializes + var 初始化 data_flows_to LIKE 前缀扫描）——13K 行 ×
子查询 = 5 万次索引查找。

**修复**：预聚合替代逐行 EXISTS——
- `edgeTargetKinds(kinds...)`：一次查询返回指定 kind 边的 target_id 集合
- `varInitFuncs()`：一次查询取 var.* 初始化引用的 func_id 集合
- 主查询只 SELECT 节点属性，Go 侧 map O(1) 标记 called/referenced

**实测**：go2o 全量 unused 从 150s+ 超时 → 0.23 秒（650×），输出
11061 未调用 + 0 孤立链。radar 回归正常。验证矩阵全绿。

**go2o 验证汇总（Q154/Q155/§25）**：init 27s（148K 节点/152K 边）；
动态派发 indirect_write 回传 ✓（profileManagerImpl.SaveProfile 22 字段）；
value-trace 去重 ✓（errorV2#err 1549 行、0 重复、0.13s，索引选择
INDEXED BY 修复）；query table ✓（wallet_log 4 列 + 读取方定位）；
update 非 git 仓库正确拒绝；unused ✓（预聚合后 0.23s）。已知边界：
gof 框架自定义仓储不在 GORM 摘要覆盖内（表虚拟节点少，relations 无
关联输出）；alipay 等包自身编译错误降级跳过。

---

## 26. 通用接口摘要：动态 invoke 外部框架 ORM 映射（Q156，2026-08-16）

**动机**（go2o 验证发现）：go2o 用 gof 框架（github.com/ixre/gof/ext/fw
或模块内复制版 pkg/infra/fw）的 `Repository[M]` 泛型仓储——267 处
`repo.Save/FindBy/Update` 全是接口方法调用（动态 invoke），候选实现
`BaseRepository[M]` 在外部模块——动态派发枚举不到、摘要匹配不上 →
表分析（query table）为 0 张表。gof 的 Repository 底层就是 GORM
（`ORM = *gorm.DB`）。

**设计（用户确认：通用接口摘要机制 + 全方法）**：
- spec 新形态：`iface`（接口全路径）+ `method` + `kind`（write/read/
  filter）+ `where_arg`/`obj_arg`/`id_arg`；field-summary.yaml 可自定义
  （其他框架复制 gof 时开箱即用）
- 挂接：emitCall 动态分支候选为空（外部实现）→ applyInterfaceSummary
- 实体类型：泛型接口实例化（Repository[M]）TypeArgs[0]；非泛型接口
  fallback 对象实参/返回值类型
- 表名：实体 TableName() 方法 SSA Return 常量，fallback snake_case
- 方法映射：Save/Update/Delete=对象写；FindBy/Get/FindList=读出 +
  where/主键 filter（Get 主键列取 pk tag，fallback id）；Count/DeleteBy
  =filter（无读出）
- 内置注册 gof 原版 + go2o 复制版两个路径

**顺带修复**：
- `--verbose/--debug` 全局标志未被 main 从 args 移除 → 子命令 flag 解析
  报错（`flag provided but not defined: -verbose`）——已修（main.go）
- whereColsOf 占位符剥离通用化（= ?/=?/< ?/IN (?)/is null 等形态）

**go2o 实测**：表.列虚拟节点从 6 个 → 1489 个（21 张表：mch_bill/
mm_extra_field/mm_relation/pay_order/sys_log 等）；query table
mch_bill 27 列 + 写入方/读取方精确定位（(billDomainImpl).Generate:319
等）；mm_extra_field 14 列（TableName mm_extra_field 解析 ✓）。
relations 无关联为数据形态使然（filter 值多来自参数而非其他表读出）。

**测试**：TestInterfaceSummaryCustom（自定义 iface spec：write/read/
filter/id/AND 拆分/TableName）+ TestGofRepositoryInterfaceSelfContained
（真实 gof 依赖，tidy 后 init）。验证矩阵全绿。

---

## 27. 间接写嵌套传播 + 调用点粒度 + value-trace 候选标注（Q157，2026-08-16）

**review 三项**（用户给出建议优先级，检查后确认 2 项未修复 + 1 项部分）：

**P0-1 嵌套对象字段传播**（未修复 → 修复）：实现写 `Order.FinalFee`，
wrapper 实参 `*OrderModel`（含 Order 嵌套字段）时类型匹配只比较实参
结构体（OrderModel）与字段属主（Order）→ wrapper 间接写缺失。
修复：`ownerTypesOf` 展开实参类型的嵌套 struct 字段 owner 类型集合
（OrderModel → {OrderModel, Order}，深度上限 3，指针/切片解包，去重），
recordCallInfo 的 argStructPaths 用展开集合——嵌套字段写经外包裹传
匹配。

**P0-2 callLine/callArg 粒度**（未修复 → 修复）：indirect 按 fieldPath
去重后复用**首次**保存的调用点——同函数两处调用同一 callee 写同字段
时，两条 INDIRECT_WRITE 边都回连首处。修复：indirect 键改
`indirectKey{fieldPath, callLine}`（字段 × 调用点粒度）；摘要行仍按
字段去重取代表；INDIRECT_WRITE 边 meta 从该边调用点的条目取（边 ×
字段粒度）。

**P1 value-trace 性能/候选标注**（性能已修复）：Q155 的 (id,dir) 去重 +
INDEXED BY 后 go2o 热点写点 --max-depth 2 = 0.05s/27 行（review 观察
的 21s/13KB 为修复前）。**候选混入标注**（未做 → 实现）：GetDispatchTargets
查全部 dispatch_to 边 → action.ValueTrace 叠加标记行所属函数
DispatchCandidate/Origin/Confidence；CLI 文本 `[候选 register 0.9]`、
--json `dispatch` 字段。

**测试**：TestIndirectWriteNestedOwner（动态接口 + 实现写嵌套字段）、
TestIndirectWriteCallLinePerCall（双调用点边各带 call_line）、
TestValueTraceDispatchMark（dispatch 边 → 行标注）。验证矩阵全绿。

---

## 28. gof 原生 SQL/ORM 映射 + pay_order 键关联贯通（Q158，2026-08-16）

**动机**（用户问：pay_order 无关联——怎么找支付方/商家信息）：go2o
会员/商户仓储用 gof 原生形态——`m.Connector.ExecScalar/ExecNonQuery`
（SQL 字符串，PostgreSQL `$N` 占位符）+ `orm.Save(o, entity, pk)` /
`orm.Orm.Get(id, &e)`——均无摘要 → mm_member 等主表无虚拟节点 →
pay_order.buyer_id 链断。

**修复链**：
1. `whereColRe` 支持 `$N` 占位符（`= $1` 形态；go2o 用 pg）
2. **接口摘要 kind=sql**：gof db.Connector 接口（ExecScalar/Query/
   QueryRow 读 + ExecNonQuery 写）——SQL 在 Args[0]（无 receiver），
   applySQLSummary 参数化 sqlArg
3. 接口摘要**候选非空也触发**（embed 提升方法的候选无函数体不产边——
   go2o Connector impls=23 但全是提升方法）
4. `orm.Save` 包级函数（静态）spec：ORMWrite ParamIndex=1
5. **applyORMWrite/Read 表名 TableName() 优先**（payment.Order →
   pay_order，此前 snake 类型名 "order" 错）
6. read 分支支持 ObjArg 输出对象实参 + MakeInterface 解包（orm.Orm.Get）
7. **IDArg 值下标修正**（主键 filter 值 = id 实参，非 where+1）
8. whereColsOf：正则拆 AND/OR（多行条件）+ ORDER BY/字面量条件剥离
9. SELECT 列跳过聚合函数（COUNT(1) → 表级读）

**go2o 实测**：sql 表 0 → 40（mm_member/mm_account/mch_merchant 等）；
pay_order 从"无关联"→ 4000 条（含 query 键关联：id → mm_account.
member_id 10 跳）；mm_member 3 条 query（id → 其他表 id/member_id）。

**回答用户问题**：pay_order 的支付方/商家信息在代码里经
`memberRepo.GetMember(BuyerId)`（→ orm.Orm.Get → mm_member 主键读）
与 seller_id 对应商户查询——键关联链现在贯通（buyer_id 值流 →
GetMember 实参 → mm_member.id filter）。测试：TestWhereColDollar +
TestInterfaceSQLSummary（$N + 接口 SQL 形态）。验证矩阵全绿。

---

## 29. ER 图外键语义过滤：丢弃主键互查噪音（Q159，2026-08-16）

**动机**（用户 review）：ER 图（query 键关联）大量 `id→id` 边——业务
系统不自增主键互查（B 表不会拿自己的自增 id 去关联 A），`id→id` 是
BFS 对象值共享桥接噪音。

**根因**：BFS 从本表**每列**独立出发（id 与 buyer_id 都是起点）——各自
命中对端表 filter → 主键 id 起点与真实外键列起点并存。

**修复**（GetTableRelations 收集后统一过滤）：
- `FromCol == id && ToCol == id` 一律丢弃（主键互查不存在）
- 同目标列多起点时外键形态列（xxx_id）优先——主键 id 起点丢弃
- 保留形态：`A.xxx_id → B.xxx_id`（业务关联键：order_no/parent_id/
  member_id/item_id）、`A.id → B.xxx_id`（主键被外键引用查询：
  mm_member.id → mm_account.member_id）、`A.xxx_id → B.id`（外键查主键）

**go2o 实测**：query 键关联 122 条 → 21 条（20 表对）——会员域各表
member_id→mm_account.member_id、category.parent_id↔product_category
（自引用）、sale_sub_order.order_no→order_list.order_no（2 跳高置信）、
商品域 item_id→snapshot.item_id 等。ER 图脚本 tmp/er_diagram.py。

## 30. 全库关联单次查询：query relations --all + export relations（Q160，2026-08-16）

**动机**（用户）：ER 图验证后确认——AGENT 拿全库表间关联需遍历全部表
逐次 `query relations`（~40 次 CLI 调用），要求一次查询拿全库。

**实现**：
- `sqlite.Repo.GetTables()`——枚举外部 gorm/sql 虚拟节点表名（去重）
- `sqlite.Repo.GetAllTableRelations()`——遍历 GetTables 调
  GetTableRelations，合并去重（同 from/to 列对取 hops 最小 + Type 最高
  query>write>read），按 from/to 稳定排序
- CLI：`query relations --all [--json|--mermaid]`（无需表名参数，cmdQuery
  target 检查放行）、`export relations [--out x.json]`（{"relations": [...]}）
- action 透传：`RelationsAll()`

**性能**（go2o 148K 节点实测）：顺序遍历 2m34s（40 表 × 每表 BFS 12 跳）。
曾试 8 路并发 → 5m53s 反而劣化：SQLite 连接池锁竞争 + 3.5G 低内存 swap
（sys 时间 4 倍）。保持顺序。

**验证**：go2o 全库 42596 条关联（query 键关联 21 条，与 ER 图/源码验证
结果一致）；单元测试覆盖聚合去重（正向 query + 反向 read）。

**边界**：全库 read/write 关联量级大（4 万+），AGENT 建议按 type 过滤取
query 键关联；耗时 2.5 分钟级——适合批量分析而非交互。

## 31. 动态候选溯源：边级元数据 + value-trace 标注/过滤 + 摘要 origins（Q161，2026-08-16）

**动机**（用户 review 剩余问题）：
1. 摘要来源折叠——function_field_summary 对 (function_id, access_kind,
   field_path) 唯一，上游函数目标字段的 indirect_write 只显示一个来源行
   （"置零"分支），动态实现写入的来源丢失
2. 追踪精度保守——从 Payment 分支具体写点追踪仍出现 SppRefundMdr（不在
   PaymentMdrFee 的 dispatch_to 候选内）；动态 argument/returns 边未携带
   候选信息，value-trace 无法区分必达/候选路径

**设计**（用户确认：展示 + --min-conf 过滤都做；origins 独立表）：
- **A. 动态边候选元数据**（crossflow.go emitCall 动态分支）：fieldExtractor
  缓存 collectDispatchRegistrations（一次扫描）；argument/returns 边
  metadata 加 {interface, candidate_origin: register|enum, confidence:
  0.9|0.7}（注册点命中优先，逻辑同 emitDispatches）
- **B. value-trace 边级候选标注 + --min-conf**：GetValueTrace 递归 CTE
  WHERE 加剪枝（metadata.candidate_origin 存在且 confidence < N → 不展开）；
  SELECT 补到达行的候选边信息（EdgeIface/EdgeOrigin/EdgeConf）；CLI
  `--min-conf N` 默认 0。Q157 函数级标注保留，边级合并展示
- **C. 摘要 origins 独立表 summary_origins**（schema v3）：列
  (function_id, access_kind, field_path, call_line, callee_id) UNIQUE；
  origin/confidence 查询期从 dispatch_to 边 join（复用 Q157
  GetDispatchTargets——callee 是候选实现时自然带出）；emit 在
  INDIRECT_WRITE 边循环收集；query fields 展示多来源

**影响**：schema user_version 2→3——验证仓库（radar/go2o）须
clean --force + init 重建；value-trace SQL 每步加 json_extract 判断
（metadata 多 NULL，实测确认开销）。
