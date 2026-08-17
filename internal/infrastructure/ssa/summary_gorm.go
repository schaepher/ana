package ssa

// GORM 摘要独立实现（2026-08-17 拆分自 summary_applier.go，spec 构建期
// 合并进 ext.specs——运行期查 map O(1)，与拆分前完全一致）。
//
// 覆盖清单（对照 gorm.io/gorm v1/v2 `(DB).Method` API）：
//
// 已支持：
//   - Create / Save / Updates / Delete / Update —— ORM 写：对象实参 →
//     表.列 虚拟节点（表名=对象类型 snake_case、字段=列名 snake_case，
//     Type=gorm；与 Where 链式组合时 filter 先行）
//   - Where —— WHERE 字符串列名（"col = ?"）→ filter 节点（键关联链
//     起点）；ParamIndex=1（实参 0 是条件字符串）
//   - Find / First / Take / Last —— ORM 读：对象实参读出 → 表.列 read
//     节点（键关联链终点）；slice 实参经 entityTypeOf 解包
//
// 未支持（候选补充，对照官方 API 逐项检查）：
//   - Table / Model：显式表名（当前表名一律从对象类型推断；显式 Table
//     "physical_table" 与结构体名不一致时表名会错）
//   - Not / Or / Select / Omit / Joins / Preload：条件链（Where 已覆盖
//     filter 提取主形态；Not("col = ?") 同为 filter 候选）
//   - Scan / Pluck / Count / Sum：非对象读出形态（无实体可映射）
//   - Exec / Raw / Row / Rows：原生 SQL（database/sql 摘要可覆盖，非
//     GORM 特有）
//   - Transaction / Begin / Commit / Rollback：事务边界（database/sql
//     摘要已覆盖 begin/commit/rollback）
//   - AutoMigrate / Debug / Session / Clauses / Association：非数据流
func gormSummarySpecs() map[string]summarySpec {
	specs := map[string]summarySpec{}
	// ORM 写（②：实参对象类型→表名、字段→列名 snake_case）
	for _, fn := range []string{"Create", "Save", "Updates", "Delete", "Update"} {
		specs["gorm.io/gorm.(DB)."+fn] = summarySpec{
			Func: "gorm.io/gorm.(DB)." + fn, ParamIndex: 1, ORMWrite: true,
		}
	}
	// Where 过滤（表关联键）：Where("col = ?", v) 字符串列名 → filter 节点
	specs["gorm.io/gorm.(DB).Where"] = summarySpec{
		Func: "gorm.io/gorm.(DB).Where", ParamIndex: 1, ORMWrite: true,
	}
	// ORM 读（键关联链贯通）：Find/First/Take/Last 对象读出 → 表.列 read
	for _, fn := range []string{"Find", "First", "Take", "Last"} {
		specs["gorm.io/gorm.(DB)."+fn] = summarySpec{
			Func: "gorm.io/gorm.(DB)." + fn, ParamIndex: 1, ORMRead: true,
		}
	}
	return specs
}
