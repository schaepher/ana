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
- **v2 计划**：MCP serve 交互入口（TD.md §7 契约，不单独设计 shell）、增量更新、map/slice 等复合类型元素追踪。

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
| S5 | ~~交互式 shell~~（**取消**） | 交互入口由 v2 MCP serve 承担（TD.md §7），不再单独设计 shell 命令。 |

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

**不保留**：`pkg/cpg` 公共 API（原 go-cpg §8.1）——查询入口为 CLI（及未来 MCP serve），无独立 pkg 导出层。

---

## 9. 性能与降级策略

| 问题 | 策略 |
| :--- | :--- |
| **别名分析成本** | 轻量自研（§14.8）：每函数 200 alloc 上限，超限跳过该函数；无 pointer/RTA 选项（go/pointer 已移除） |
| **节点数量膨胀**（SSA_VALUE 每指令一个） | 仅保留参与字段访问的 `ssa_value`（def-use 链两端），与 alias 粒度一致（Q53 同思路）；全量保留会图爆炸 |
| **SQLite 文件过大** | `code_snippet` 限长 500 字符；表达式索引而非冗余列（摘要表冗余列除外，其为查询加速设计） |
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
- MCP serve 交互入口（TD.md §7 契约，字段追溯作为新工具契约）
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
| Q20 | 无 S5 | S5 取消，交互入口走 MCP serve | TD.md 已有 MCP 契约 |

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
- 性能基准 benchmarks/（§11：pprof 构建时间/内存记录）
- v2 计划：MCP serve、增量更新、泛型完整支持、channel 元素追踪

---

**文档结束**。本版由 go-cpg v1.0 设计文档（2026-08-13 之前版本）整体适配而来：保留全部 SSA 语义与映射规则，重塑为 codeintel 适配器形态；§14 为 2026-08-14 实现阶段需求增补记录。
