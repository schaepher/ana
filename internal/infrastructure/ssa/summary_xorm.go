package ssa

import "golang.org/x/tools/go/ssa"

// XORM 摘要独立实现（Q175 链式形态，2026-08-17 拆分自
// summary_applier.go——spec 构建期合并，运行期查 map O(1) 无差异）。
//
// 覆盖清单（对照 xorm.io/xorm v1/v2 Engine / Session API）：
//
// 已支持（链式表名模式：Engine/Session.Table 记链 → Session 方法
// ChainTable 查链）：
//   - Engine.Table / Session.Table —— 链式表名起点（kind=table：记录
//     Table("name") 返回值的表名 + 发射整表节点）
//   - Session.Where / And / Or / In / NotIn —— 条件字符串（WhereArg=0）→ filter 节点
//   - Session.Find / Iterate —— 读：对象/slice 实参读出 → 表.列 read 节点
//   - Session.Get —— 读：对象实参读出 → read 节点；IDArg=1 主键实参 →
//     主键列 filter
//   - Session.Update / Insert / Delete —— 写：对象实参 → 表.列 write 节点
//   - Engine.Exec / Session.Exec —— 原生 SQL 写（SQLWrite）
//
// 未支持（非数据流/无实体形态，对照官方 API 逐项检查后不补 spec）：
//   - Session.ID：主键条件——主键列名无法静态确定（Get 的 IDArg 已覆盖
//     "按主键读"形态）
//   - Exist / Count / Sum：表级聚合/存在判断——无实体字段可映射
//   - Limit / Asc / Desc / OrderBy / Join：排序/分页/关联链（非数据流；
//     链上无 spec 也不影响链式表名查询）
//   - Sync / CreateTables / DropTables：DDL（非数据流）
func xormSummarySpecs() map[string]summarySpec {
	specs := map[string]summarySpec{}
	for _, spec := range []summarySpec{
		{Interface: "xorm.io/xorm.(Engine)", Method: "Table", Kind: "table", Type: "xorm"},
		{Interface: "xorm.io/xorm.(Session)", Method: "Table", Kind: "table", Type: "xorm"},
		{Interface: "xorm.io/xorm.(Session)", Method: "Where", Kind: "filter", WhereArg: 0, ChainTable: true, Type: "xorm"},
		{Interface: "xorm.io/xorm.(Session)", Method: "And", Kind: "filter", WhereArg: 0, ChainTable: true, Type: "xorm"},
		{Interface: "xorm.io/xorm.(Session)", Method: "Or", Kind: "filter", WhereArg: 0, ChainTable: true, Type: "xorm"},
		{Interface: "xorm.io/xorm.(Session)", Method: "In", Kind: "filter", WhereArg: 0, ChainTable: true, Type: "xorm"},
		{Interface: "xorm.io/xorm.(Session)", Method: "NotIn", Kind: "filter", WhereArg: 0, ChainTable: true, Type: "xorm"},
		{Interface: "xorm.io/xorm.(Session)", Method: "Iterate", Kind: "read", ObjArg: 0, ChainTable: true, Type: "xorm"},
		{Interface: "xorm.io/xorm.(Session)", Method: "Find", Kind: "read", ObjArg: 0, ChainTable: true, Type: "xorm"},
		{Interface: "xorm.io/xorm.(Session)", Method: "Get", Kind: "read", ObjArg: 0, IDArg: 1, ChainTable: true, Type: "xorm"},
		{Interface: "xorm.io/xorm.(Session)", Method: "Update", Kind: "write", ObjArg: 0, ChainTable: true, Type: "xorm"},
		{Interface: "xorm.io/xorm.(Session)", Method: "Insert", Kind: "write", ObjArg: 0, ChainTable: true, Type: "xorm"},
		{Interface: "xorm.io/xorm.(Session)", Method: "Delete", Kind: "write", ObjArg: 0, ChainTable: true, Type: "xorm"},
		{Interface: "xorm.io/xorm.(Session)", Method: "Exec", Kind: "sql", WhereArg: 0, SQLWrite: true, Type: "xorm"},
		{Interface: "xorm.io/xorm.(Engine)", Method: "Exec", Kind: "sql", WhereArg: 0, SQLWrite: true, Type: "xorm"},
	} {
		specs["iface:"+spec.Interface+"."+spec.Method] = spec
	}
	// 静态键（Q177 修复）：真实仓库用具体类型 *xorm.Session（SSA 解析
	// 静态 callee → applySummary 普通键）。静态方法调用 cc.Args 含
	// receiver（Args[0]）→ WhereArg/ObjArg 下标 +1；链式表名机制与
	// iface 共用（chainTables）。
	staticSpecs := []summarySpec{
		{Func: "xorm.io/xorm.(Engine).Table", Kind: "table", Type: "xorm"},
		{Func: "xorm.io/xorm.(Session).Table", Kind: "table", Type: "xorm"},
		{Func: "xorm.io/xorm.(Session).Where", Kind: "filter", WhereArg: 1, ChainTable: true, Type: "xorm"},
		{Func: "xorm.io/xorm.(Session).And", Kind: "filter", WhereArg: 1, ChainTable: true, Type: "xorm"},
		{Func: "xorm.io/xorm.(Session).Or", Kind: "filter", WhereArg: 1, ChainTable: true, Type: "xorm"},
		{Func: "xorm.io/xorm.(Session).In", Kind: "filter", WhereArg: 1, ChainTable: true, Type: "xorm"},
		{Func: "xorm.io/xorm.(Session).NotIn", Kind: "filter", WhereArg: 1, ChainTable: true, Type: "xorm"},
		{Func: "xorm.io/xorm.(Session).Find", Kind: "read", ObjArg: 1, ChainTable: true, Type: "xorm"},
		{Func: "xorm.io/xorm.(Session).Get", Kind: "read", ObjArg: 1, ChainTable: true, Type: "xorm"},
		{Func: "xorm.io/xorm.(Session).Iterate", Kind: "read", ObjArg: 1, ChainTable: true, Type: "xorm"},
		{Func: "xorm.io/xorm.(Session).Update", Kind: "write", ObjArg: 1, ChainTable: true, Type: "xorm"},
		{Func: "xorm.io/xorm.(Session).Insert", Kind: "write", ObjArg: 1, ChainTable: true, Type: "xorm"},
		{Func: "xorm.io/xorm.(Session).Delete", Kind: "write", ObjArg: 1, ChainTable: true, Type: "xorm"},
		{Func: "xorm.io/xorm.(Session).Exec", Kind: "sql", WhereArg: 1, SQLWrite: true, Type: "xorm"},
		{Func: "xorm.io/xorm.(Engine).Exec", Kind: "sql", WhereArg: 1, SQLWrite: true, Type: "xorm"},
	}
	for _, spec := range staticSpecs {
		specs[spec.Func] = spec
	}
	return specs
}

// chainTableName XORM 链式表名（Q175）：Session 方法调用值 cc 在链上
// 由 Engine.Table(name) 记录过表名时返回该表名（filter/write/read 的
// ChainTable 分支查链）。XORM 特有逻辑，随实现归入本文件。
func (ext *fieldExtractor) chainTableName(cc *ssa.CallCommon) string {
	// Q177 修复：静态调用 cc.Value 是方法函数（receiver 在 cc.Args[0]）；
	// invoke 的 cc.Value 才是接口接收者。统一按接收者查链。
	if cc.IsInvoke() {
		return ext.chainTables[cc.Value]
	}
	if len(cc.Args) > 0 {
		return ext.chainTables[cc.Args[0]]
	}
	return ""
}

// recordChainTable XORM 链式表名记录（Q175）：Table("name") 调用的返回
// 值 callVal → 表名，供链上后续 Session 方法查询。
func (ext *fieldExtractor) recordChainTable(callVal ssa.Value, name string) {
	ext.chainTables[callVal] = name
}
