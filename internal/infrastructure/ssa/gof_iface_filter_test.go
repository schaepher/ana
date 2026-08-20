package ssa

import (
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

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

// TestInferIfaceFilterSQLType：Q205 通用性——inferInterfaceFilter 的
// vtype 不写死 gorm：where 串是完整 SQL（SELECT/FROM/WHERE）时标注
// sql（业务接口方法如 SelectByQuery 封装返回 []T 的形态），列条件
// 占位符形态（"col = $1"）保持 gorm。
func TestInferIfaceFilterSQLType(t *testing.T) {
	nodes, _, _ := indexFixtureFull(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

type AttrItem struct {
	Id     int
	AttrId int
	Value  string
}

type Repo interface {
	QueryBySQL(where string, args ...interface{}) []*AttrItem
	QueryByCol(where string, args ...interface{}) []*AttrItem
}

func use(r Repo, attrId int) {
	_ = r.QueryBySQL("SELECT * FROM attr_item WHERE attr_id = $1", attrId)
	_ = r.QueryByCol("attr_id = $1", attrId)
}
`,
	})
	funcID := "symbol:go:example.com/mtest:use"
	find := func(name, access, ts string) bool {
		for _, n := range nodes {
			if n.Kind == domain.KindFieldAccess && n.Property("func_id") == funcID &&
				n.Name == name && n.Property("access_kind") == access &&
				n.Property("type_string") == ts {
				return true
			}
		}
		return false
	}
	if !find("attr_item.attr_id", "filter", "sql") {
		t.Error("SQL 全文 where 的 filter 节点应为 type_string=sql（非固定 gorm）")
	}
	if !find("attr_item.attr_id", "filter", "gorm") {
		t.Error("列条件占位符形态保持 gorm")
	}
}
