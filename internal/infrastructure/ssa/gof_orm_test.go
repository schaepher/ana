package ssa

import (
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

// TestGofOrmIfaceSelect：Q205 gof orm.Orm 字符串 where 形态——go2o
// 的 p.o.Select(&list, "attr_id = $1", pk) 等封装调用此前无 spec，
// where 字符串列名不产生 filter 节点（attr/attr_item 键关联漏报根因）。
// YAML 自定义 iface spec（ObjArg 0 = dst/实体，WhereArg 1 = where 串）：
//   - read：dst slice 元素类型 → 表名 + 全列 read 节点
//   - WhereArg：where 串列名 → filter 节点 + 绑定值实参 → filter 边
func TestGofOrmIfaceSelect(t *testing.T) {
	nodes, facts, _ := indexFixtureFull(t, map[string]string{
		"go.mod": moduleGoMod,
		"field-summary.yaml": `summaries:
  - iface: example.com/mtest.Orm
    method: Select
    kind: read
    obj_arg: 0
    where_arg: 1
  - iface: example.com/mtest.Orm
    method: GetBy
    kind: read
    obj_arg: 0
    where_arg: 1
  - iface: example.com/mtest.Orm
    method: Delete
    kind: write
    obj_arg: 0
    where_arg: 1
  - iface: example.com/mtest.Orm
    method: SelectByQuery
    kind: sql
    where_arg: 1
  - iface: example.com/mtest.Orm
    method: Save
    kind: write
    obj_arg: 1
`,
		"main.go": `package m

type AttrItem struct {
	Id      int
	AttrId  int
	ModelId int
	Value   string
}

// gof orm.Orm 形态（外部框架接口，无模块内实现——触发接口摘要）
type Orm interface {
	Select(dst interface{}, where string, args ...interface{}) error
	GetBy(entity interface{}, where string, args ...interface{}) error
	Delete(entity interface{}, where string, args ...interface{}) (int64, error)
	SelectByQuery(dst interface{}, sql string, args ...interface{}) error
	Save(primary interface{}, entity interface{}) (int64, int64, error)
}

func useOrm(o Orm, attrId int) {
	list := []*AttrItem{}
	_ = o.Select(&list, "attr_id = $1", attrId)
	one := AttrItem{}
	_ = o.GetBy(&one, "attr_id = $1", attrId)
	items := []AttrItem{}
	_ = o.SelectByQuery(&items, "SELECT * FROM attr_item WHERE value = $1", attrId)
	_, _ = o.Delete(AttrItem{}, "attr_id = $1", attrId)
	_, _, _ = o.Save(attrId, &AttrItem{AttrId: attrId})
}
`,
	})
	funcID := "symbol:go:example.com/mtest:useOrm"
	find := func(name, access string) *domain.CodeEntity {
		for _, n := range nodes {
			if n.Kind != domain.KindFieldAccess || n.Property("func_id") != funcID ||
				n.Name != name || n.Property("access_kind") != access {
				continue
			}
			// ORM 形态 gorm / SQL 直写形态 sql
			ts := n.Property("type_string")
			if ts == "gorm" || ts == "sql" {
				return n
			}
		}
		return nil
	}
	// Select：dst slice 元素 → 表名 attr_item + 全列 read 节点
	for _, col := range []string{"attr_item.id", "attr_item.attr_id", "attr_item.value"} {
		if find(col, "read") == nil {
			t.Errorf("Select 未生成 read 节点 %s", col)
		}
	}
	// Select/GetBy/Delete：where 字符串列名 → filter 节点（键关联终点）
	if find("attr_item.attr_id", "filter") == nil {
		t.Error("Select 未生成 attr_item.attr_id filter 节点（where 串列名）")
	}
	// SelectByQuery：直写 SQL 同样解析出 filter（value 列，与 Select 区分）
	if find("attr_item.value", "filter") == nil {
		t.Error("SelectByQuery 未生成 attr_item.value filter 节点")
	}
	// Save（方法形态）/Delete：实体类型 → 全列 write
	if find("attr_item.attr_id", "write") == nil {
		t.Error("Save/Delete 未生成 write 节点")
	}
	// 值流边：attrId 实参 → attr_item.attr_id filter（键关联链贯通的关键）
	edgeOK := false
	for _, f := range facts {
		if f.Kind != domain.FactSummaryIO {
			continue
		}
		tn := nodeNameOf(nodes, f.TargetID)
		if tn == "attr_item.attr_id" {
			edgeOK = true
			break
		}
	}
	if !edgeOK {
		t.Error("未找到绑定值 → attr_item.attr_id filter 的 summary_io 边")
	}
}

// TestInferIfaceFilterFallback：Q205 兜底——无 spec 的业务接口方法
// （go2o SelectAttrItem 形态：where 常量形参 + slice 返回）在调用点
// 启发式识别：where 串列名 + 返回元素表名 → filter 节点 + 绑定值边。
// 这补上"包裹方法"漏报：SelectAttrItem(where string) 内部 p.o.Select
// 的 where 是形参（常量在调用点，不跨函数传播）。
func TestInferIfaceFilterFallback(t *testing.T) {
	nodes, facts, _ := indexFixtureFull(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

type AttrItem struct {
	Id     int
	AttrId int
	Value  string
}

// 业务接口（无 spec，模块内有实现——但调用方只持有接口）
type Repo interface {
	SelectItems(where string, args ...interface{}) []*AttrItem
}

func use(r Repo, attrId int) {
	list := r.SelectItems("attr_id = $1", attrId)
	_ = list
}
`,
	})
	funcID := "symbol:go:example.com/mtest:use"
	find := func(name, access string) *domain.CodeEntity {
		for _, n := range nodes {
			if n.Kind == domain.KindFieldAccess && n.Property("func_id") == funcID &&
				n.Name == name && n.Property("access_kind") == access {
				return n
			}
		}
		return nil
	}
	if find("attr_item.attr_id", "filter") == nil {
		t.Error("无 spec 接口调用未生成 attr_item.attr_id filter（where 常量兜底）")
	}
	edgeOK := false
	for _, f := range facts {
		if f.Kind == domain.FactSummaryIO && nodeNameOf(nodes, f.TargetID) == "attr_item.attr_id" {
			edgeOK = true
			break
		}
	}
	if !edgeOK {
		t.Error("未找到绑定值 → attr_item.attr_id filter 的 summary_io 边")
	}
}

// TestSliceReturnNoDualEmit：Q205 双发射修复——slice 变量返回（go2o
// SelectAttr：list := []T{}; p.o.Select(&list); return list）中，读边
// 连 Alloc（#t0），returns 边连 UnOp load（#list）——同一逻辑值两个
// 节点，跨函数链断（attr.id → attr_item.attr_id 漏报根因）。
// 修复：emitValue 的 UnOp MUL of Alloc 分支在 Alloc 已发射时复用其 ID。
func TestSliceReturnNoDualEmit(t *testing.T) {
	nodes, facts, _ := indexFixtureFull(t, map[string]string{
		"go.mod": moduleGoMod,
		"field-summary.yaml": `summaries:
  - iface: example.com/mtest.Orm
    method: Select
    kind: read
    obj_arg: 0
    where_arg: 1
`,
		"main.go": `package m

type AttrItem struct {
	Id     int
	AttrId int
	Value  string
}

type Orm interface {
	Select(dst interface{}, where string, args ...interface{}) error
}

// 模拟 go2o SelectAttr：slice 变量读出后整体返回
func listAll(o Orm) []*AttrItem {
	list := []*AttrItem{}
	_ = o.Select(&list, "attr_id = $1", 1)
	return list
}

func caller(o Orm) {
	arr := listAll(o)
	_ = arr[0].Id
}
`,
	})
	// 读边：attr_item.attr_id read → list 值
	readTarget := ""
	for _, f := range facts {
		if f.Kind == domain.FactSummaryIO && nodeNameOf(nodes, f.SourceID) == "attr_item.attr_id" {
			readTarget = string(f.TargetID)
			break
		}
	}
	if readTarget == "" {
		t.Fatal("未找到 attr_item.attr_id read 的 summary_io 边")
	}
	// returns 边：listAll 返回值 → caller 的 arr
	retSource := ""
	for _, f := range facts {
		if f.Kind == domain.FactReturns && nodeNameOf(nodes, f.SourceID) == "list" &&
			nodeNameOf(nodes, f.TargetID) == "arr" {
			retSource = string(f.SourceID)
			break
		}
	}
	if retSource == "" {
		t.Fatal("未找到 list → arr 的 returns 边")
	}
	// 无双发射：returns 的 source 必须与读边的 target 是同一节点
	if retSource != readTarget {
		t.Errorf("双发射：读边 target=%s 但 returns source=%s（同一逻辑值两个节点）", readTarget, retSource)
	}
}

// TestBuiltinGofOrmSpecs：Q205 内置 spec 注册校验——gof orm.Orm 字符串
// where 形态的 key 与参数（防止误删/参数漂移）。
func TestBuiltinGofOrmSpecs(t *testing.T) {
	specs := builtinSummaries()
	check := func(key, kind string, objArg, whereArg int) {
		s, ok := specs[key]
		if !ok {
			t.Errorf("内置 spec 缺失: %s", key)
			return
		}
		if s.Kind != kind || s.ObjArg != objArg || s.WhereArg != whereArg {
			t.Errorf("%s spec 参数 = kind:%s obj:%d where:%d, want kind:%s obj:%d where:%d",
				key, s.Kind, s.ObjArg, s.WhereArg, kind, objArg, whereArg)
		}
	}
	base := "iface:github.com/ixre/gof/db/orm.Orm."
	check(base+"Select", "read", 0, 1)
	check(base+"GetBy", "read", 0, 1)
	check(base+"Delete", "write", 0, 1)
	check(base+"SelectByQuery", "sql", 0, 1)
	check(base+"GetByQuery", "sql", 0, 1)
	check(base+"Save", "write", 1, 0) // WhereArg 未设（默认 0，unwrap 常量失败即跳过）
}

// nodeNameOf 按 ID 查节点名（测试辅助）。
func nodeNameOf(nodes []*domain.CodeEntity, id domain.CanonicalID) string {
	for _, n := range nodes {
		if n.ID == id {
			return n.Name
		}
	}
	return ""
}
