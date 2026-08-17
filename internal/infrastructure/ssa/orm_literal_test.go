package ssa

import (
	"strings"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

// TestORMWriteVariableArg：② ORM 映射——实参为变量（非结构体字面量，
// 调用点无字段级 Store，如 Create(row) / Delete(&X{})）时，仍须生成
// "表.列" 虚拟节点（fieldValueOf 定位不到字段值时按类型展开）。
// 通过 field-summary.yaml 的 orm_write 条目定义本地 ORM 写调用。
func TestORMWriteVariableArg(t *testing.T) {
	nodes, facts, _ := indexFixtureFull(t, map[string]string{
		"go.mod": moduleGoMod,
		"field-summary.yaml": `summaries:
  - func: example.com/mtest.(DB).create
    orm_write: true
    param_index: 1
`,
		"main.go": `package m

type DB struct{}

type User struct {
	Name   string
	APIKey string
}

func (d *DB) create(row *User) {}

func f(db *DB, row *User) {
	db.create(row)
}
`,
	})
	funcID := "symbol:go:example.com/mtest:f"
	// 表.列 虚拟节点：user.name / user.api_key（变量实参 → 类型展开，
	// 列名与 GORM 默认命名一致）
	var vName, vKey *domain.CodeEntity
	for _, n := range nodes {
		if n.Kind != domain.KindFieldAccess || n.Property("func_id") != funcID ||
			n.Property("type_string") != "gorm" {
			continue
		}
		switch n.Name {
		case "user.name":
			vName = n
		case "user.api_key":
			vKey = n
		}
	}
	if vName == nil || vKey == nil {
		t.Fatalf("变量实参未生成 表.列 虚拟节点: %+v", nodes)
	}
	for _, v := range []*domain.CodeEntity{vName, vKey} {
		if v.Property("access_kind") != "write" {
			t.Errorf("gorm 节点 access = %q, want write", v.Property("access_kind"))
		}
	}

	for _, v := range []*domain.CodeEntity{vName, vKey} {
		found := false
		for _, f := range facts {
			if f.Kind == domain.FactSummaryIO && string(f.TargetID) == string(v.ID) {
				found = true
				if !strings.HasPrefix(string(f.SourceID), funcID) {
					t.Errorf("summary_io 来源应为调用者值节点: %s", f.SourceID)
				}
			}
		}
		if !found {
			t.Errorf("gorm 节点 %s 缺 summary_io 边（对象值 → 列）", v.Name)
		}
	}
}

// TestORMWriteEmptyLiteral：空字面量实参（Delete(&X{})）同样按类型展开。
func TestORMWriteEmptyLiteral(t *testing.T) {
	nodes, _, _ := indexFixtureFull(t, map[string]string{
		"go.mod": moduleGoMod,
		"field-summary.yaml": `summaries:
  - func: example.com/mtest.(DB).remove
    orm_write: true
    param_index: 1
`,
		"main.go": `package m

type DB struct{}

type Session struct {
	ID string
}

func (d *DB) remove(s *Session) {}

func f(db *DB) {
	db.remove(&Session{})
}
`,
	})
	funcID := "symbol:go:example.com/mtest:f"
	found := false
	for _, n := range nodes {
		if n.Kind == domain.KindFieldAccess && n.Property("func_id") == funcID &&
			n.Property("type_string") == "gorm" && n.Name == "session.id" {
			found = true
		}
	}
	if !found {
		t.Errorf("空字面量实参未生成 session.id 虚拟节点")
	}
}
