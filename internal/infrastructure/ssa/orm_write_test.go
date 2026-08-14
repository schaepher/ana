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
	// 对象值 → 列节点 summary_io 边（实参值行 → 持久化列）
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

// TestORMChainUpdateColumnName：⑦ 链式 ORM——Model(&X{主键}).Where(...)
// .Update("col", v) 字符串列名形态：表名溯源链式 Model 范围对象，
// 列名取字符串实参（此前仅结构体实参可映射，该形态零节点）。
// Model 本身非写操作不配 orm_write——范围对象经 receiver 定义链解析。
func TestORMChainUpdateColumnName(t *testing.T) {
	nodes, facts, _ := indexFixtureFull(t, map[string]string{
		"go.mod": moduleGoMod,
		"field-summary.yaml": `summaries:
  - func: example.com/mtest.(DB).Update
    orm_write: true
    param_index: 1
`,
		"main.go": `package m

type DB struct{}

type Session struct {
	ID     string
	Status string
}

func (d *DB) Model(v any) *DB { return d }

func (d *DB) Where(q string, v any) *DB { return d }

func (d *DB) Update(col string, v any) {}

func f(db *DB) {
	db.Model(&Session{ID: "s1"}).Where("status = ?", "x").Update("status", "done")
}
`,
	})
	funcID := "symbol:go:example.com/mtest:f"
	// 表名溯源 Model(&Session{}) → session，列名取字符串实参 → status
	var vCol *domain.CodeEntity
	for _, n := range nodes {
		if n.Kind == domain.KindFieldAccess && n.Property("func_id") == funcID &&
			n.Property("type_string") == "gorm" && n.Name == "session.status" {
			vCol = n
		}
	}
	if vCol == nil {
		t.Fatalf("链式 Update 未生成 session.status 虚拟节点: %+v", nodes)
	}
	if vCol.Property("access_kind") != "write" {
		t.Errorf("access = %q, want write", vCol.Property("access_kind"))
	}
	// 值实参 "done" → 列节点 summary_io 边
	found := false
	for _, f := range facts {
		if f.Kind == domain.FactSummaryIO && string(f.TargetID) == string(vCol.ID) {
			found = true
		}
	}
	if !found {
		t.Errorf("链式 Update 缺 summary_io 边（值 → session.status）")
	}
	// Model 非写操作：除 Update 的 session.status 外不应有其他表.列节点
	for _, n := range nodes {
		if n.Kind == domain.KindFieldAccess && n.Property("func_id") == funcID &&
			n.Property("type_string") == "gorm" && n.Name != "session.status" {
			t.Errorf("Model 范围对象不应产生表.列节点: %s", n.Name)
		}
	}
}
