package ssa

import (
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
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
		{"xorm.io/xorm.(Session)", "Table"},
		{"xorm.io/xorm.(Session)", "Where"},
		{"xorm.io/xorm.(Session)", "In"},
		{"xorm.io/xorm.(Session)", "NotIn"},
		{"xorm.io/xorm.(Session)", "Iterate"},
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
		{"xorm.io/xorm.(Session)", "ID"},
		{"xorm.io/xorm.(Session)", "Exist"},
		{"xorm.io/xorm.(Session)", "Count"},
		{"xorm.io/xorm.(Session)", "Sum"},
		{"xorm.io/xorm.(Session)", "Limit"},
		{"xorm.io/xorm.(Session)", "Asc"},
		{"xorm.io/xorm.(Session)", "Desc"},
		{"xorm.io/xorm.(Session)", "OrderBy"},
		{"xorm.io/xorm.(Session)", "Join"},
		{"xorm.io/xorm.(Session)", "Sync"},
		{"xorm.io/xorm.(Session)", "CreateTables"},
		{"xorm.io/xorm.(Session)", "DropTables"},
		{"xorm.io/xorm.(Engine)", "Sync"},
		{"xorm.io/xorm.(Engine)", "CreateTables"},
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

// TestXORMChainExtended：链式扩展方法（Q177 补全）——Session.Table 链式
// 表名、In/NotIn filter、Iterate 读（模拟接口 + yaml，Q175 模式）。
func TestXORMChainExtended(t *testing.T) {
	src := `package m

type Engine interface {
	Table(tableNameOrBean interface{}) Session
}

type Session interface {
	Table(tableNameOrBean interface{}) Session
	Where(query interface{}, args ...interface{}) Session
	In(query interface{}, args ...interface{}) Session
	NotIn(query interface{}, args ...interface{}) Session
	Iterate(out any) error
}

type Settlement struct {
	OrderID int64
	Amount  int64
}

func query(engine Engine, list *[]Settlement, id int64) {
	engine.Table("settlement").Where("order_id = ?", id).In("amount", 100).NotIn("status", 1).Iterate(list)
}
`
	yaml := `summaries:
  - iface: "example.com/mtest.Engine"
    method: "Table"
    kind: "table"
    type: "xorm"
  - iface: "example.com/mtest.Session"
    method: "Table"
    kind: "table"
    type: "xorm"
  - iface: "example.com/mtest.Session"
    method: "Where"
    kind: "filter"
    where_arg: 0
    chain_table: true
    type: "xorm"
  - iface: "example.com/mtest.Session"
    method: "In"
    kind: "filter"
    where_arg: 0
    chain_table: true
    type: "xorm"
  - iface: "example.com/mtest.Session"
    method: "NotIn"
    kind: "filter"
    where_arg: 0
    chain_table: true
    type: "xorm"
  - iface: "example.com/mtest.Session"
    method: "Iterate"
    kind: "read"
    obj_arg: 0
    chain_table: true
    type: "xorm"
`
	nodes, _, _ := indexFixtureFull(t, map[string]string{
		"go.mod":             moduleGoMod,
		"main.go":            src,
		"field-summary.yaml": yaml,
	})
	// 独立 bool 判定（同名 filter/read 节点并存——链式多环各产各的）
	var whereFilter, inFilter, notInFilter, iterRead bool
	for _, n := range nodes {
		if n.Kind != domain.KindFieldAccess || n.Property("is_external") != "true" {
			continue
		}
		switch {
		case n.Name == "settlement.order_id" && n.Property("access_kind") == "filter":
			whereFilter = true
		case n.Name == "settlement.amount" && n.Property("access_kind") == "filter":
			inFilter = true
		case n.Name == "settlement.status" && n.Property("access_kind") == "filter":
			notInFilter = true
		case (n.Name == "settlement.order_id" || n.Name == "settlement.amount") &&
			n.Property("access_kind") == "read":
			iterRead = true
		}
	}
	if !whereFilter {
		t.Error("Where 应产 filter settlement.order_id（链式表名 settlement）")
	}
	if !inFilter {
		t.Error("In 应产 filter settlement.amount")
	}
	if !notInFilter {
		t.Error("NotIn 应产 filter settlement.status（Q177 多级链传播）")
	}
	if !iterRead {
		t.Error("Iterate 应产字段 read 节点")
	}
}
