# Q239 设计：SQL 关系识别增强——JOIN/子查询/动态拼接还原（2026-08-22）

Q238 验证（go2o 孤立表检查）发现：152 表 20 张孤立，其中 4 类是分析
短板造成（JOIN 缺失 / 子查询括号 / gorm 表名错误 / 动态 where 盲区）。
本设计分步增强 SQL 关系识别，最终目标：**显式 JOIN、子查询、动态拼接
的 SQL 都能还原出表间键关系**。访谈确认后实施，实施完归档并入
field_trace.md。

## 1. 背景与问题（go2o 实测证据）

现状 SQL 识别是正则启发式（summary_sqlparse.go `parseSQLStmt` /
`extractWhereCols`），无真实语法解析、无动态拼接还原。孤立表根因：

| 问题 | 证据 | 影响 |
|---|---|---|
| JOIN 不解析 | `FROM sys_sub_station s INNER JOIN sys_district a ON a.code = s.city_code`——只取 FROM 后首表，JOIN 表与 ON 键对丢弃 | sys_sub_station / sale_sub_item（`JOIN sale_normal_order ord ON ord.id=it.order_id`）孤立 |
| 子查询括号并进表名 | `(SELECT COUNT(1) FROM mm_member)` → 表名 `mm_member)` | 假表 mm_member) |
| gorm Model(类型实参) 表名错 | `Model(wallet.WalletLog{})`（TableName=wal_wallet_log）→ 解析成 transaction_data | transaction_data 假表 + wal_wallet_log 关系断裂 |
| 动态 where 盲区 | `fmt.Sprintf("SELECT * FROM rbac_role WHERE %s", where)`——%s 处无 `列=?` | rbac 系 5 表孤立（QueryPagingPermRole 等） |

## 2. 目标与范围

**Step 1（正则层增强，见效快）**
- JOIN 解析：FROM/JOIN 段结构化提取——`JOIN <表> [alias] ON <col> = <col>` → 键对
- 子查询括号剥离：表名 token 去尾 `)` / 边界修正
- gorm Model(类型实参)：补 TableName() 调用摘要（类型 → TableName 方法 → 表名）

**Step 2（动态拼接还原，SSA 值流）**
- `fmt.Sprintf("...WHERE %s", where)`：`%s` 实参值流追溯（常量字符串 /
  另一层 Sprintf / 跨函数参数）→ 还原 SQL 模板 → 走统一解析
- 还原不完整（%s 实参追溯不到）时保持现状（不误报）

**Step 3（评估项，暂不实施）**
- 真实 SQL parser（vitess/sqlparser）替换正则层——工程量大，Step 1/2
  覆盖主要形态后评估增量收益

## 3. 设计细节

### 3.1 JOIN 解析（Step 1）

parseSQLStmt 的 rest 段（FROM 之后）结构化处理：

```
FROM t1 [alias] {, t2 [alias]} | {[INNER|LEFT|RIGHT|CROSS] JOIN tN [alias] ON <cond>}
```

- 提取：JOIN 表名列表 + ON 条件键对（`a.code = s.city_code` → 列对
  sys_district.code ↔ sys_sub_station.city_code，别名映射到真实表）
- 产出：JOIN 键对并入 relations 的 query 类型（键关联，与 WHERE 值流
  同通道）——ON 条件是比 WHERE 更强的键信号，优先级不降级
- 多表 JOIN / 逗号连接 / 子查询 JOIN 都覆盖

### 3.2 子查询括号剥离（Step 1）

表名 token 提取规则：`[A-Za-z_][A-Za-z0-9_]*` 后若紧邻 `)` 且该 `)`
是子查询闭合（`(SELECT ... FROM t)` 形态）→ 剥离。WHERE 摘录的列名
同样适用（`mm_member).status` 现象）。

### 3.3 gorm Model(类型实参) 表名（Step 1）

chainTableNameValue（Q177 显式字符串表名溯源）扩展：Model(T{}) 类型
实参 → 查 T 的 TableName() 方法（summary 缓存已有 tableNameOf 类型
解析，Q205）→ 表名。兜底失败才回退现有推断。

### 3.4 动态拼接还原（Step 2）

SSA 值流：Sprintf 调用的格式串实参是常量（模板）——`%s` 占位符按
序对应后续实参（字符串值流）：

- 实参是字符串常量 → 直接替换
- 实参是另一 Sprintf 结果 → 递归还原
- 实参是函数参数（如 QueryPagingPermRole 的 where）→ 跨函数调用点
  追溯（实参来源处替换），深度上限 3 层，追溯不到放弃该占位符
- 还原后 SQL 仍含 %s → 不强行解析（保持现状）
- 占位符替换为 `?` 后走现有 whereColRe（`列 = ?`）摘录

还原产物与静态 SQL 同路径：表名/列/where 摘录 → 虚拟节点 → relations。

## 4. 数据流与影响面

- summary_sqlparse.go：parseSQLStmt / extractWhereCols（JOIN/括号）
- summary_gorm.go：chainTableNameValue / Model 类型实参分支
- 新增 summary_dynsql.go：Sprintf 模板还原（SSA 值流遍历）
- relations 键关联（Q146 where 值流 / Q234 filter 增强）：JOIN 键对并入
- 影响查询：query relations / query table / ER 图（fk/query 线）

## 5. 测试矩阵（形态矩阵）

- JOIN：INNER/LEFT/RIGHT/CROSS、多 JOIN 链、逗号连接、别名映射、
  ON 键对方向（a.code=s.city_code 双向）、子查询 JOIN
- 子查询：`(SELECT ... FROM t)` 表名、where 列名、多层嵌套
- gorm：Model(类型) / Table("x") / Model("x") 字符串 / 无 Model 兜底
- 动态拼接：Sprintf+常量实参 / Sprintf 链 / 函数参数跨调用点 / 深度
  超限放弃 / 还原不完整不误报
- 回归：go2o 孤立表复检（20 张 → 预期：mm_member) 消失、
  sys_sub_station/sale_sub_item 出现 query 关系、rbac 系恢复、
  transaction_data 消失（wal_wallet_log 恢复）、真孤立仅剩配置表）

## 6. 待确认

1. JOIN 键对的关系类型：归 query（键关联）还是独立新类型？
   （推荐：query——与 WHERE 值流同通道，精度同源）
2. 动态还原深度上限 3 层是否合适？（推荐：是——更深价值递减且误配风险升）
3. Step 3（真 SQL parser）是否立项为长期候选？（推荐：是，待 Step 1/2
   效果评估后再启动）
