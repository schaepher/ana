# Q241 设计：表间通路查询（table-path，2026-08-23）

**需求**：添加 action——获取表 A 到表 B 之间的通路。即从一张表可以
找到另一张表的数据，中间可能跨过 mapping 表或其他类型的关联表。

## 1. 背景

现有 `query path` 是**节点级**最短路径（--kind data|calls）；relations
图（表间关联：fk/query/write/read + 列对）已可单表展开，但**缺少表间
多跳通路**查询——用户想回答「从 mch_merchant 怎么到 order_wholesale_order，
经过哪些表」这类问题（mapping 表如 pt_siteconf、mm_relation 等跨表）。

## 2. 设计

### 2.1 命令面

```
codeintel query table-path <表A> <表B> [--max-hops N] [--json] [--repo <path>]
```

- 表名：精确匹配（mch_merchant）；多匹配（大小写/后缀）报候选
- 输出：通路序列（表A.col → [类型] → 表B.col → ... 表B.col），
  每步带关系类型（fk/query/write/read）与列对
- --json：`{path: [{from, from_col, type, to, to_col}...], hops, reachable}`

### 2.2 通路算法

- 图：relations 全量（mergeBidirectional 双向边——表间无向）
- **BFS 最短跳数**（mapping 表自然在链中）；`--max-hops` 上限（默认
  6？）超出报不可达
- 同跳数多路径：按**边类型优先级**选（fk > query > write > read——
  真实业务链优先，Q219 同源）；其余并列输出？——首版取优先级最优
  一条（--json 可全列候选？）

### 2.3 起点/终点语义

- 只要求「表 A 可达表 B」（表级），不限定列
- 通路每一步展示**具体列对**（如 mch_merchant.id → [fk] →
  order_wholesale_order.vendor_id）——用户可据此追溯代码

## 3. 影响面

- action 层新增（窄接口 + 复用 relations 图构建）
- CLI query 分派 + usage
- 无存储层改动（复用 relation_candidates / 查询期 BFS）

## 4. 测试矩阵

- 直接关联（A→B 一步 fk）
- mapping 表跨跳（A→M→B，M 是 mapping 表）
- 多跳链（A→M1→M2→B）
- 不可达（--max-hops 内无通路 → 报不可达）
- 同跳数多路径（类型优先级选择 fk 链）
- 表名多匹配报候选
- --json 结构

## 5. 决策确认（2026-08-23 访谈）

1. **命令名**：独立 `query table-path <表A> <表B>`（不扩展 query path——
   path 语义是节点/符号，混入表名易歧义；与 query relations 并列表级家族）
2. **同跳数多路径**：文本输出类型优先级最优一条（fk>query>write>read），
   `--json` 全列候选（程序消费）
3. **--max-hops 默认 6**：mapping 链常见 2-3 跳，6 留足余量；显式查询
   意图明确，放宽于 relations 降噪默认 4
