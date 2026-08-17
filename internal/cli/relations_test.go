package cli

// seedTableRelations 建临时仓库 + 灌入外部表虚拟节点与数据流链
// （table_a.id 读出 → table_b.a_id 过滤，Q160 测试用）。

// TestQueryRelationsAll：query relations --all 一次返回全库关联
// （Q160）——JSON 数组含正向 query 关联，无需逐表查询。

// TestQueryRelationsAllText：--all 文本模式按表分组展示。

// TestExportRelations：export relations 一次性导出全库关联 JSON 文件（Q160）。

// TestValueTraceMinConfCLI：Q161——value-trace --min-conf 剪枝低置信
// 候选边（0.7 < 0.8），且边级候选标注 JSON 输出。

// TestQueryFieldsOrigins：Q161——query fields 展示间接写多来源
// （summary_origins 落库 + dispatch join）。

// TestValueTraceIncludeContainerCLI：Q163——--include-container 显式
// 开启父容器路径扩展（默认精确匹配拦截容器读；flag 放行且不影响
// 候选剪枝语义）。

// TestTraceBackwardIndirectCLI：Q172——trace-backward --follow-indirect
// 经 summary_origins 链到达下游真实写者；默认（无 flag）为空。

// TestQueryRelationsFilters：--type/--max-hops/--max-results 过滤与默认
// 行为（P0④）——默认只输出 query+write（read 低置信隐藏），--type read
// 显式展开，--memory sql 走逐节点 SQL 路径结果一致。
