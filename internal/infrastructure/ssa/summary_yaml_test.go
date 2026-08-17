package ssa

import (
	"path/filepath"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

func TestLoadSummariesUserYAML(t *testing.T) {
	dir := t.TempDir()

	writeFile(t, filepath.Join(dir, "field-summary.yaml"), `
summaries:
  - func: "example.com/foo.Bar"
    reads: ["user.ID"]
    writes: ["user.Name"]
    param_index: 1
`)
	specs, warnings := loadSummaries(dir)
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v", warnings)
	}
	spec, ok := specs["example.com/foo.Bar"]
	if !ok || len(spec.Reads) != 1 || spec.Reads[0] != "user.ID" ||
		len(spec.Writes) != 1 || spec.Writes[0] != "user.Name" || spec.ParamIndex != 1 {
		t.Errorf("user spec = %+v", spec)
	}

	if _, ok := specs["encoding/json.Unmarshal"]; !ok {
		t.Error("builtin summaries missing")
	}

	writeFile(t, filepath.Join(dir, "field-summary.yaml"), `
summaries:
  - func: "encoding/json.Unmarshal"
    writes: ["v.ID"]
    param_index: 1
`)
	specs, _ = loadSummaries(dir)
	if spec := specs["encoding/json.Unmarshal"]; len(spec.Writes) != 1 || spec.Writes[0] != "v.ID" {
		t.Errorf("override spec = %+v", spec)
	}

	writeFile(t, filepath.Join(dir, "field-summary.yaml"), `
summaries:
  - func: "example.com/foo.Bar"
    reads: ["a"]
  - func: "example.com/foo.Bar"
    reads: ["b"]
`)
	specs, warnings = loadSummaries(dir)
	if len(warnings) == 0 {
		t.Error("duplicate should warn")
	}
	if spec := specs["example.com/foo.Bar"]; len(spec.Reads) != 1 || spec.Reads[0] != "a" {
		t.Errorf("duplicate spec = %+v", spec)
	}

	writeFile(t, filepath.Join(dir, "field-summary.yaml"), "summaries: [broken")
	specs, warnings = loadSummaries(dir)
	if len(warnings) == 0 {
		t.Error("bad yaml should warn")
	}
	if _, ok := specs["encoding/json.Unmarshal"]; !ok {
		t.Error("builtin lost after bad yaml")
	}
}

// TestUserSummaryRelativeFieldPath：S2 回归——field-summary.yaml 相对字段
// 路径（"user.ID" 带实例前缀）须补全为类型限定路径（pkg.T.ID），
// 而非错误拼成 pkg.T.user.ID（此前补全条件含点相对路径全拼）。
func TestUserSummaryRelativeFieldPath(t *testing.T) {
	nodes, _, _ := indexFixtureFull(t, map[string]string{
		"go.mod": moduleGoMod,
		"external/external.go": `package external

func Wrap(v any) {}
`,
		"main.go": `package m

import "example.com/mtest/external"

type T struct {
	ID   int
	Name string
}

func f(t *T) {
	external.Wrap(t)
}
`,
		"field-summary.yaml": `summaries:
  - func: "example.com/mtest/external.Wrap"
    reads: ["user.ID", "user.Name"]
    param_index: 0
`,
	})
	var idPath, namePath bool
	for _, n := range nodes {
		if n.Kind != domain.KindFieldAccess || n.Property("is_external") != "true" {
			continue
		}
		fp := n.Property("full_path")
		switch fp {
		case "example.com/mtest.T.ID":
			idPath = true
		case "example.com/mtest.T.Name":
			namePath = true
		case "example.com/mtest.T.user.ID", "example.com/mtest.T.user.Name":
			t.Errorf("相对路径补全错误: full_path = %q", fp)
		}
	}
	if !idPath || !namePath {
		t.Errorf("相对路径未补全为类型限定路径: id=%v name=%v", idPath, namePath)
	}
}

// TestInterfaceSQLSummary：Q158——接口摘要 SQL 形态（gof Connector 接口：
// SQL 字符串在 Args[0]，无接收者）。ExecScalar=读（SELECT 列 read + filter）、
// ExecNonQuery=写（SET 列 write + filter）。
func TestInterfaceSQLSummary(t *testing.T) {
	nodes, _, _ := indexFixtureFull(t, map[string]string{
		"go.mod": moduleGoMod,
		"field-summary.yaml": `summaries:
  - iface: example.com/mtest.Connector
    method: ExecScalar
    kind: sql
    where_arg: 0
  - iface: example.com/mtest.Connector
    method: ExecNonQuery
    kind: sql
    where_arg: 0
    sql_write: true
`,
		"main.go": `package m

type Connector interface {
	ExecScalar(s string, result interface{}, args ...interface{}) error
	ExecNonQuery(sql string, args ...interface{}) (int, error)
}

func use(c Connector, id int64) {
	var n int64
	_ = c.ExecScalar("SELECT COUNT(1) FROM mm_member WHERE level= $1", &n, id)
	_, _ = c.ExecNonQuery("UPDATE mm_member SET level = $1 WHERE id = $2", 3, id)
}
`,
	})
	funcID := "symbol:go:example.com/mtest:use"
	find := func(name, access string) bool {
		for _, n := range nodes {
			if n.Kind == domain.KindFieldAccess && n.Property("func_id") == funcID &&
				n.Property("type_string") == "sql" && n.Name == name &&
				n.Property("access_kind") == access {
				return true
			}
		}
		return false
	}

	if !find("mm_member", "read") {
		t.Error("ExecScalar 未生成 mm_member 表级 read 节点")
	}
	if !find("mm_member.level", "filter") {
		t.Error("ExecScalar 未生成 mm_member.level filter 节点（$1 占位符）")
	}

	if !find("mm_member.level", "write") {
		t.Error("ExecNonQuery 未生成 mm_member.level write 节点")
	}
	if !find("mm_member.id", "filter") {
		t.Error("ExecNonQuery 未生成 mm_member.id filter 节点")
	}
}

// TestXORMSummarySelfContained：Q175——XORM 链式形态（Table().Where()
// .Find()）的表名/字段/查询条件提取。
func TestXORMSummarySelfContained(t *testing.T) {
	src := `package m

type Engine interface {
	Table(name string) Session
}

type Session interface {
	Where(cond string, args ...any) Session
	Find(out any) error
}

type Settlement struct {
	OrderID int64
	Amount  int64
}

func query(engine Engine, list *[]Settlement) {
	engine.Table("settlement").Where("order_id = ?", 1).Find(list)
}
`
	yaml := `summaries:
  - iface: "example.com/mtest.Engine"
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
    method: "Find"
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
	var filterSeen, readSeen, tableSeen bool
	for _, n := range nodes {
		if n.Kind != domain.KindFieldAccess || n.Property("is_external") != "true" {
			continue
		}
		switch n.Name {
		case "settlement.order_id":
			if n.Property("access_kind") == "filter" {
				filterSeen = true
			}
			t.Logf("node %s access=%v type=%v id=%s", n.Name, n.Property("access_kind"), n.Property("type_string"), n.ID)
			if n.Property("type_string") != "xorm" {
				t.Errorf("XORM 节点 type_string = %v, want xorm", n.Property("type_string"))
			}
		case "settlement.amount":
			if n.Property("access_kind") == "read" {
				readSeen = true
			}
		case "settlement":
			tableSeen = true
		}
	}
	if !tableSeen {
		t.Error("XORM Table 调用应发射整表节点 settlement")
	}
	if !filterSeen {
		t.Error("XORM Where 应发射 filter 节点 settlement.order_id（表名来自链式 Table）")
	}
	if !readSeen {
		t.Error("XORM Find 应发射字段 read 节点（settlement.amount）")
	}
}
