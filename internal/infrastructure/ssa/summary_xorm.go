package ssa

import "golang.org/x/tools/go/ssa"

// XORM 摘要独立实现（Q175 链式形态，2026-08-17 拆分自
// summary_applier.go——spec 构建期合并，运行期查 map O(1) 无差异）。
//
// 覆盖清单（对照 xorm.io/xorm v1/v2 Engine / Session API）：
//
// 已支持（链式表名模式：Engine.Table 记链 → Session 方法 ChainTable 查链）：
//   - Engine.Table —— 链式表名起点（kind=table：记录
//     Table("name") 返回值的表名 + 发射整表节点）
//   - Session.Where —— WHERE 字符串（WhereArg=0）→ filter 节点
//   - Session.Find —— 读：slice 实参读出 → 表.列 read 节点
//   - Session.Get —— 读：对象实参读出 → read 节点；IDArg=1 主键实参 →
//     主键列 filter
//   - Session.Update / Insert / Delete —— 写：对象实参 → 表.列 write 节点
//   - Engine.Exec / Session.Exec —— 原生 SQL 写（SQLWrite）
//
// 未支持（候选补充，对照官方 API 逐项检查）：
//   - Session.Table：Session 上显式表名（当前只认 Engine.Table 起点）
//   - Session.ID：主键条件（Get 的 IDArg 已覆盖"按主键读"形态）
//   - In / NotIn / Exist / Count / Sum / Iterate：其余读与条件形态
//   - Limit / Asc / Desc / OrderBy / Join：排序/分页/关联链（非数据流）
//   - Sync / CreateTables / DropTables：DDL（非数据流）
func xormSummarySpecs() map[string]summarySpec {
	specs := map[string]summarySpec{}
	for _, spec := range []summarySpec{
		{Interface: "xorm.io/xorm.(Engine)", Method: "Table", Kind: "table", Type: "xorm"},
		{Interface: "xorm.io/xorm.(Session)", Method: "Where", Kind: "filter", WhereArg: 0, ChainTable: true, Type: "xorm"},
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
	return specs
}

// chainTableName XORM 链式表名（Q175）：Session 方法调用值 cc 在链上
// 由 Engine.Table(name) 记录过表名时返回该表名（filter/write/read 的
// ChainTable 分支查链）。XORM 特有逻辑，随实现归入本文件。
func (ext *fieldExtractor) chainTableName(cc *ssa.CallCommon) string {
	return ext.chainTables[cc.Value]
}

// recordChainTable XORM 链式表名记录（Q175）：Table("name") 调用的返回
// 值 callVal → 表名，供链上后续 Session 方法查询。
func (ext *fieldExtractor) recordChainTable(callVal ssa.Value, name string) {
	ext.chainTables[callVal] = name
}
