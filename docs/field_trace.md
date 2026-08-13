# Go-CPG 设计文档（v1 最终锁定版）

**项目名称**：`go-cpg` —— Go 代码理解与数据追溯工具  
**版本**：v1.0（最终锁定）  
**日期**：2026-08-13  
**状态**：已确认，进入实现阶段  
**变更说明**：本版整合了全部 67 项设计决策（Q1–Q67），并补充 map/slice 等追踪推迟至 v2 的约束。

---

## 目录

1. [项目背景与目标](#1-项目背景与目标)
2. [核心用户场景](#2-核心用户场景)
3. [总体架构](#3-总体架构)
4. [数据模型（CPG Schema）](#4-数据模型cpg-schema)
5. [存储层：SQLite 设计](#5-存储层sqlite-设计)
6. [核心算法](#6-核心算法)
7. [外部依赖与摘要系统](#7-外部依赖与摘要系统)
8. [模块划分与目录结构](#8-模块划分与目录结构)
9. [性能与容灾策略](#9-性能与容灾策略)
10. [实现路线图](#10-实现路线图)
11. [测试策略](#11-测试策略)
12. [附录：决策记录](#12-附录决策记录)

---

## 1. 项目背景与目标

### 1.1 动机
在大型 Go 代码库中，理解**数据流向**（尤其是结构体字段的读取、修改、传递）是代码审查、重构、故障排查和知识传承的核心需求。现有工具（如 `gopls`）侧重于语法导航和类型检查，缺乏**跨函数的细粒度字段追溯**能力；而安全分析工具（如 Joern）虽强大，但过于重载、查询复杂度高，且不完全适配 Go 的生态。

本项目旨在填补这一空白：构建一个**轻量、快速、可交互**的代码分析工具，专注于**字段级别的数据溯源与使用追踪**，服务于开发者日常的“学习与查找”场景。

### 1.2 目标
- **核心能力**：
  ① 给定任意函数，列出其直接/间接读取和编辑的所有结构体字段（全路径 `a.b.c`，类型限定）；
  ② 给定任意字段，反向追溯其所有产生点（赋值来源），正向追溯其返回后所有使用点（消费位置）。
- **v1 非目标**：不提供漏洞扫描、安全规则匹配、污点传播、反射分析；不追踪 map/slice/array/channel 元素访问（推迟至 v2）。
- **v2 计划**：交互式 shell（S5）、增量更新（`--update`）、map/slice 等复合类型元素追踪、泛型完整支持。

### 1.3 适用规模
- 目标代码库：**10 万～50 万行 Go 代码**（中型项目，约 200～500 个包）。
- 分析入口：**单个 Go Module**（含 `go.mod`）；`go.work` 场景下报错并提示用户进入具体模块目录。

---

## 2. 核心用户场景

| 场景 ID | 用户动作 | 期望结果 |
| :--- | :--- | :--- |
| S1 | `go-cpg analyze --func=github.com/x/payment.(*Service).Process` | 列出该函数直接/间接读写的所有字段，按 `direct_read` / `direct_write` / `indirect_write` 分组，显示类型路径、实例路径、行号、代码片段。 |
| S2 | `go-cpg trace --field=github.com/x/payment.Request.Amount --func=github.com/x/payment.(*Service).Process` | 追溯该字段在 `Process` 函数中的来源（产生点），输出树形路径（缩进 + 边类型 + 节点名 + 行号）。 |
| S3 | `go-cpg trace --field=github.com/x/payment.Request.Amount --func=github.com/x/payment.(*Service).Process --forward` | 以字段对象/引用为追踪目标，追溯该字段在 `Process` 返回后（调用方）的后续读写，输出调用链缩进树。 |
| S4 | `go-cpg export --out=analysis.json` | 生成双层索引 JSON（函数→字段，字段→函数），用于 IDE 或脚本二次分析。 |
| S5 | `go-cpg shell` | 交互式 REPL（**v2 实现**，v1 仅预留命令字）。 |

---

## 3. 总体架构

```
┌───────────────────────────────────────────────────────────────────┐
│                         Go Source Repository                      │
│                       (go.mod + .go files)                       │
└────────────────────────────┬──────────────────────────────────────┘
                             │
                             ▼
┌───────────────────────────────────────────────────────────────────┐
│                         Loader Module                            │
│         (go/packages + go/types + go/parser)                     │
│   - 解析 workspace、构建标签、vendoring、module 依赖              │
│   - 输出 AST + Types + Imports                                   │
│   - Go 1.22+，默认当前 GOOS/GOARCH，cgo 关闭                     │
└────────────────────────────┬──────────────────────────────────────┘
                             │
                             ▼
┌───────────────────────────────────────────────────────────────────┐
│                         SSA Builder                              │
│         (golang.org/x/tools/go/ssa)                              │
│   - 构建完整 SSA IR (Program, Packages, Functions)               │
│   - 保留源映射 (line numbers, file paths)                       │
└────────────────────────────┬──────────────────────────────────────┘
                             │
                             ▼
┌───────────────────────────────────────────────────────────────────┐
│                    Pointer Analysis & Call Graph                 │
│         (x/tools/go/pointer + callgraph)                         │
│   - 解析接口调用、函数值、闭包、方法表达式                          │
│   - 生成 Alias Map (Value → PointsToSet)                        │
│   - 默认精确模式，--pointer-mode=quick 回退 RTA                  │
└────────────────────────────┬──────────────────────────────────────┘
                             │
                             ▼
┌───────────────────────────────────────────────────────────────────┐
│                    CPG Builder (核心构建器)                       │
│   - 遍历 SSA 指令，提取 FieldAddr / Field / Store / Call        │
│   - 生成 NODE_FIELD_ACCESS（类型限定路径 + 实例路径）           │
│   - 生成边：DATA_FLOW, FUNCTION_CONTAINS, ARGUMENT, RETURNS,    │
│     ALIAS, CALL, INDIRECT_WRITE, SUMMARY_IO, FIELD_CONTAINS,    │
│     PHI_OPERAND                                                │
│   - 预计算函数字段读写摘要表                                    │
│   - 写入 SQLite (nodes / edges / function_field_summary)        │
└────────────────────────────┬──────────────────────────────────────┘
                             │
                             ▼
┌───────────────────────────────────────────────────────────────────┐
│                       Query Engine                               │
│   - 基于 SQLite 递归 CTE 实现双向追溯                            │
│   - 聚合函数层：字段读/写集合（直接查摘要表）                    │
│   - 输出格式化：CLI 表格 / JSON / DOT                            │
└────────────────────────────┬──────────────────────────────────────┘
                             │
                             ▼
┌───────────────────────────────────────────────────────────────────┐
│                    CLI / Export / Shell                          │
└───────────────────────────────────────────────────────────────────┘
```

---

## 4. 数据模型（CPG Schema）

### 4.1 节点（Node）类型

所有分析单元均抽象为节点，写入 `nodes` 表。`kind` 字段标识节点类型。v1 节点类型如下：

| `kind` 值 | 说明 | 关键属性 |
| :--- | :--- | :--- |
| `FILE` | 源文件 | `file_path` |
| `PACKAGE` | Go 包 | `package_path` |
| `FUNCTION` | 函数/方法（含闭包、匿名函数） | `name`（含接收者，如 `(*Server).Handle` 或编译器内部名），`file_path`, `line_start`, `line_end` |
| `TYPE` | 结构体类型定义 | `name`（类型限定路径，如 `pkg.User`），`extra` 含字段列表 |
| `FIELD_ACCESS` | 结构体字段访问（**实例槽**） | `full_path`（类型限定路径，如 `github.com/x/payment.Request.Amount`），`instance_path`（变量访问链，如 `req.Amount`），`access_kind`（`read`/`write`/`readwrite`，v1 实际使用 `read`/`write`），`type_string`，`line_start`，`code_snippet` |
| `CALL_SITE` | 函数调用点 | `target_func`（被调函数名，可选），`line_start`，`extra` 含调用类型（`static`/`interface`/`function_value`/`closure`/`goroutine`/`defer`）及可能目标列表 |
| `SSA_VALUE` | 所有 SSA 值（临时变量、参数、接收者、局部变量、全局变量、字面量、Phi、Alloc、Call 返回值等） | `name`（变量名或 SSA 表示），`type_string`，`extra` 含 `origin_kind`（`param`/`local`/`receiver`/`global`/`literal`/`phi`/`alloc`/`call_result` 等）、`ssa_op` |
| `EXTERNAL_SUMMARY` | 外部库摘要函数 | `name`，`summary_json`（声明读/写字段模式） |

**节点统一说明**：v1 废弃原设计中的 `RECEIVER`、`PARAM`、`LOCAL_VAR`、`GLOBAL_VAR`、`LITERAL`、`PHI` 独立 `kind`，统一为 `SSA_VALUE`，通过 `extra.origin_kind` 区分。这保证 Def-Use 链完整，避免冗余节点类型。

**字段访问节点**：每个 SSA `Field` / `FieldAddr` 指令生成一个 `FIELD_ACCESS` 节点；`access_kind` 根据指令性质确定（见 6.1）。同一源码位置的复合读写（如 `x.a = x.a + 1`）会生成两个独立节点，分别标记 `read` 和 `write`。`readwrite` 保留在枚举中但 v1 不使用。

### 4.2 边（Edge）类型

节点间关系写入 `edges` 表，`kind` 字段标识关系类型。v1 边类型如下：

| `kind` 值 | 方向 | 含义 |
| :--- | :--- | :--- |
| `DATA_FLOW` | 定义 → 使用 | SSA Def-Use 链（直接值传递，包括 extract、store 等） |
| `FUNCTION_CONTAINS` | 函数 → 内部节点 | 函数与其内部所有相关节点的包含关系 |
| `ARGUMENT` | 实参节点 → 形参节点 | 调用点实参 → 被调函数形参 |
| `RETURNS` | 被调函数返回值 → 调用点接收变量 | 跨函数返回赋值；多返回值时返回 tuple SSA_VALUE，extract 通过 DATA_FLOW 拆解 |
| `ALIAS` | 源变量 → 目标变量 | 指针分析结果（`property='may_alias'`），仅存储参与字段访问的变量 |
| `CALL` | 调用点 → 被调函数 | 控制流调用关系，`property` 标注调用类型（`static`/`interface`/`function_value`/`closure`/`goroutine`/`defer`/`normal`） |
| `INDIRECT_WRITE` | 调用者函数 → 被调函数/虚拟节点 | 标记调用者通过被调函数间接修改字段；项目内函数指向被调函数，外部摘要指向虚拟字段节点 |
| `SUMMARY_IO` | 外部摘要函数 → 字段路径 | 声明该函数会读/写某字段（用于摘要传播） |
| `FIELD_CONTAINS` | 结构体类型 → 字段 | 类型定义与字段的关系（用于类型导航） |
| `PHI_OPERAND` | Phi 节点 → 前驱值 | 每个分支输入（SSA Phi 的入边） |

**移除的边类型**：v1 不实现 `AST_PARENT`，仅通过 `FUNCTION_CONTAINS` 关联函数内部节点；行号上下文直接从节点字段读取。

---

## 5. 存储层：SQLite 设计

### 5.1 数据库文件
- 分析结果保存在 `./cpg.db`（可指定 `--out` 路径）。
- 使用 `modernc.org/sqlite`（纯 Go，无 CGO，支持 JSON1）。
- 启用 `WAL` 模式。
- **增量分析**：v1 仅支持全量重建。若目标数据库已存在且未显式指定 `--rebuild`，则报错并提示用户。

### 5.2 表定义

```sql
-- 元信息表
CREATE TABLE meta (
    key TEXT PRIMARY KEY,
    value TEXT
);
-- 初始化时插入 ('schema_version', '1')

-- 节点表
CREATE TABLE nodes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    kind TEXT NOT NULL,            -- 节点类型（见 4.1）
    name TEXT,                     -- 标识名（函数名、变量名等）
    full_path TEXT,                -- FIELD_ACCESS：类型限定路径；全局变量：pkg.VarName
    instance_path TEXT,            -- FIELD_ACCESS：变量访问链（如 req.Amount）；其他节点可空
    access_kind TEXT CHECK(access_kind IN ('read','write','readwrite')), -- 仅 FIELD_ACCESS 使用
    code_snippet TEXT,             -- 代码片段（最多 500 字符）
    package_path TEXT,
    file_path TEXT,
    line_start INTEGER,
    line_end INTEGER,
    type_string TEXT,
    is_external BOOLEAN DEFAULT 0,
    extra JSON                     -- 存储 origin_kind、ssa_op、generated 标记、调用类型等扩展信息
);

-- 边表
CREATE TABLE edges (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    from_id INTEGER NOT NULL,
    to_id INTEGER NOT NULL,
    kind TEXT NOT NULL,
    property TEXT,                 -- 如 'direct'/'indirect'/'may_alias'/调用类型/字段名
    FOREIGN KEY(from_id) REFERENCES nodes(id) ON DELETE CASCADE,
    FOREIGN KEY(to_id) REFERENCES nodes(id) ON DELETE CASCADE
);

-- 函数字段摘要表（构建时预计算，加速 S1 查询）
CREATE TABLE function_field_summary (
    function_id INTEGER NOT NULL,
    access_kind TEXT CHECK(access_kind IN ('direct_read','direct_write','indirect_write')),
    field_path TEXT NOT NULL,      -- 类型限定路径（同 FIELD_ACCESS.full_path）
    FOREIGN KEY(function_id) REFERENCES nodes(id) ON DELETE CASCADE
);

-- 索引（针对核心查询优化）
CREATE INDEX idx_nodes_full_path ON nodes(full_path);
CREATE INDEX idx_nodes_instance_path ON nodes(instance_path);
CREATE INDEX idx_nodes_kind ON nodes(kind);
CREATE INDEX idx_nodes_package ON nodes(package_path);
CREATE INDEX idx_edges_from ON edges(from_id);
CREATE INDEX idx_edges_to ON edges(to_id);
CREATE INDEX idx_edges_kind ON edges(kind);
CREATE INDEX idx_edges_from_kind ON edges(from_id, kind);
CREATE INDEX idx_edges_to_kind ON edges(to_id, kind);
CREATE INDEX idx_summary_func_access ON function_field_summary(function_id, access_kind);
CREATE INDEX idx_summary_field ON function_field_summary(field_path);
```

**Schema 版本管理**：打开数据库时检查 `meta` 表中 `schema_version`，若不存在或版本不匹配，提示用户使用 `--rebuild` 重新构建（退出码 3）。

### 5.3 并发与事务
- 构建阶段使用**批量插入**（`BEGIN TRANSACTION` / `COMMIT`），减少 I/O。
- 查询阶段只读，无需事务。
- 使用 `WAL` 模式提升并发性能。

---

## 6. 核心算法

### 6.1 SSA 指令到 FIELD_ACCESS 映射规则

构建器遍历 SSA 指令，按下述规则生成 `FIELD_ACCESS` 节点和 `DATA_FLOW` 边：

| SSA 指令 | 生成节点 | access_kind | 边连接 |
| :--- | :--- | :--- | :--- |
| `FieldAddr`（取字段地址） | `FIELD_ACCESS` 节点 | `write`（通常用于后续 Store） | `DATA_FLOW` 边从基地址 SSA_VALUE 连到该字段节点 |
| `Field`（读字段） | `FIELD_ACCESS` 节点 | `read` | `DATA_FLOW` 边从字段节点连到指令结果 SSA_VALUE |
| `Store`（写入） | 不新建节点 | 若目标为已有 `FieldAddr` 节点，确保其 `access_kind='write'`；若目标不是字段访问，则忽略字段相关处理 | `DATA_FLOW` 边从写入值 SSA_VALUE 连到目标 `FIELD_ACCESS` 节点 |
| 复合读写（如 `x.a = x.a + 1`） | 分别由 `Field` 和 `FieldAddr` 生成两个节点 | `read` 和 `write` | 分别对应上述边 |

- `full_path` 生成：基于 SSA 值/表达式的**静态类型**解析出类型声明包路径和类型名，再拼接字段链（如 `pkg.Request.Amount`）。对于嵌套字段，递归解析中间结构体类型的声明类型。若静态类型解析失败，回退到源码字面量路径并记录警告。
- `instance_path` 生成：基于源码变量名和字段链（如 `req.Amount`，或 `a.b.c`）。全局变量的 `full_path` 和 `instance_path` 均为 `pkg.VarName`。
- 嵌入字段：`full_path` 始终使用**声明字段的结构体类型路径**（如嵌入类型的字段），保证全局唯一；`instance_path` 保留源码访问形式。
- 类型别名：使用 `go/types.Unalias` 解析为原始类型后再生成类型限定路径。
- 未导出字段：与导出字段同等对待。
- 生成代码：识别 `// Code generated ... DO NOT EDIT.` 标记，在节点 `extra` 中记录 `generated=true`，默认仍分析。

### 6.2 函数 → 字段读取/编辑（场景 S1）

**输入**：函数名 `funcName`（强制完整包路径，如 `github.com/x/payment.(*Service).Process`）。  
**输出**：按 `direct_read`、`direct_write`、`indirect_write` 分组的字段列表。

**实现**：直接查询构建期预计算的 `function_field_summary` 表，无需动态遍历调用图。查询步骤：

1. 根据 `funcName` 在 `nodes` 中找到 `FUNCTION` 节点 id。
2. 查询 `function_field_summary` 获取该函数 id 下的所有行。
3. 将 `access_kind` 映射为输出分组。
4. 关联 `nodes` 获取每个字段路径对应的实例路径、行号、代码片段（可连接 `FIELD_ACCESS` 节点或从摘要表冗余存储，实际实现可在摘要表冗余列存储 `instance_path`/`line_start` 以优化查询）。

**间接写范围**：任意深度调用链——从调用者函数出发沿调用图可达的所有被调函数，若其内部存在 `write` 的 `FIELD_ACCESS` 节点，且该字段通过指针别名与调用者作用域内变量关联，则标记为间接写。构建时预计算此集合并写入摘要表。

### 6.3 字段 → 产生点追溯（反向，场景 S2）

**输入**：字段全路径（类型限定，如 `github.com/x/payment.Request.Amount`），入口函数 `funcName`（限定范围）。  
**输出**：从产生点到该字段的完整路径树（缩进格式，每条路径包含节点和边类型）。

**SQL 递归 CTE**（使用预定义模板，参数化查询；递归使用 `UNION` 去重 + 深度限制）：

```sql
WITH RECURSIVE def_trace(id, depth, path_nodes, edge_kinds) AS (
    -- 起点：目标字段节点（限定在入口函数内）
    SELECT n.id, 0, n.instance_path, ''
    FROM nodes n
    JOIN edges e ON e.to_id = n.id
    JOIN nodes func ON e.from_id = func.id
    WHERE n.full_path = ?
      AND func.kind = 'FUNCTION' AND func.name = ?
      AND n.kind = 'FIELD_ACCESS'
    UNION
    -- 反向遍历 DATA_FLOW, ARGUMENT, RETURNS, ALIAS, PHI_OPERAND
    SELECT e.from_id, d.depth + 1,
           d.path_nodes || ' -> ' || n_prev.name,
           d.edge_kinds || ',' || e.kind
    FROM edges e
    JOIN def_trace d ON e.to_id = d.id
    JOIN nodes n_prev ON e.from_id = n_prev.id
    WHERE e.kind IN ('DATA_FLOW', 'ARGUMENT', 'RETURNS', 'ALIAS', 'PHI_OPERAND')
      AND d.depth < ?   -- 默认 8，--max-depth 可调
)
SELECT id, depth, path_nodes, edge_kinds
FROM def_trace
ORDER BY depth DESC;
```

**输出格式化**：按深度渲染为缩进树，每行格式：`缩进 + 边类型前缀（如 ← DATA_FLOW）+ 节点名 + (行号)`。多条路径分行显示，路径间空行；不合并重复前缀。

### 6.4 字段 → 后续使用追踪（正向，场景 S3）

**追踪对象**：以**字段对象/引用**为追踪目标，而非仅返回值。从入口函数返回后，继续沿 `ALIAS`/`DATA_FLOW`/`RETURNS`/`CALL` 正向，直到下一次 `FIELD_ACCESS`（读或写）。

**输入**：字段全路径，入口函数 `funcName`。  
**输出**：从函数返回后到调用链下游的使用路径（缩进树）。

**实现**：递归 CTE 正向遍历。起点为入口函数节点；沿 `RETURNS`、`DATA_FLOW`、`CALL`、`ALIAS` 边向外扩展，同时追踪承载该字段的变量/指针。路径中若遇到 `FIELD_ACCESS` 节点且其 `full_path` 与目标字段匹配，则作为使用点输出。深度默认 8（`--max-depth` 可调）。递归使用 `UNION` 去重。

---

## 7. 外部依赖与摘要系统

### 7.1 使用的 Go 标准及扩展库
- `golang.org/x/tools/go/packages`：加载 workspace。
- `golang.org/x/tools/go/ssa` / `ssautil`：构建 SSA。
- `golang.org/x/tools/go/pointer`：指针分析（默认 precise，快速模式回退 RTA）。
- `golang.org/x/tools/go/callgraph`：调用图。
- `modernc.org/sqlite`：SQLite 驱动（纯 Go，无 CGO）。

### 7.2 项目内外划分
以 `go.mod` 的 `module` 路径为前缀：所有以该前缀开头的包路径视为**项目内**，精确分析；其余视为**外部依赖**，仅使用摘要或跳过。

### 7.3 内置摘要（标准库常见模式）
对以下标准库函数提供手写摘要，声明它们会读/写传入结构体的哪些字段：

- `encoding/json.Unmarshal(data []byte, v any)`：写入 `v` 的所有字段（递归）。
- `fmt.Printf(format string, args ...any)`：读取所有 `args` 的字段（保守策略）。
- `net/http` 相关：`Request` 的 `Body`、`Header`、`Form` 等字段的读/写模式。
- `database/sql` 的 `Rows.Scan(dest ...any)`：写入 `dest` 的指向值。
- `context.Context`：视为透明传递，不分析内部。

### 7.4 用户自定义摘要
用户可在模块根目录放置 `cpg-summary.yaml`，格式如下：

```yaml
summaries:
  - func: "github.com/mycorp/internal/db.InsertUser"
    reads: ["user.ID", "user.Name"]        # 读取这些字段（类型限定路径）
    writes: ["user.CreatedAt"]             # 写入这些字段
    param_index: 1                         # 操作第几个参数（0 为接收者）
```

解析规则：
- 文件必须位于模块根目录，文件名固定为 `cpg-summary.yaml`。
- 若文件不存在，使用内置摘要。
- 若存在但 YAML 解析失败，报错并中止构建（退出码 1）。
- 同一函数重复定义视为错误。
- 字段路径使用类型限定路径；若与实际参数类型不匹配，输出警告并忽略该条摘要，不中断构建。

### 7.5 摘要应用机制
构建器遇到调用带摘要的外部函数（项目外）时：
1. 根据摘要的 `param_index` 和实际参数类型，在调用点生成**虚拟 `FIELD_ACCESS` 节点**（`is_external=1`，`access_kind` 根据摘要声明设为 `read` 或 `write`）。
2. 生成 `EXTERNAL_SUMMARY` 节点（若尚未存在）和 `SUMMARY_IO` 边，从摘要节点连接到虚拟字段节点。
3. 生成 `INDIRECT_WRITE` 边（若摘要声明写入）从调用者函数直接指向虚拟字段节点，保证间接写可查询。

项目内函数间接写**不生成虚拟节点**，而是通过 `INDIRECT_WRITE` 边从调用者函数指向被调函数（见 6.2），查询时沿该边收集被调函数内实际写入的字段节点。

---

## 8. 模块划分与目录结构

```
go-cpg/
├── cmd/
│   └── go-cpg/
│       └── main.go                 # CLI 入口
├── internal/
│   ├── loader/                     # go/packages 加载，输出 SSA 构建所需
│   │   ├── loader.go
│   │   └── workspace.go
│   ├── builder/                    # CPG 构建核心
│   │   ├── builder.go              # 主流程：遍历 SSA，调用各子构建器
│   │   ├── field_extractor.go      # 提取 FieldAddr/Field/Store，生成 FIELD_ACCESS
│   │   ├── callgraph_builder.go    # 构建调用边，调用指针分析
│   │   ├── alias_builder.go        # 生成 ALIAS 边（仅字段访问相关变量）
│   │   ├── indirect_writer.go      # 间接写分析和 INDIRECT_WRITE 边生成
│   │   ├── summary_applier.go      # 应用内置/用户摘要，生成虚拟节点
│   │   └── function_summary.go     # 预计算 function_field_summary 表
│   ├── storage/                    # SQLite 操作层
│   │   ├── db.go                   # 初始化、迁移、schema 版本检查
│   │   ├── node_writer.go          # 插入节点（批量）
│   │   ├── edge_writer.go          # 插入边
│   │   └── queries.go              # 预定义查询（CTE）及摘要查询
│   ├── query/                      # 查询引擎
│   │   ├── field_reads.go          # 场景 S1
│   │   ├── trace_backward.go       # 场景 S2
│   │   ├── trace_forward.go        # 场景 S3
│   │   └── export.go               # 生成 JSON
│   └── model/                      # 内部数据结构
│       ├── node.go
│       ├── edge.go
│       └── summary.go
├── pkg/                            # 可导出的公共 API（供其他工具集成）
│   └── cpg/
│       ├── client.go               # 封装查询接口
│       └── types.go
├── testdata/                       # 测试用小型 Go 项目
│   ├── basic/                      # 结构体字段读写、方法、嵌入字段、全局变量
│   ├── pointer/                    # 指针参数、别名、指针字段修改
│   ├── interface/                  # 接口方法调用、动态分发
│   ├── closure/                    # 闭包捕获外层变量和字段
│   └── external/                   # 调用标准库摘要和自定义摘要
├── summaries/                      # 内置摘要（Go 文件）
├── go.mod
├── go.sum
└── README.md
```

### 8.1 公共 API（`pkg/cpg`）

对外暴露的最小 API 集合：

```go
package cpg

type AnalyzeOptions struct {
    ModuleDir     string   // 模块根目录，默认当前目录
    EntryFuncs    []string // 额外入口函数（可选）
    IncludeTests  bool     // 是否包含 _test.go
    BuildTags     []string // 构建标签
    Platform      string   // "GOOS/GOARCH"，默认当前
    PointerMode   string   // "precise"（默认）或 "quick"
    CacheDir      string   // 缓存目录，默认 ModuleDir/.cpg-cache
    OutputDB      string   // SQLite 输出路径
    Strict        bool     // 错误即中止
    Verbose       bool     // 详细日志
    NoCache       bool     // 禁用缓存
    Rebuild       bool     // 是否强制重建；若输出存在且未指定则报错
    MaxDepth      int      // 查询递归深度，默认 8
}

type FieldSet struct {
    DirectReads    []FieldInfo
    DirectWrites   []FieldInfo
    IndirectWrites []FieldInfo
}

type FieldInfo struct {
    TypePath     string // 类型限定路径
    InstancePath string // 实例路径
    Line         int
    CodeSnippet  string
}

type TracePath struct {
    Nodes []TraceNode
}

type TraceNode struct {
    NodeID   int
    Name     string
    EdgeKind string
    Line     int
}

func Analyze(opts AnalyzeOptions) (*DB, error)
func (db *DB) GetFunctionFields(funcName string) (*FieldSet, error)
func (db *DB) TraceBackward(field, funcName string) ([]TracePath, error)
func (db *DB) TraceForward(field, funcName string) ([]TracePath, error)
func (db *DB) ExportJSON() ([]byte, error)
func (db *DB) Close() error
```

---

## 9. 性能与容灾策略

| 问题 | 策略 |
| :--- | :--- |
| **大型项目内存爆炸** | ① 默认**只分析入口函数可达的子图**（Call Graph 闭包），而非全程序；② SSA 和指针分析结果序列化缓存于 `.cpg-cache/`，复用；③ 构建时直接写入 SQLite，不保存整图内存。 |
| **SQLite 文件过大** | 定期执行 `VACUUM`；字段 `code_snippet` 限制长度；使用索引而非冗余字段。 |
| **指针分析超时** | 提供 `--pointer-mode=quick` 选项，使用 RTA 快速模式（牺牲精度换取速度）。 |
| **递归 CTE 深度爆炸** | 设置深度限制（默认 8 层），可通过 `--max-depth` 调整；递归使用 `UNION` 去重。 |
| **并发分析** | 构建阶段利用 `go/packages` 的并行加载，但 SSA 构建和指针分析为单线程（`go/ssa` 要求）。 |
| **构建错误** | 默认跳过错误包并输出警告到 stderr，继续分析其余包；`--strict` 选项使任何错误导致构建中止；主模块入口包错误无论是否 strict 都中止。 |
| **不可达函数** | 所有项目内函数构建 SSA 并保留节点，但不可达函数不参与指针分析，因此无数据流边连接。 |
| **缓存失效** | 缓存 key = hash(go.mod 路径, 所有 .go 文件 mtime+size, 工具版本, 构建 tags/platform)。 |
| **缓存内容** | 序列化 SSA Program 和指针分析结果（gob 格式）到模块根目录 `.cpg-cache/`；SQLite 数据库不作为缓存，每次显式 `--rebuild` 生成。 |

---

## 10. 实现路线图

### v1（8 周）

| 阶段 | 里程碑 | 主要工作 |
| :--- | :--- | :--- |
| **Phase 0** | 项目脚手架、SQLite 迁移、基础加载器 | 目录结构、meta 表、schema 版本、Loader 支持 tags/platform/cgo 关闭 |
| **Phase 1** | SSA 构建 + 节点写入（FUNCTION/TYPE/SSA_VALUE） | 统一 SSA_VALUE，origin_kind 标记，FUNCTION_CONTAINS 边 |
| **Phase 2** | 字段访问提取（FIELD_ACCESS）及 DATA_FLOW 边 | 映射规则，full_path/instance_path，嵌入字段规范化 |
| **Phase 3** | 指针分析 + 调用图 + ALIAS 边构建 | 精确/快速模式，接口/闭包/函数值调用，ALIAS 仅字段相关变量 |
| **Phase 4** | 跨过程 ARGUMENT / RETURNS 边，间接写标记 | 项目内间接写分析，INDIRECT_WRITE 边，function_field_summary 预计算 |
| **Phase 5** | 查询引擎（S1, S2, S3）CLI 实现 | 递归 CTE 模板，输出格式化（表格/树形），深度限制 |
| **Phase 6** | JSON 导出、内置摘要、用户摘要支持 | 双层索引 JSON，外部摘要虚拟节点，YAML 解析 |
| **Phase 7** | 测试、文档、性能调优 | 单元/端到端测试，缓存实现，性能基准 |

### v2 计划
- 交互式 shell（S5）
- 增量更新（`--update`）
- map/slice/array/channel 元素追踪
- 泛型实例化完整支持
- CLI 过滤选项（如排除生成代码）

---

## 11. 测试策略

- **单元测试**：每个内部模块（`builder`, `storage`, `query`）用 `testing` 包覆盖，使用 `testdata/` 中的小型项目。
- **端到端测试**：
  - 使用 `testdata/basic`、`pointer`、`interface`、`closure`、`external` 五个项目，为每个项目编写 `expected.json` 用于对比查询结果。
  - 选取开源项目（如 `gin` 的示例）作为基准，手动标注若干函数字段读写期望，验证工具输出。
- **性能基准**：使用 `pprof` 分析构建时间和内存，记录在 `benchmarks/` 中。
- **SQL 查询测试**：单独测试递归 CTE 在 `modernc.org/sqlite` 上的正确性和效率。

---

## 12. 附录：决策记录

以下列出访谈中确定的关键决策点（Q1～Q67 及补充），供参考：

| 决策编号 | 决策 | 选择 | 理由/说明 |
| :--- | :--- | :--- | :--- |
| Q1 | SSA 值建模 | 新增 `SSA_VALUE` 节点，统一承载所有 SSA 值；`origin_kind` 区分 | 保证 Def-Use 链完整 |
| Q2 | 函数作用域关联 | 新增 `FUNCTION_CONTAINS` 边 | 查询直接定位函数内部节点 |
| Q3 | 字段全路径标识 | 类型限定 `full_path` + 实例 `instance_path` | 全局唯一 + 可读展示 |
| Q4 | FIELD_ACCESS 读写属性 | 独立列 `access_kind` | 避免 JSON 函数查询，性能好 |
| Q5 | 调用图算法 | pointer analysis 默认，quick 回退 RTA | 平衡精度与速度 |
| Q6 | S3 正向追溯语义 | 以字段对象/引用为追踪目标 | 覆盖指针修改传递场景 |
| Q7 | Go 版本与构建约束 | Go 1.22+，默认环境，cgo 关闭，--tags/--platform | 兼容主流 |
| Q8 | SQLite 驱动 | `modernc.org/sqlite` | 纯 Go，无 CGO |
| Q9 | 增量更新 | v1 仅 `--rebuild` | 简化实现 |
| Q10 | CLI 标志与输出 | 函数名含包路径，field 类型路径，--instance-path 可选 | 明确无歧义 |
| Q11 | 缓存失效 | hash(go.mod 路径, 文件 mtime+size, 工具版本, tags/platform) | 可靠 |
| Q12 | 未导出字段 | 分析所有源码内可访问字段 | 覆盖完整 |
| Q13 | 读写粒度 | 读/写独立节点，readwrite 不用 | 精确 |
| Q14 | 闭包建模 | 闭包独立 FUNCTION 节点 | 逻辑清晰 |
| Q15 | 全路径生成 | SSA 静态类型优先，失败回退字面量 | 全局统一 |
| Q16 | 外部摘要应用 | 调用点生成虚拟 FIELD_ACCESS | 避免深入外部源码 |
| Q17 | JSON 导出结构 | 双层索引，producers/consumers 含函数/行号/实例路径/代码片段 | 满足二次处理 |
| Q18 | CLI 表格列 | GROUP/TYPE_PATH/INSTANCE_PATH/LINE/CODE | 信息充分 |
| Q19 | 构建错误处理 | 默认跳过，--strict 中止；入口包错误必中止 | 容灾 |
| Q20 | v1 范围 | 无 S5、无增量更新、指针 precise/quick | 聚焦核心 |
| Q21 | 泛型 | v1 不专门处理实例化 | 限制说明 |
| Q22 | 查询 SQL | 预定义模板 + 参数化 | 安全可维护 |
| Q23 | 分析入口 | 所有导出函数 + main + init，--entry 追加 | 覆盖大部分生产代码 |
| Q24 | 函数标识符语法 | 强制完整包路径，(*Type).Method | 无歧义 |
| Q25 | 嵌入字段规范化 | 声明类型路径 | 全局唯一 |
| Q26 | 测试文件 | 默认不分析，--include-tests 启用 | 控制规模 |
| Q27 | go.work | 报错提示进入模块目录 | 单模块语义 |
| Q28 | 树形输出格式 | 缩进+边类型+节点名+(行号)，深度 8 | 清晰 |
| Q29 | S1 间接读 | 仅三组，不追踪间接读 | v1 范围 |
| Q30 | 间接写深度 | 任意深度 | 完整副作用视图 |
| Q31 | 直接/间接判定 | 以是否跨函数调用为准 | 简单一致 |
| Q32 | 项目内判定 | go.mod module 路径前缀 | 清晰 |
| Q33 | 缓存目录 | 模块根目录/.cpg-cache | 共享 |
| Q34 | 全局变量路径 | full_path=instance_path=pkg.VarName | 全局唯一 |
| Q35 | 摘要不匹配 | 警告并忽略 | 容灾 |
| Q36 | INDIRECT_WRITE 机制 | 调用点分析被调函数写字段 + 指针别名 | 精确 |
| Q37 | 节点类型统一 | 统一为 SSA_VALUE，废弃独立 kind | 简化模型 |
| Q38 | TYPE 节点 | 新增 TYPE 节点，实现 FIELD_CONTAINS | 类型导航 |
| Q39 | AST_PARENT | 不实现 | 已用 FUNCTION_CONTAINS |
| Q40 | 查询性能 | 构建时预计算 function_field_summary | 查询快 |
| Q41 | 缓存内容 | gob 序列化 SSA/指针结果，SQLite 非缓存 | 分离 |
| Q42 | Schema 版本 | meta 表，不匹配提示 --rebuild | 安全 |
| Q43 | init 查询 | 不可直接查询，仅入口 | 无意义 |
| Q44 | 匿名函数查询 | 不可直接查询，通过外层递归包含 | 简化 CLI |
| Q45 | 公共 API | Analyze/GetFunctionFields/TraceBackward/TraceForward/ExportJSON | 最小集 |
| Q46 | 构建日志 | 简洁进度到 stderr，--verbose 详细 | 用户友好 |
| Q47 | 退出码 | 0/1/2/3/4 | 脚本集成 |
| Q48 | 生成代码 | 标记 generated=true，默认分析 | 透明 |
| Q49 | 递归 CTE 循环 | UNION 去重 + 深度限制 | 防无限 |
| Q50 | 不可达函数 | SSA 节点保留但无数据流边 | 完整语法结构 |
| Q51 | SSA 映射规则 | Field/FieldAddr 独立节点，Store 补边 | 一致 |
| Q52 | CALL_SITE 连接 | DATA_FLOW/CALL/ARGUMENT/RETURNS | 完整数据流 |
| Q53 | ALIAS 粒度 | 仅字段访问相关变量，may_alias 边 | 控制规模 |
| Q54 | 接口/动态调用 | CALL 边标注调用类型，未解析标记 | 可追踪 |
| Q55 | 内建函数 | 忽略（make/new 分配点跟踪） | v1 简化 |
| Q56 | 反射 | 不分析 | 限制 |
| Q57 | goroutine | 视为普通调用 | 简化 |
| Q58 | AnalyzeOptions | 完整字段定义 | API 清晰 |
| Q59 | 摘要 YAML 解析 | 模块根目录，解析失败中止，重复定义报错 | 一致 |
| Q60 | testdata 结构 | 五项目：basic/pointer/interface/closure/external | 覆盖场景 |
| Q61 | 复合类型元素 | 不追踪 map/slice/array/channel 元素 | 推迟 v2 |
| Q62 | 多返回值 | RETURNS 边到 tuple，Extract 用 DATA_FLOW | 完整 |
| Q63 | defer 调用 | 视为普通调用 | 简化 |
| Q64 | 方法表达式/函数值 | 通过 callgraph 解析 | 统一 |
| Q65 | --platform 格式 | GOOS/GOARCH，非法报错退出码 2 | 明确 |
| Q66 | 类型别名 | 用 go/types.Unalias 解析原始类型 | 唯一路径 |
| Q67 | 间接写虚拟节点 | 项目内函数用 INDIRECT_WRITE 边指向被调函数；外部摘要生成虚拟节点 | 明确 |

---

## 结语

本设计文档完整定义了 `go-cpg` v1 的架构、数据模型、算法、实现计划和测试策略。所有 67 项关键决策及 map/slice 等推迟项已由项目发起人确认锁定。开发团队可依据本文档开始编码工作，并将在实现过程中严格遵循设计，如有偏差需重新评估并更新文档。