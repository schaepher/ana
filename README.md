# codeintel — Go 代码库智能索引与查询

为 AI Agent 提供大型 Go 代码库的结构化、语义化与历史感知能力。系统按
[`docs/TD.md`](docs/TD.md)（v2.0 设计文档）实现，采用六边形架构，以单一 Go
二进制部署，数据落在仓库根目录 `.codeintel/codeintel.db`（SQLite 单文件）。

## 功能

- **全量构建**：并行运行三个分析器，生成代码图（符号、调用、实现、导入、Git 历史）
- **符号查询**：函数/方法/结构体/接口的定义、签名、文档、行号
- **调用图查询**：调用者（callers）与被调用者（callees），支持多深度遍历
- **影响分析**：修改一个符号后，沿图遍历计算受影响范围（深度 ≤ 3）
- **接口实现**：结构体/方法与接口的 IMPLEMENTS 关系
- **图探索前端**（AntV G6）：初始展示顶层入口（main / HTTP 服务 / gRPC 服务），
  点击节点双向展开依赖关系
- **降级运行**：任一分析器失败不影响其余数据，构建报告标记状态

## 快速开始

依赖：

- Go 1.26+
- [scip-go](https://github.com/scip-code/scip-go)（符号索引器）：

  ```shell
  go install github.com/scip-code/scip-go/cmd/scip-go@latest
  ```

构建：

```shell
go build -o codeintel ./cmd/codeintel
```

对目标仓库建立索引：

```shell
codeintel init --repo /path/to/repo
```

图探索（浏览器）：

```shell
codeintel serve --repo /path/to/repo --addr :8090
# 打开 http://localhost:8090 —— 初始展示顶层入口，单击节点展开依赖
```

查询：

```shell
# 符号详情（支持名称或 canonical ID，模糊匹配多结果时提示用 ID）
codeintel query symbol Server --repo /path/to/repo

# 调用关系（--depth 遍历深度）
codeintel query callers 'symbol:go:example.com/svc:(Service).Handle' --repo /path/to/repo
codeintel query callees main --repo /path/to/repo --depth 2

# 影响分析
codeintel query impact 'symbol:go:example.com/svc:(Service).Handle' --repo /path/to/repo

# 清理索引
codeintel clean --repo /path/to/repo
```

## 配置（解析新项目）

codeintel 通过被分析仓库根目录的**可选 YAML 文件**增强解析——全部可选（不配置也能 init，但外部框架的表映射/字段传播/模块图 http 边会缺失）：

| 文件 | 作用 | 何时需要 |
|---|---|---|
| `field-summary.yaml` | 外部函数/接口调用的读写语义映射（ORM/SQL/回调）——外部调用 → 表.列 虚拟节点 | `query table`/`query relations` 缺表或缺写入方时（自研 ORM/DAO、非 GORM 框架） |
| `modules.yaml` | 多模块 monorepo 模块划分（`query module-calls` 模块图） | 多 go.mod 或需模块级调用图时 |
| `routes.yaml` | HTTP 路由人工表（客户端 URL ↔ 服务端 handler，模块图 http 边） | 需模块图含 HTTP 调用时 |

完整格式与字段说明见本仓库根目录的示例（复制为对应文件名放入目标仓库根目录即可）：

- `field-summary.example.yaml`——含函数摘要（func：`orm_write`/`orm_read`/`reads`/`writes`/`reads_all`/`writes_all`/`param_index`）与接口摘要（iface：`method`/`kind`（write/read/filter/sql）/`obj_arg`/`where_arg`/`id_arg`/`sql_write`）
- `modules.example.yaml`——模块前缀划分（`prefix`/`name`，最长前缀优先，未匹配归 `_root`）
- `routes.example.yaml`——HTTP 路由表（`path`/`handler`/`method`；`{id}` 段通配）

配置后重新 `codeintel init --repo <目标仓库>`（或 `reindex`）生效。

## Agent Skill

为 AI Agent 提供 codeintel 命令行使用指南（字段追溯 / 字段读写摘要 / 数据值
全链追踪等查询能力），可通过 [skills 生态](https://github.com/vercel-labs/skills)
一键安装：

```shell
# 从本仓库安装（GitHub）
npx skills add schaepher/codeintel --skill codeintel-cli

# 本地路径安装
npx skills add .claude/skills --skill codeintel-cli

# 安装到用户目录（所有项目可用），指定 agent 并跳过确认
npx skills add schaepher/codeintel --skill codeintel-cli -g -a claude-code -y
```

skill 源文件：`.claude/skills/codeintel-cli/SKILL.md`（含全部命令、参数说明与
真实示例）。安装后 agent 会自动匹配该 skill 来使用 codeintel 命令行。

## CLI 一览

```
codeintel init --repo <path>    全量构建索引
codeintel serve --repo <path>   启动图探索 Web 服务（AntV G6 前端，默认 :8090）
codeintel query <子命令>         查询（symbol / callers / callees / impact）
codeintel clean --repo <path>   删除索引数据库
```

## 架构

```
                     ┌─────────────────────────────┐
  codeintel CLI ───► │ Orchestrator（并行编排）       │
                     │  ├ scip-go 适配器（符号/实现边）│
                     │  ├ AST 适配器（调用/导入边）    │
                     │  └ Git 适配器（历史边）         │
                     └──────────────┬──────────────┘
                                    ▼
                     SQLite: nodes / edges / build_metadata
```

| 数据来源 | 产出 | 置信度 |
| :--- | :--- | :--- |
| SCIP (scip-go) | 符号定义节点、IMPLEMENTS 边 | 1.0 |
| AST (go/packages) | CALLS、IMPORTS 边 | 0.8 |
| Git | COMMIT 节点、MODIFIED_BY 边 | 1.0 |

**Canonical ID**：`symbol:go:<import_path>:<name>`，方法名统一为 `(T).method`
形式（值/指针接收者不区分）。

## 项目结构

```
cmd/codeintel/        CLI 入口
internal/domain/      领域模型与端口（六边形内核）
internal/canonicalizer/ 实体解析（canonical ID 生成、SCIP 符号解析）
internal/orchestrator/  构建编排（并行、超时、降级、报告）
internal/infrastructure/
  scip/  ast/  git/  sqlite/   各适配器与仓储实现
internal/server/      HTTP API（/api/roots、/api/expand）
internal/cli/         init / serve / query / clean 命令
assets/web/           AntV G6 前端页面（go:embed 嵌入二进制）
docs/TD.md            系统设计文档（v2.0）
```

## 当前范围与限制

- 仅支持 Go module 仓库；MVP 阶段未实现：MCP serve、增量构建、Semble/LLM 摘要
- SCIP 的引用边未入库（scip-go 的 occurrence 无法归属引用者），引用关系由调用边覆盖
- 构建/查询细节与设计文档的偏差见 `AGENTS.md`
