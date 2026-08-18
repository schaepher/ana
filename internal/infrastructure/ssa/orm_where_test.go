package ssa

import (
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

// TestGORMWhereStringCols Q220：GORM Where 字符串实参列名提取——原实现
// 只截 " = "（strings.Index 定位），"b.id is null" / "name LIKE $1" 等
// 其他条件形态整串当列名（go2o 实测 merchant.b.id is null 垃圾节点）。
// 改用 whereColsOf 提取列名（go.mod replace 模拟 gorm.io/gorm 形态，
// 与 xorm_static_test 同款）。
func TestGORMWhereStringCols(t *testing.T) {
	nodes, _, _ := indexFixtureFull(t, map[string]string{
		"go.mod": `module example.com/mtest

go 1.21

require gorm.io/gorm v0.0.0

replace gorm.io/gorm => ./gorm
`,
		"gorm/go.mod": "module gorm.io/gorm\n\ngo 1.21\n",
		"gorm/gorm.go": `package gorm

type DB struct{}

func (db *DB) Model(value interface{}) *DB { return db }

func (db *DB) Where(query interface{}, args ...interface{}) *DB { return db }

func (db *DB) Find(out interface{}, args ...interface{}) *DB { return db }
`,
		"main.go": `package mtest

import "gorm.io/gorm"

type Merchant struct {
	ID   int64
	Name string
	Code string
}

func q1(db *gorm.DB, m *Merchant) {
	db.Model(m).Where("b.id is null").Find(&m)
}
func q2(db *gorm.DB, m *Merchant) {
	db.Model(m).Where("name LIKE ?", "x%").Find(&m)
}
func q3(db *gorm.DB, m *Merchant) {
	db.Model(m).Where("code = ?", "A").Find(&m)
}
`,
	})
	var got []string
	for _, n := range nodes {
		if n.Kind == domain.KindFieldAccess && n.Name != "" {
			got = append(got, n.Name+"|"+n.Property("access_kind"))
		}
	}
	for _, want := range []string{
		"merchant.b.id|filter",
		"merchant.name|filter",
		"merchant.code|filter",
	} {
		found := false
		for _, g := range got {
			if g == want {
				found = true
			}
		}
		if !found {
			t.Errorf("缺 %s；got=%v", want, got)
		}
	}
	for _, g := range got {
		if g == "merchant.b.id is null|filter" || g == "merchant.name LIKE ?|filter" {
			t.Errorf("where 整串不得当列名：%s；got=%v", g, got)
		}
	}
}
