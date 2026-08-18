package cli

// outputOpts 查询输出选项（--json / --compact，Q96）。

// encodeJSON 输出结构化 JSON（stdout 唯一内容）。

// cmdQuery 实现 `codeintel query ...`。

// queryFlags 是 query 子命令的手动解析结果。
type queryFlags struct {
	repoPath         string
	depth            int
	maxDepth         int
	funcPath         string
	positional       []string
	json             bool
	compact          bool
	format           string   // summary 的 mermaid 输出（Q100）
	since            string   // unused 的 --since <ref>（git diff 区间）
	failOn           string   // unused 的 --fail-on unused|isolated（CI 退出码）
	all              bool     // relations --all：全库关联聚合（Q160）
	minConf          float64  // value-trace --min-conf：候选边置信度剪枝（Q161）
	minConfSet       bool     // --min-conf 显式设置（Q163 默认 1.0）
	includeContainer bool     // value-trace --include-container：父容器扩展（Q163）
	followIndirect   bool     // trace-backward --follow-indirect：跨函数间接写链（Q172）
	relTypes         []string // relations --type：关联类型过滤（query/write/read，可多次/逗号分隔；空=默认 query+write，P0④）
	maxHops          int      // relations --max-hops：跳数上限（0=不限）
	maxResults       int      // relations --max-results：条数上限（0=不限）
	includeLongQuery bool     // relations --include-long-query：query 不限制跳数（等价 --query-max-hops 0）
	queryMaxHops     int      // relations --query-max-hops：键关联跳数上限（0=不限制，默认 4）
	writeMaxHops     int      // relations --write-max-hops：同源写跳数上限（0=不限制，默认 4）
	readMaxHops      int      // relations --read-max-hops：间接读跳数上限（0=不限制，默认 4）
	memory           string   // relations --memory：full/sql（默认 auto 按规模，P0④）
}

// parseQueryFlags 手动解析 query 子命令的参数，支持 flags 与位置参数任意顺序。

// queryFields 输出函数的字段读写摘要（S1，field_trace.md §6.2），
// 按 direct_read / direct_write / indirect_write 分组。

// queryTraceDir 输出字段追溯路径（S2/S3，field_trace.md §6.3/6.4）。
// 树形渲染：缩进 + 边类型 + 节点名 + (行号)（Q28）；--compact 去缩进。

// lastEdgeKind 取路径上最后一段边类型（进入当前节点的边）。

// sinceFlag 函数/方法节点的 --since 标注（§17.2）：[new]/[mod]/空。

// sinceMarks 对 ID 列表批量计算 --since 标注（callers/callees 邻居用）。

// querySymbol 输出符号摘要（对齐 TD.md 7.1 explore_symbol 摘要层）。

// queryGraph 输出 callers/callees/impact 查询结果。

// factEndpointIDs 提取边的端点 ID 列表（--since 标注用）。

// factIDs 提取边的端点 ID 列表（endpoint=source/target，JSON 输出用）。

// nodeBriefs 提取节点摘要（JSON 输出用）。

// printFacts 打印边列表；endpoint 为 "source" 时显示边左端（调用者场景），
// 否则显示右端（被调用者场景）。

// shortID 压缩 canonical ID 显示：保留 pkg 末段与符号名。

// printNodes 打印节点列表。

// queryValueTrace 输出数据值在整条链路上的处理过程，按函数上下文分组
// （field_trace.md §14.2 数据值全链追踪）。

// shortFuncName 从函数 canonical ID 提取短名（symbol:go:<pkg>:<name> → <name>）。

// querySummary 跨层摘要（Q100）：字段生命周期主链
// （入口 → 计算 → 写入 → 消费），每步带 file:line。

// dispatchJSON 候选派发标注（Q157 P1：value-trace --json 输出）。
// edgeCandidateJSON 边级候选标注（Q161：动态 argument/returns 边元数据）。
type edgeCandidateJSON struct {
	Iface      string  `json:"interface"`
	Origin     string  `json:"origin"`
	Confidence float64 `json:"confidence"`
}

type dispatchJSON struct {
	Origin     string  `json:"origin"`
	Confidence float64 `json:"confidence"`
}
