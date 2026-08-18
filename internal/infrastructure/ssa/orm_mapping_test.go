package ssa

import (
	"sort"
	"strings"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

// Q211 orm.Mapping 表名映射（go2o `orm.Mapping(ValueCoupon{}, "pm_coupon")`
// 注册实体→表名）：收集阶段独立于发射（Index 开头全量扫描，跨包时序
// 无关），表名溯源优先级：链式 Table() > TableName() 方法 > Mapping
// 映射 > snakeCase(类型名) fallback。
//
// fixture 用 replace 模拟 gof 接口路径（github.com/ixre/gof/db/orm.Orm
// 本地模块）——collectOrmMappings 按真实接口路径匹配，模拟包保证
// 接口 String() 与真实一致；Select/GetBy/Save 走内置 gof spec（Q205）。

const ormMappingFixtureGoMod = `module example.com/mtest

go 1.26

require github.com/ixre/gof v0.0.0

replace github.com/ixre/gof => ./gofmock
`

const ormMappingFixtureGofMock = `module github.com/ixre/gof

go 1.26
`

// gof orm.Orm 接口模拟（与 github.com/ixre/gof@v1.17.15/db/orm/orm.go
// 的 Orm 接口同名方法子集——Mapping(v, table) + Select/GetBy/Save）
const ormMappingFixtureOrmIface = `package orm

type Orm interface {
	Mapping(v interface{}, table string) error
	Select(dst interface{}, where string, args ...interface{}) error
	GetBy(entity interface{}, where string, args ...interface{}) error
	Save(primary interface{}, entity interface{}) (int64, int64, error)
}
`

const ormMappingFixtureModel = `package model

type AttrItem struct {
	Id      int
	AttrId  int
	ModelId int
	Value   string
}
`

// ormMappingNodes 收集测试用虚拟节点（表.列 + access）。
func ormMappingNodes(nodes []*domain.CodeEntity) map[string]string {
	out := map[string]string{}
	for _, n := range nodes {
		if n.Kind == domain.KindFieldAccess && n.Property("is_external") == "true" {
			out[n.Name] = n.Property("access_kind")
		}
	}
	return out
}

// mapKeys 调试用：map 键排序输出。
func mapKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// TestOrmMappingCrossPackage：注册（reg 包）与使用（use 包）跨包分离——
// 收集全量扫描，与 emitFunction 按包处理顺序无关；使用点表名解析为
// Mapping 注册名（t_attr_items），而非 snakeCase fallback（attr_item）。
func TestOrmMappingCrossPackage(t *testing.T) {
	files := map[string]string{
		"go.mod":                ormMappingFixtureGoMod,
		"gofmock/go.mod":        ormMappingFixtureGofMock,
		"gofmock/db/orm/orm.go": ormMappingFixtureOrmIface,
		"model/entity.go":       ormMappingFixtureModel,
		"reg/reg.go": `package reg

import (
	"github.com/ixre/gof/db/orm"

	"example.com/mtest/model"
)

// Register 在独立包注册实体→表名映射（go2o repo_v1.go OrmMapping 形态）
func Register(o orm.Orm) error {
	return o.Mapping(model.AttrItem{}, "t_attr_items")
}
`,
		"use/use.go": `package use

import (
	"github.com/ixre/gof/db/orm"

	"example.com/mtest/model"
)

// Use 使用点：实体类型经 Mapping 解析表名（内置 gof spec：Select 读路径）
func Use(o orm.Orm) error {
	var list []model.AttrItem
	return o.Select(&list, "attr_id = $1", 1)
}
`,
	}
	nodes, _, _ := indexFixtureFull(t, files)
	cols := ormMappingNodes(nodes)
	if _, ok := cols["t_attr_items.attr_id"]; !ok {
		t.Fatalf("Mapping 表名未生效：节点含 %d 个外部列，无 t_attr_items.attr_id（%v）",
			len(cols), mapKeys(cols))
	}
	for name := range cols {
		if strings.HasPrefix(name, "attr_item.") {
			t.Fatalf("应使用 Mapping 表名 t_attr_items，却出现 snakeCase fallback %s", name)
		}
	}
	// filter 节点（where 串列名）也走 Mapping 表名
	if cols["t_attr_items.attr_id"] != "filter" {
		t.Fatalf("attr_id 应为 filter 节点，got %q", cols["t_attr_items.attr_id"])
	}
}

// TestOrmMappingTableNameMethodPriority：TableName() 方法优先于 Mapping
// 注册（tableNameOfSlow 顺序：TableName > Mapping > snakeCase）。
func TestOrmMappingTableNameMethodPriority(t *testing.T) {
	files := map[string]string{
		"go.mod":                ormMappingFixtureGoMod,
		"gofmock/go.mod":        ormMappingFixtureGofMock,
		"gofmock/db/orm/orm.go": ormMappingFixtureOrmIface,
		"model/entity.go": `package model

type WithMethod struct {
	Id   int
	Name string
}

func (WithMethod) TableName() string { return "tn_method" }
`,
		"reg/reg.go": `package reg

import (
	"github.com/ixre/gof/db/orm"

	"example.com/mtest/model"
)

func Register(o orm.Orm) error {
	return o.Mapping(model.WithMethod{}, "tn_mapping")
}
`,
		"use/use.go": `package use

import (
	"github.com/ixre/gof/db/orm"

	"example.com/mtest/model"
)

func Use(o orm.Orm) error {
	var list []model.WithMethod
	return o.Select(&list, "name = $1", "x")
}
`,
	}
	nodes, _, _ := indexFixtureFull(t, files)
	cols := ormMappingNodes(nodes)
	if _, ok := cols["tn_method.id"]; !ok {
		t.Fatalf("TableName() 应优先于 Mapping：节点无 tn_method.id（%v）", mapKeys(cols))
	}
	for name := range cols {
		if strings.HasPrefix(name, "tn_mapping.") {
			t.Fatalf("Mapping 不应覆盖 TableName()：出现 %s", name)
		}
	}
}

// TestOrmMappingWritePath：写路径（Save 全列 write）同样用 Mapping 表名
// ——applyORMWrite 与 applyORMRead 共用 tableNameOf。
func TestOrmMappingWritePath(t *testing.T) {
	files := map[string]string{
		"go.mod":                ormMappingFixtureGoMod,
		"gofmock/go.mod":        ormMappingFixtureGofMock,
		"gofmock/db/orm/orm.go": ormMappingFixtureOrmIface,
		"model/entity.go":       ormMappingFixtureModel,
		"reg/reg.go": `package reg

import (
	"github.com/ixre/gof/db/orm"

	"example.com/mtest/model"
)

func Register(o orm.Orm) error {
	return o.Mapping(model.AttrItem{}, "t_attr_items")
}
`,
		"use/use.go": `package use

import (
	"github.com/ixre/gof/db/orm"

	"example.com/mtest/model"
)

func Use(o orm.Orm) error {
	item := &model.AttrItem{AttrId: 1}
	_, _, err := o.Save(0, item)
	return err
}
`,
	}
	nodes, _, _ := indexFixtureFull(t, files)
	cols := ormMappingNodes(nodes)
	if cols["t_attr_items.attr_id"] != "write" {
		t.Fatalf("Save 写路径应产生 t_attr_items.attr_id write 节点，got %q（cols=%v）",
			cols["t_attr_items.attr_id"], mapKeys(cols))
	}
}
