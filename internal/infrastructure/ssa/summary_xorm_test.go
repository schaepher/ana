package ssa

import (
	"testing"
)

// TestXORMSummarySpecsCoverage：XORM spec 覆盖清单——Engine/Session 已支持
// API 断言存在；未支持输出到日志（对照 xorm.io/xorm 官方 API 检查缺口）。
func TestXORMSummarySpecsCoverage(t *testing.T) {
	specs := xormSummarySpecs()
	have := map[string]bool{}
	for k := range specs {
		have[k] = true
	}
	// 已支持（断言在册）：Engine.Table/Exec + Session 链式七方法
	for _, m := range []struct{ iface, method string }{
		{"xorm.io/xorm.(Engine)", "Table"},
		{"xorm.io/xorm.(Engine)", "Exec"},
		{"xorm.io/xorm.(Session)", "Where"},
		{"xorm.io/xorm.(Session)", "Find"},
		{"xorm.io/xorm.(Session)", "Get"},
		{"xorm.io/xorm.(Session)", "Update"},
		{"xorm.io/xorm.(Session)", "Insert"},
		{"xorm.io/xorm.(Session)", "Delete"},
		{"xorm.io/xorm.(Session)", "Exec"},
	} {
		if !have["iface:"+m.iface+"."+m.method] {
			t.Errorf("XORM %s.%s 应有 spec", m.iface, m.method)
		}
	}
	// 未支持清单（人工对照）
	var missing []string
	for _, m := range []struct{ iface, method string }{
		{"xorm.io/xorm.(Session)", "Table"},
		{"xorm.io/xorm.(Session)", "ID"},
		{"xorm.io/xorm.(Session)", "In"},
		{"xorm.io/xorm.(Session)", "NotIn"},
		{"xorm.io/xorm.(Session)", "Exist"},
		{"xorm.io/xorm.(Session)", "Count"},
		{"xorm.io/xorm.(Session)", "Sum"},
		{"xorm.io/xorm.(Session)", "Iterate"},
		{"xorm.io/xorm.(Session)", "Limit"},
		{"xorm.io/xorm.(Session)", "Asc"},
		{"xorm.io/xorm.(Session)", "Desc"},
		{"xorm.io/xorm.(Session)", "OrderBy"},
		{"xorm.io/xorm.(Session)", "Join"},
		{"xorm.io/xorm.(Session)", "Sync"},
	} {
		if !have["iface:"+m.iface+"."+m.method] {
			missing = append(missing, m.iface+"."+m.method)
		}
	}
	t.Logf("XORM 未支持 API（候选补充）：%v", missing)
}

// TestXORMSummarySpecsShape：spec 形态——链式方法 ChainTable=true，
// filter 带 WhereArg，Get 带 IDArg 主键。
func TestXORMSummarySpecsShape(t *testing.T) {
	specs := xormSummarySpecs()
	where := specs["iface:xorm.io/xorm.(Session).Where"]
	if !where.ChainTable || where.Kind != "filter" || where.WhereArg != 0 {
		t.Errorf("Session.Where 形态错：%+v", where)
	}
	get := specs["iface:xorm.io/xorm.(Session).Get"]
	if get.Kind != "read" || get.IDArg != 1 || !get.ChainTable {
		t.Errorf("Session.Get 形态错：%+v", get)
	}
	tab := specs["iface:xorm.io/xorm.(Engine).Table"]
	if tab.Kind != "table" {
		t.Errorf("Engine.Table 应为 kind=table：%+v", tab)
	}
}
