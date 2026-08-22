package ssa

import (
	"fmt"
	"go/constant"

	"golang.org/x/tools/go/ssa"
)

// GORM 摘要独立实现（2026-08-17 拆分自 summary_applier.go，spec 构建期
// 合并进 ext.specs——运行期查 map O(1)，与拆分前完全一致）。
//
// 覆盖清单（对照 gorm.io/gorm v1/v2 `(DB).Method` API）：
//
// 已支持：
//   - Create / Save / Updates / Delete / Update —— ORM 写：对象实参 →
//     表.列 虚拟节点（表名=对象类型 snake_case、字段=列名 snake_case，
//     Type=gorm；与 Where 链式组合时 filter 先行）
//   - Where / Not / Or —— 条件字符串（"col = ?"）→ filter 节点（键关联
//     链起点）；ParamIndex=1（实参 0 是条件字符串）
//   - Find / First / Take / Last / Scan —— ORM 读：对象实参读出 → 表.列
//     read 节点（键关联链终点）；slice 实参经 entityTypeOf 解包
//   - Exec / Raw —— 原生 SQL（SQLStmt：SQL 字符串解析表列 + 值实参映射；
//     Exec 写、Raw 读，与 database/sql 同机制）
//   - Begin / (Tx).Commit / (Tx).Rollback —— 事务边界
//   - Table("x") 显式表名：chainTableNameValue 链式溯源（无 spec 无节点
//     产出——表名优先于实体类型推断）
//
// 未支持（非数据流/无实体形态，对照官方 API 逐项检查后不补 spec）：
//   - Table / Model：不补 spec（显式表名经 chainTableNameValue 溯源，
//     补 spec 只会多 external_summary 噪音节点）
//   - Select / Omit / Joins / Preload / Group / Having / Order / Limit /
//     Offset / Distinct / Scopes / Unscoped：条件/排序/分页/关联链（非
//     数据流；链上无 spec 不影响后续 Where/Find 处理）
//   - Pluck / Count / Sum：非对象读出形态（无实体字段可映射）
//   - Row / Rows：无执行语义（Raw/Query 已覆盖 SQL 解析）
//   - Transaction：闭包形态（fn(tx) 非 begin/commit 调用）
//   - AutoMigrate / Debug / Session / Clauses / Association / UpdateColumn：
//     非数据流或单列更新（Update 已覆盖字符串列名形态）
//
// chainTableNameValue GORM 显式表名溯源（Q177）：receiver 沿定义链回溯
// Table("name") / Model("name") 调用的字符串实参——显式表名与实体类型名
// 不一致时（Table("mch_account") + 结构体 Merchant）以显式为准。无显式
// 表名返回 ""（fallback 实体类型推断）。Where/Not/Or 的条件串不被误取
// （方法名限定 Table/Model）。
// Q239：Model(类型实参)（Model(&WalletLog{})）——经实体类型 TableName()
// 摘要解析（go2o 曾把 wal_wallet_log 误解析成 transaction_data）。
func (ext *fieldExtractor) chainTableNameValue(recv ssa.Value) string {
	c, ok := recv.(*ssa.Call)
	if !ok {
		return ""
	}
	for i, a := range c.Call.Args {
		fmt.Printf("  arg%d=%T type=%v\n", i, a, a.Type())
	}
	// 方法调用 CallCommon.Args[0] 是 receiver——实参从 Args[1] 起；
	// any 参数被 MakeInterface 包装（Type()=interface{}）须解包取实体类型
	// 链式中间值调用是 Value 风格（Method=nil、Value=MethodValue）——
	// 方法名判定须兼容两种（Q239：此前只查 Method 导致链式 Table/Model
	// 分支从未生效）
	if len(c.Call.Args) >= 2 {
		callee := ""
		if c.Call.Method != nil {
			callee = c.Call.Method.Name()
		} else if v, ok := c.Call.Value.(*ssa.Function); ok {
			callee = v.Name()
		}
		if callee == "Table" || callee == "Model" {
			arg1 := c.Call.Args[1]
					if mi, ok := arg1.(*ssa.MakeInterface); ok {
				arg1 = mi.X
						}
			if cst, isConst := arg1.(*ssa.Const); isConst && cst.Value != nil {
				if s := constant.StringVal(cst.Value); s != "" {
					return s
				}
			}
			// Q239：类型实参（&T{} / T{}）→ TableName()/Mapping/snakeCase
			if et := arg1.Type(); et != nil {
				if tn := ext.tableNameOf(et); tn != "" {
					return tn
				}
			}
		}
	}
	// 递归沿 receiver（Args[0]）回溯链式调用
	if len(c.Call.Args) > 0 {
		return ext.chainTableNameValue(c.Call.Args[0])
	}
	return ""
}

func gormSummarySpecs() map[string]summarySpec {
	specs := map[string]summarySpec{}
	// ORM 写（②：实参对象类型→表名、字段→列名 snake_case）
	for _, fn := range []string{"Create", "Save", "Updates", "Delete", "Update"} {
		specs["gorm.io/gorm.(DB)."+fn] = summarySpec{
			Func: "gorm.io/gorm.(DB)." + fn, ParamIndex: 1, ORMWrite: true,
		}
	}
	// 条件过滤（表关联键）：Where/Not/Or("col = ?", v) 字符串列名 →
	// filter 节点（applyORMWrite ⑦ 按方法名后缀判定）
	for _, fn := range []string{"Where", "Not", "Or"} {
		specs["gorm.io/gorm.(DB)."+fn] = summarySpec{
			Func: "gorm.io/gorm.(DB)." + fn, ParamIndex: 1, ORMWrite: true,
		}
	}
	// ORM 读（键关联链贯通）：Find/First/Take/Last/Scan 对象读出 → 表.列 read
	for _, fn := range []string{"Find", "First", "Take", "Last", "Scan"} {
		specs["gorm.io/gorm.(DB)."+fn] = summarySpec{
			Func: "gorm.io/gorm.(DB)." + fn, ParamIndex: 1, ORMRead: true,
		}
	}
	// 原生 SQL（Q97 同 database/sql 机制）：Exec 写 / Raw 读
	specs["gorm.io/gorm.(DB).Exec"] = summarySpec{
		Func: "gorm.io/gorm.(DB).Exec", ParamIndex: 1, SQLStmt: true, SQLWrite: true,
	}
	specs["gorm.io/gorm.(DB).Raw"] = summarySpec{
		Func: "gorm.io/gorm.(DB).Raw", ParamIndex: 1, SQLStmt: true,
	}
	// 事务边界
	specs["gorm.io/gorm.(DB).Begin"] = summarySpec{
		Func: "gorm.io/gorm.(DB).Begin", TxBoundary: "begin",
	}
	for _, fn := range []string{"Commit", "Rollback"} {
		boundary := "rollback"
		if fn == "Commit" {
			boundary = "commit"
		}
		specs["gorm.io/gorm.(Tx)."+fn] = summarySpec{
			Func: "gorm.io/gorm.(Tx)." + fn, TxBoundary: boundary,
		}
	}
	return specs
}
