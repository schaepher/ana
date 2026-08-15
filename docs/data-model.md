# codeintel 图数据模型总表

权威来源：`internal/domain/entity.go` 常量定义 + 各适配器 emit 点 + `db.go` schema。
本表为理解项目的速查总纲——节点/边/属性/置信度一页看全。

## 1. 节点 kind（EntityKind）

| kind | 含义 | 产生工具 | 说明 |
|---|---|---|---|
| `file` | 文件 | scip | ID `file:<relpath>` |
| `package` | 包（名取路径末段） | ast | ID `symbol:go:<import_path>:<basename>` |
| `function` | 函数 | scip/ast/ssa | ID `symbol:go:<pkg>:<name>` |
| `method` | 方法（`(T).m`，值/指针接收者不分） | scip/ast/ssa | ID `symbol:go:<pkg>:(T).m` |
| `struct` | 结构体 | scip/ast | |
| `interface` | 接口 | scip/ast | 接口方法不建独立节点（IsInterfaceMethod） |
| `commit` | git 提交 | git | ID `commit:<sha>` |
| `object` | struct 实例化对象（值流锚点） | ast | ID `symbol:go:<pkg>:<name>#obj.<id>` |
| `field_access` | 字段访问实例槽 | ssa | ID `<func>#<instance>.<read\|write>@<line>`；properties 有 full_path |
| `ssa_value` | SSA 值（param/local/phi/alloc/global） | ssa | ID `<func>#<slot>`；global 为 `<pkg>:var.<name>` |
| `external_summary` | 外部库摘要函数（field-summary.yaml） | ssa | |
| `parameter` | 函数签名参数（前端展开） | ssa | ID `<func>#param.<name>` |
| `receiver` | 方法接收者（与参数区分展示） | ssa | ID `<func>#param.recv` |
| `result` | 函数返回值 | ssa | ID `<func>#result.<n>` |
| `grpc_service` | gRPC 服务标识 | ast | ID `symbol:go:<genpkg>:svc.<Xxx>` 或 `symbol:proto:<protopkg>:svc.<Xxx>` |
| `http_route` | HTTP 路由（routes.yaml 人工表） | ast | ID `symbol:go:<handler包>:route.<path>` |

## 2. 边 kind（FactKind）

### 图基础（TD.md 5.1）
| kind | source → target | 工具 | 置信度 |
|---|---|---|---|
| `calls` | 调用者 → 被调者 | codegraph | 0.8 |
| `imports` | 包 → 直接依赖的项目内包 | codegraph | 0.8 |
| `depends_on` | 包/模块 → 依赖 | — | — |
| `implements` | **接口 → 实现者**（用户确认方向） | scip | 1.0 |
| `modified_by` | 文件 → commit | git | 1.0 |
| `references` | 引用（未实现：scip 引用无法归属调用者） | — | — |
| `tests` | 测试关系 | — | — |

### AST 对象流（codegraph 0.8）
| kind | source → target | 语义 |
|---|---|---|
| `initializes` | 调用者 → struct | `&T{}`/`T{}`/`new(T)` 实例化（仅 Underlying 为 *types.Struct） |
| `uses` | 对象 → 方法 | `x.Method()` 使用处 |
| `passes_to` | **接收函数 → 参数函数**（用户确认方向） | `foo(bar)` 回调传递 |
| `passes_result` | 接收者 → 嵌套调用 callee | `A(B(C()))` → A→B、B→C 持有返回参数 |
| `of_type` | 对象 → struct 类型 | |
| `has_method` | receiver 类型 → 方法（方法线，虚线展示） | |
| `var-init calls` | 包节点 → 函数 | `var x = NewFoo()` 包级初始化调用（Q108） |

### SSA 字段追溯（ssa；data_flows_to/argument/returns/phi_operand/summary_io 1.0，alias 0.8）
| kind | source → target | 语义 |
|---|---|---|
| `data_flows_to` | 值 → 消费值 | 函数内 def-use 主链（含跨函数经跳板） |
| `argument` | 实参节点 → 形参节点 | 跨过程传参 |
| `returns` | 被调返回值 → 调用点接收变量 | |
| `alias` | 指针别名 | may-alias，conf 0.8；source 是值节点（funcID#slot） |
| `phi_operand` | Phi 节点 → 前驱值 | 分支汇合 |
| `indirect_write` | 调用者函数 → 被调函数/虚拟字段节点 | 间接写（metadata 含 call_line/call_args） |
| `dispatch_to` | 接口类型 → 候选实现方法 | 动态派发（metadata register/enum 标注注册点） |
| `summary_io` | 外部摘要函数 → 字段路径 | field-summary.yaml 应用 |
| `has_param` / `has_result` | 函数 → 签名参数/返回值节点 | |

### 模块间调用（codegraph 1.0）
| kind | source → target | 语义 |
|---|---|---|
| `grpc_call` | 客户端调用方函数 → grpc_service | metadata method/method_path/line_num |
| `grpc_impl` | 服务实现类型 → grpc_service | 服务端归属（module-calls 用） |
| `http_call` | 客户端调用方函数 → http_route | metadata url/host/path/method/line_num |

## 3. 工具来源与置信度

| tool_source | 角色 | 置信度 |
|---|---|---|
| `scip` | 符号权威（定义/行号/implements） | 1.0 |
| `codegraph` | 调用图/依赖图（AST） | 0.8 |
| `git` | 历史 | 1.0 |
| `ssa` | 字段追溯 def-use | 1.0（alias 0.8） |

- **同边合并**：edges UNIQUE(source_id, target_id, kind)，UPSERT 保留最高置信度
- **查询阈值 0.8**（TD.md 决策 10 的 0.85 与 5.1 表矛盾，0.85 会过滤全部调用边——已确认偏差）

## 4. 节点 properties（按 kind）

| 节点 | 键 |
|---|---|
| function/method | `signature`（types.ObjectString）、`doc_comment`（scip）、`serves_http`/`serves_grpc`（AST 服务入口标记） |
| field_access | `full_path`（类型限定路径）、`instance_path`、`access_kind`（read/write）、`code_snippet`、`type_string`、`func_id` |
| ssa_value | `origin_kind`（param/local/phi/alloc/global）、`ssa_op`、`type_string`、`func_id` |
| grpc_service | `service_name` |
| http_route | `path`、`method`、`handler_id` |
| commit | `date`、`message` |
| 边 metadata | `line_num`、`call_line`、`call_args`、`method`、`method_path`、`url`、`host`、`path`、`register`/`enum` |

## 5. 表结构（SQLite，user_version=2）

- `nodes(id PRIMARY KEY, kind, name, file_path, line_start, line_end, properties JSON)`
- `edges(source_id, target_id, kind, tool_source, confidence, metadata JSON, UNIQUE(source_id,target_id,kind))`
  - FK：端点节点必须存在（不存在则跳过并计数 SkippedEdges）
- `function_field_summary(function_id, access_kind, field_path, instance_path, line_start, code_snippet, FK→nodes, UNIQUE(function_id,access_kind,field_path))` —— S1 预计算摘要（field_trace.md §5.2）
- `build_metadata` —— 构建记录（tool_name/timestamp/status/commit_sha）
- schema 无自动迁移：改动表结构后验证仓库须 `codeintel clean` + `init` 重建

## 6. Canonical ID 规则

- 符号：`symbol:go:<import_path>:<name>`；方法 `(T).method`（值/指针接收者不区分，与 scip-go 一致）
- 文件：`file:<relpath>`；提交：`commit:<sha>`；包：`symbol:go:<path>:<basename>`
- SCIP 引用：`scip-go gomod <module> . \`<pkg>\`/Symbol#` 格式（FromScipSymbol 解析）
- 节点后缀：field_access `#<instance>.<read|write>@<line>`、ssa_value `#<slot>`、参数 `#param.<name>`、全局 `:var.<name>`
- 服务节点：`symbol:go:<genpkg>:svc.<Xxx>`（NewXxxClient 识别）/ `symbol:proto:<protopkg>:svc.<Xxx>`（手写 client / ServiceDesc）
