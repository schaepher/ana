---
name: codeintel-cli
license: 'MIT'
description: '使用 codeintel 命令行工具：Go 代码库智能索引与字段追溯查询。构建索引（init）、查询符号/字段读写摘要（query fields）、字段追溯（trace-backward/forward）、数据值全链追踪（value-trace）、调用关系（callers/callees/impact）、导出（export）、Web 服务（serve）。当用户要求分析 Go 代码库的字段数据流、追踪字段使用方/产生点、查看函数字段读写、查询调用关系时使用本 skill。'
---

# codeintel CLI 使用指南

codeintel 是 Go 代码库智能索引工具（SSA 字段追溯），输出存 SQLite（`.codeintel/codeintel.db`）。仓库 module：`github.com/schaepher/codeintel`。

## 构建与安装

```bash
# 在 codeintel 仓库内构建
go build -o codeintel ./cmd/codeintel
# 或安装到 GOBIN
make install
```

## 命令速查

```bash
codeintel init --repo <path>               # 全量构建索引（须有 go.mod；go.work 根目录会提示进模块目录）
codeintel update --repo <path>             # 增量更新（git 检测变更文件，全量分析+增量写入）
codeintel serve --repo <path> --addr :8096 # 启动图探索 Web 服务（前端 AntV G6，端口默认 :8090）
codeintel query <sub> ... --repo <path>    # 查询（见下；全部子命令支持 --json / --compact）
codeintel export --repo <path> [--out x.json]  # 导出字段双层索引 JSON（字段→产生者/消费者）
codeintel export graph --type value-trace|callees|lifecycle --target <节点> [--format mermaid|dot] [--out file]
                                           # 图导出：value-trace 默认 mermaid（函数分组）、callees 默认 dot、
                                           # lifecycle 生命周期图（[存储]/[观测]/[读]/[写]+条件标注）
codeintel clean --repo <path> --force      # 删除索引（schema 变更后必须 clean + init 重建）
codeintel version
```

## query 子命令

| 子命令 | 用途 | 关键参数 |
|---|---|---|
| `symbol <sym>` | 符号详情（含调用者/被调用者） | |
| `fields <func>` | 函数字段读写摘要（direct_read/write + indirect_write） | |
| `trace-backward <field> --func <func>` | 字段产生点反向追溯 | `--max-depth N` 默认 8 |
| `trace-forward <field> --func <func>` | 字段后续使用正向追踪 | `--max-depth N` |
| `value-trace <nodeID>` | 数据值全链（跨函数，函数上下文分组） | `--max-depth N` |
| `callers/callees <sym>` | 调用者/被调用者 | `--depth N` 默认 1 |
| `impact <sym>` | 影响分析 | `--depth N` 默认 3 |
| `summary <节点>` | 跨层摘要：入口→计算→写入→消费主链（每步带 file:line） | `--format mermaid` |

- 全部 query 子命令支持 `--json`（结构化输出）与 `--compact`（去缩进）
- 日志写入 `.codeintel/codeintel.log`（与 db 同目录），stdout 只留查询结果

- `<sym>` 接受 canonical ID（`symbol:go:<pkg>:<name>`，方法 `(T).m`）或名称（多匹配时报错列出候选）
- `<field>` 是类型限定路径（如 `example.com/app/internal/agent.Config.APIKey`）
- `<nodeID>` 是完整节点 ID（如 `symbol:go:...:(Manager).Run#m.cfg.APIKey.read@47`）

## 真实示例

以下以某 Go 仓库（module `example.com/app`，含 LLM 代理的
`m.cfg.APIKey` 字段）为例：

```bash
# 1. 构建索引（首次或 schema 变更后）
codeintel init --repo <目标仓库>

# 2. 函数字段读写摘要（验证后能看到 [direct_read]/[direct_write]/[indirect_write] 分组）
codeintel query fields "(Manager).Run" --repo <目标仓库>

# 3. 字段使用方正向追踪
codeintel query trace-forward example.com/app/internal/agent.Config.APIKey \
  --func "(Manager).Run" --repo <目标仓库>

# 4. 数据值全链（跨函数）：先查节点 ID，再追踪
sqlite3 <目标仓库>/.codeintel/codeintel.db \
  "SELECT id FROM nodes WHERE kind='field_access' AND json_extract(properties,'\$.instance_path')='m.cfg.APIKey' LIMIT 1"
codeintel query value-trace "<上面查到的ID>" --repo <目标仓库>
```

## 输出解读

- **fields 摘要**：`[direct_read]` 读字段、`[direct_write]` 写字段、`[indirect_write]` 经别名/调用闭包间接写（如 `m.mu :55 m.mu.Lock()`）。摘要表按字段 UNIQUE 去重（同一字段多处访问只列首行），明细在图节点里
- **value-trace**：`【函数名】` 分组 + 缩进树，`←` 产生链（反向）、`→` 使用链（正向），边类型（data_flows_to/argument/returns/phi_operand）、`[读]/[写]` 标记与行号
- 读链中间层（如 `m.cfg.APIKey` 的内层 `m.cfg`）标记为 read 而非 write；`[]T{...}` 字面量初始化不产元素节点
- **路径条件**：追溯行可带 `[条件: ...]`（if/类型分支/env，查询期计算）
- **动态派发**：symbol 接口类型展示候选实现（`[register 0.9]`/`[enum 0.7]` + 注册点）
- **持久化**：SQL 写映射为 `users.name` 虚拟节点（字段→表.列，经 value-trace 可见）
- **全局溯源**：全局变量跨函数共享节点（`var.<name>`），value-trace 可达初始化表达式
- **跨层摘要**：`query summary <节点>` 输出生命周期主链（entry/compute/write/consume）

## 验证与注意事项

- 改动验证矩阵：`make test`（单元）、`make it`（集成，需 scip-go）、`make e2e`（playwright 22 项，端口 8096，用 `E2E_REPO=<仓库>` 指定验证仓库）
- **schema 无自动迁移**（user_version=2）：改 schema 后验证仓库须 `clean --force` + `init` 重建，否则报版本不匹配
- 每次改完并验证完后要 `git push`（用户约定）
- 日志：zap + OTel 写入 `.codeintel/codeintel.log`（main 粗解析 --repo 传入 Setup）；
  --verbose 的 debug 日志也在文件里；stdout 仅查询结果
- 坑：`pkill -f "codeintel-e2e serve"` 会匹配自身命令行自杀（用 `pgrep -x codeintel-e2e` + kill）
- 索引查询无网络依赖；构建需 `go` 与可选的 `scip-go`（缺失时 scip 适配器降级跳过）
