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

- 仅支持 Go module 仓库；MVP 阶段未实现：MCP serve、增量构建、Joern/Semble/LLM 摘要
- SCIP 的引用边未入库（scip-go 的 occurrence 无法归属引用者），引用关系由调用边覆盖
- 构建/查询细节与设计文档的偏差见 `AGENTS.md`
