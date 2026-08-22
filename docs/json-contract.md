# JSON 输出契约（Q243）

所有 `--json` / `export` 输出统一 **snake_case** 字段名，稳定承诺：
schema 变更需更新本契约并走 Q 流程（Agent/MCP 依赖字段名解析）。

## 标准

- 字段名 snake_case（`file_path`、`source_id`、`access_kind`）
- 空/零值字段省略（`omitempty`），调用方按缺失处理而非 null
- 顶层为对象（`query context`）或数组（`query table`、`relations`）
- canonical ID 一律字符串；置信度 `confidence` float64（0.0~1.0）
- 行号 `line`/`line_start` int；`file`/`file_path` 为仓库相对路径

## 核心类型（domain 层，Q243 加 tag 统一）

| 类型 | 关键字段 |
|---|---|
| CodeEntity | id / kind / name / file_path / line_start / line_end / properties |
| Fact | source_id / target_id / kind / tool_source / confidence / metadata |
| FunctionFieldSummary | function_id / access_kind / field_path / instance_path / line_start / code_snippet / origins |
| TraceRow | id / depth / parent_id / name / edge_kinds / line / is_usage / dir / kind / access / func_id / file_path / full_path / conditions / dispatch_* / edge_* |
| TableRelation | from_table / from_col / to_table / to_col / hops / type |
| RelationRule | id / from_table / from_col / to_table / to_col / created_at |
| TableColumn | name / access / line_start / writers / readers |
| SummaryStep | kind / name / file / line / func |
| TablePathStep | from_table / from_col / type / to_table / to_col |

## 各命令输出结构

- **query symbol**：对象——id/name/kind/file/line/signature/doc/callers[]/callees[]/candidates[]（callers/callees 元素：id/tool/confidence）
- **query context**：对象——symbol/fields(direct_read|direct_write|indirect_write)/chain[]/traces[]/dispatch[]/callers[]/callees[]（顶层键缺失=无数据，omitempty）
- **query fields**：对象——name/rows[]（rows 元素：access_kind/field_path/instance_path/line/code_snippet/origins）或 field/func/rows
- **query trace-backward/forward**：对象——flows[]（元素：id/name/depth/dir/edge/line/kind/access/func_id/func_name/conditions/dispatch/edge_candidate）
- **query value-trace**：对象——flows[]（同 trace，含 [读]/[写] 语义在 access）
- **query callers/callees/impact**：对象——target/rows[] 或 target/nodes[]（元素：id/name/kind/file/line）
- **query summary**：对象——producers[]/consumers[]/chain[]（producers/consumers 元素：function/access/line/instance/code）
- **query context/table**：数组——TableColumn[]（表.列虚拟节点）
- **query relations**：数组——TableRelation[]（--all 同样式）
- **query table-path**：对象——path[]/candidates[]/hops/reachable（元素 TablePathStep）
- **query unused**：对象——unused[]/total（unused 元素：id/name/kind/file/line/exported/referenced/since）
- **query module-calls**：对象——calls[]（元素：from_module/to_module/service/method/transport/caller/line）
- **query path**：对象（--since 标注）——见 field_trace.md §17
- **list**：数组——short/path/module/status/worktree_of/workspace
- **export**：对象——fields{}（field→producers/consumers）或 relations[]（graph --type）
- **rule list**：数组——RelationRule[]

## 历史（为何有本文档）

Q235-5 `query context` 直接 marshal domain.CodeEntity/Fact/FunctionFieldSummary/
TraceRow——这些类型当时无 json tag，输出 Go 默认 camelCase
（`ID`/`FilePath`/`SourceID`/`AccessKind`...），与其余命令的 snake_case
不一致，机器消费（Agent/MCP）易错。Q243：domain 全部输出类型统一加
snake_case tag；`internal/cli/json_contract_test.go` 固化契约（禁止
camelCase 键出现）。server /api 的 NodeJSON/EdgeJSON（camelCase
`fullPath`/`funcName`）是前端私有契约，不在本契约内——前端专有，不承诺。
