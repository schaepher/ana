// 外部函数摘要系统（field_trace.md §7）：内置摘要 + 用户 field-summary.yaml。
// 构建器遇到带摘要的外部函数调用时：
//   - 生成虚拟 field_access 节点（is_external=1，func_id=调用者）
//   - external_summary 节点 + summary_io 边
//   - 写摘要：INDIRECT_WRITE 边（调用者 → 虚拟节点）+ data_flows_to（实参 → 虚拟节点）
//   - 读摘要：data_flows_to（虚拟节点 → 实参）
//   - 写摘要的字段进入调用者的间接写摘要表（indirectWrites）
package ssa

// fieldPattern 摘要字段模式："all" = 递归展开实参类型全部字段（Q16 保守策略）。

const (
	patternAll = "all"
)

// summarySpec 单个外部函数的摘要。

// userSummaryFile 对应 field-summary.yaml。

// loadSummaries 加载内置 + 用户摘要（用户覆盖同名内置），返回 函数全路径 → spec。
// YAML 解析失败/重复定义时跳过对应条目并输出警告（构建降级，不中止，Q59）。

// builtinSummaries 内置摘要（field_trace.md §7.3）。
// context.Context 为透明传递，无条目。

// summaryApplier 在单个函数内应用摘要（emitCall 调用）。

// applySummary 对带摘要的外部函数调用生成虚拟节点与边。
// 返回 false 表示无摘要（或无需处理）。

// applyArgSummary 对单个实参应用摘要（all 模式递归展开字段）。

// applyArgSummaryOne 对单个实参值应用摘要。

// expandAllFields 递归展开具名结构体的全部字段路径（深度 ≤ 4，
// 指针字段解一层，防递归类型爆炸）。

// namedStructOf 解指针/取具名结构体（非结构体返回 nil）。

// summaryKey 生成函数全路径摘要键：pkg.Func / pkg.(T).Method。

// lastPathSeg 取路径最后一段（instance_path 拼接用）。

// variadicElems 解开 ...any 变参的 Slice 包装取元素：
// alloc（数组）→ IndexAddr（元素地址）→ Store → 元素值（MakeInterface 等）。
// 非 Slice 原样返回；无法解出时返回原值（保守）。

// applySQLSummary 处理 SQL 语句调用（Q97）：SQL 字符串（第 0 实参）解析
// 表名与列名 → 虚拟节点（Name=表.列）；后续值实参按 ? 顺序映射列，
// 发 summary_io 边（字段值 → 虚拟节点）。
// applySQLSummary 处理 SQL 语句摘要：SQL 字符串在 Args[sqlArg]（database/sql
// 的 receiver 后 Args[1]；gof Connector 接口无 receiver 在 Args[0]，Q158），
// 值实参在 sqlArg+1 起（variadic 解包按 ?/$N 顺序映射）。

// applyScanOut 处理 Scan 写 out 实参（表关联链贯通）：
// row.Scan(&x) —— 接收者（row 值）→ 实参指向的局部变量节点
// （变量名 ID find#x，与 Load 归一节点一致）。数据流链：
// table_a.x.read → row → x → table_b.y.filter。

// applyTxBoundary 事务边界（Q97）：Begin/Commit/Rollback → 事务虚拟节点
// （Name=sql.tx.<boundary>），标注事务边界位置。

// parseSQLStmt 从 SQL 语句提取表名、列名与 WHERE 过滤列（Q97 启发式，
// 不做完整 SQL 解析）：
//
//	INSERT INTO t(a, b) VALUES(?, ?)  → t, [a b], nil
//	UPDATE t SET a=?, b=?             → t, [a b], nil
//	DELETE FROM t                     → t, [], nil
//	SELECT a, b FROM t                → t, [a b], nil（P0-2 读路径）
//	SELECT * FROM t                   → t, [], nil（表级）
//	... WHERE y = ?                   → ..., [], [y]（表关联：值实参按 ? 顺序映射）

// extractWhereCols 从 SQL 语句剩余部分提取 WHERE 子句的过滤列
// （`列 = ?` 序列，值实参按 ? 顺序映射——表关联分析的数据基础）。
// 支持 a.y = ? 表前缀（去前缀）；WHERE 缺失返回 nil。

// derefSlice 解切片（*[]Session → Session；GORM 读对象形态）。

// applyORMRead 处理 ORM 读调用（Find/First/Take/Last）：对象实参
// （&sessions / &s）→ 表名（Model 链式溯源或对象类型）+ 字段展开 →
// 表.列 read 虚拟节点 + 边（读出值 → 对象，与写方向相反）——
// 读出的字段（s.ID）作为后续查询实参时，键关联链贯通。

// applyORMWrite 处理 ORM 写调用（②⑦：GORM Create/Save/Updates/Delete/
// Update 等）：
//   - 对象实参（结构体字面量/变量）：类型 → 表名（snake_case）+ 字段 →
//     列名 → 虚拟节点 表.列 + summary_io 边（字段值 → 虚拟节点）。
//     字段值不可定位（变量/调用结果/空字面量——调用点无字段级 Store）
//     时不跳过该列：仍按类型展开生成 表.列 节点，连对象值兜底
//   - 字符串列名实参（Update("col", v) 单列更新）：表名溯源链式调用
//     receiver 的 Model(&X{}) 范围对象（⑦），列名取字符串实参

// emitORMColumn 生成单个 表.列 虚拟节点 + summary_io 边（值实参 → 节点）。

// chainScopeObject 溯源链式调用的范围对象（⑦）：Update/Updates 的 receiver
// 沿定义链回溯中间调用（Where/Model 等），找到实参为结构体对象的调用
// （如 Model(&Session{ID:...})）返回其类型。链上游无结构体实参返回 nil。

// fieldValueOf 按字段索引取对象值的字段读取（对象为 Alloc/寄存器时经
// FieldAddr 或 Field 指令；无法定位时返回 nil——字段值无 SSA 实体则
// 跳过该列）。

// derefType 解指针。

// commonInitialisms 常见缩写表（golang/lint 同款，GORM 默认命名用）——
// 先 Title 化再转小写，保证 SessionID → session_id、SourceURL → source_url。

// snakeCase 类型/字段名 → 表/列名，与 GORM 默认命名完全一致（移植
// gorm NamingStrategy.toDBName：常见缩写 Title 化 + 大小写扫描——连续
// 大写不拆，转小写前插线）。UserProfile → user_profile、SessionID →
// session_id、SourceURL → source_url、APIKey → apikey、
// SQLiteKnowledgeGraph → sq_lite_knowledge_graph（SQL 不在缩写表，
// 与 GORM 默认一致；radar 用 TableName() 定制表名时无法静态推导）。

// applyInterfaceSummary 处理接口摘要（Q156）：动态 invoke 无静态 callee 且
// 候选实现为空（外部框架实现，如 gof fw.Repository——底层是 GORM）时，
// 按 "iface:" + 接口全路径 + "." + 方法名 匹配 spec（内置 + field-summary.yaml）：
//   - write：对象实参字段展开 → 表.列 write 虚拟节点 + 边（值 → 节点）
//   - read：返回值对象展开 → read 虚拟节点 + 边（节点 → 调用点值）
//   - filter：where 字符串实参 → 列名（AND/OR 拆分 + 占位符剥离）→ filter 节点
//   - IDArg >= 0：主键实参 → 主键列 filter（键关联）
//
// 表名：实体类型参数 M 的 TableName() 常量优先，fallback snakeCase(类型名)。

// emitEntityFields 为实体类型展开 表.列 虚拟节点（write/read）+ summary_io 边。
// valID：write=对象值（边 值→节点）；read=调用点返回值（边 节点→值）。

// emitWhereFilter 为 where 列发射 filter 虚拟节点 + 值实参 → 节点边。
// whereArg 是 where 字符串实参下标；值实参在其后（variadic 解包）。

// emitWhereFilterTyped 同 emitWhereFilter，type 参数指定虚拟节点
// type_string（Q175：xorm；空默认 gorm）。

// entityTypeOf 取接口摘要的实体类型：泛型接口实例化（Repository[M]）的
// 类型实参优先；fallback 按 kind 从对象实参/返回值类型取。

// tableNameOf 实体类型表名：TableName() 方法（SSA Return 常量）优先，
// fallback snakeCase(类型名)（GORM 默认命名）。

// pkColumnOf 主键列名：字段 pk:"yes" tag（gorm column 优先）→ 该字段列名；
// 无标记时 fallback "id"。

// gormColumnOf 提取 gorm:"column:x" 的列名（无则 snake_case 字段名）。

// whereColsOf 从 where 条件串提取列名：AND/OR 拆分 + 占位符剥离
// （IN (?) 先处理；其余形态截到最后一个 ? 再 TrimRight 运算符——
// 兼容 " = ?" / "=?" / " <?" / " LIKE ?" 等有无空格写法，以及多行
// 条件串（AND/OR 前后为换行/制表符——pay_order 实测整串未被拆分）。
