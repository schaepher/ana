package ssa

// TestORMWriteVariableArg：② ORM 映射——实参为变量（非结构体字面量，
// 调用点无字段级 Store，如 Create(row) / Delete(&X{})）时，仍须生成
// "表.列" 虚拟节点（fieldValueOf 定位不到字段值时按类型展开）。
// 通过 field-summary.yaml 的 orm_write 条目定义本地 ORM 写调用。

// TestORMWriteEmptyLiteral：空字面量实参（Delete(&X{})）同样按类型展开。

// TestORMChainUpdateColumnName：⑦ 链式 ORM——Model(&X{主键}).Where(...)
// .Update("col", v) 字符串列名形态：表名溯源链式 Model 范围对象，
// 列名取字符串实参（此前仅结构体实参可映射，该形态零节点）。
// Model 本身非写操作不配 orm_write——范围对象经 receiver 定义链解析。

// TestORMWhereFilter：GORM Where("session_id = ?", v) 字符串列名形态——
// 列名剥离 " = ?" 后缀产 filter 虚拟节点（表关联键：值 → 过滤列）。
// 用本地模拟 DB 类型（链式 Model/Where/Count，同 gorm 形态）。

// TestORMReadFind：GORM 读路径——Find(&sessions) 对象读出产 read
// 虚拟节点（表.列）+ 边（读出值 → 对象）；读出的 s.ID 作为 Where 实参
// 时，键关联链贯通（session.id.read → ... → chat_message.session_id.filter）。
