package sqlite

// TestGetTableRelationsBridge：循环读出桥（Q152）——BFS 到 ssa_value 节点
// （类型 []example.com/m.Session → Session）时，桥接同函数、同类型的字段
// 读取节点（非外部 read field_access，full_path 含类型名，下游 2 跳可达
// filter 外部节点）：对象读出的值经字段读取后进入 WHERE。

// TestGetTableRelationsXORM：Q175 修复——xorm 外部节点（type_string='xorm'）
// 也要参与关联终点判定（旧实现 byNode 只认 sql/gorm，xorm 表关联全丢）。

// TestGetTableRelationsBridgeDirectional：桥 2 跳检查是定向出边
// （旧 SQL EXISTS：e1.source_id = n2.id）——只有"反向边"连到 filter 的
// read 节点不应被桥（双向误桥会引入噪音关联）。

// TestRelationMemoryModes：内存 BFS（full）与逐节点 SQL（sql）两路径
// 结果一致（P0④ --memory 参数：大仓库强制 SQL 防爆内存）。

// TestBuildMetaCounts：节点/边数随构建元数据缓存（--memory auto 判断用，
// 不每次重新 COUNT）。

// TestGetTableRelationsTypeRank：同列多节点（read + write）时 Type 取
// rank 最高（query > write > read）——与遍历顺序无关（旧实现只升级 query，
// write 不覆盖 read，结果依赖 map 遍历顺序不确定）。

// TestRelationCandidatesCache：relation_candidates 缓存语义（P0③）——
// ① 有 build_id 时单表结果写缓存，图状态变化后仍返回缓存；
// ② build_id 变化 → 缓存失效 → 现场重算；
// ③ 无 build_metadata 时跳过缓存（不写行）。

// TestGetAllTableRelationsRebuildCache：--all 全量重建缓存——先算完
// 单表（缓存只有 table_a），--all 后 relation_candidates 覆盖为全部表。
