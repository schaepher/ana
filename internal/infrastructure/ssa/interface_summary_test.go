package ssa

import (
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

// TestInterfaceSummaryCustom：Q156 通用接口摘要——动态 invoke 且候选
// 实现为空（外部框架实现）时，按 field-summary.yaml 的 iface+method
// spec 映射表.列虚拟节点：Save=对象写、FindBy/Get=读出+where/主键
// filter、Count=filter。表名取实体 TableName() 常量（mm_order）。
func TestInterfaceSummaryCustom(t *testing.T) {
	nodes, facts, _ := indexFixtureFull(t, map[string]string{
		"go.mod": moduleGoMod,
		"field-summary.yaml": `summaries:
  - iface: example.com/mtest.Repo
    method: Save
    kind: write
    obj_arg: 0
  - iface: example.com/mtest.Repo
    method: FindBy
    kind: read
    where_arg: 0
  - iface: example.com/mtest.Repo
    method: Get
    kind: read
    id_arg: 0
  - iface: example.com/mtest.Repo
    method: Count
    kind: filter
    where_arg: 0
`,
		"main.go": `package m

type Order struct {
	Id       int
	FinalFee float64
}

func (o *Order) TableName() string { return "mm_order" }

// 接口无模块内实现——外部框架形态（泛型仓储，如 gof Repository[M]），
// 触发接口摘要；类型参数 M 提供实体类型（表名/字段展开来源）
type Repo[M any] interface {
	Save(v *M) error
	FindBy(where string, args ...interface{}) *M
	Get(id int) *M
	Count(where string, args ...interface{}) (int, error)
}

func useRepo(r Repo[Order], o *Order) {
	r.Save(o)
	x := r.FindBy("id = ? AND final_fee > ?", o.Id, 100)
	_ = x.FinalFee
	g := r.Get(o.Id)
	_ = g.FinalFee
	_, _ = r.Count("final_fee > ?", 100)
}
`,
	})
	funcID := "symbol:go:example.com/mtest:useRepo"
	find := func(name, access string) *domain.CodeEntity {
		for _, n := range nodes {
			if n.Kind == domain.KindFieldAccess && n.Property("func_id") == funcID &&
				n.Property("type_string") == "gorm" && n.Name == name &&
				n.Property("access_kind") == access {
				return n
			}
		}
		return nil
	}
	// Save：对象写（表名取 TableName → mm_order）
	for _, col := range []string{"mm_order.id", "mm_order.final_fee"} {
		if find(col, "write") == nil {
			t.Errorf("Save 未生成 write 节点 %s", col)
		}
	}
	// FindBy：读出 + where 双列 filter（AND 拆分）
	if find("mm_order.id", "read") == nil || find("mm_order.final_fee", "read") == nil {
		t.Error("FindBy 未生成 read 节点（对象读出）")
	}
	fid := find("mm_order.id", "filter")
	if fid == nil {
		t.Error("FindBy 未生成 id filter 节点")
	}
	ff := find("mm_order.final_fee", "filter")
	if ff == nil {
		t.Error("FindBy 未生成 final_fee filter 节点（AND 拆分）")
	}
	// Get：主键 filter（fallback id 列）
	if find("mm_order.id", "filter") == nil {
		t.Error("Get 未生成主键 id filter 节点")
	}
	// Count：filter 无 read
	if find("mm_order.final_fee", "filter") == nil {
		t.Error("Count 未生成 final_fee filter 节点")
	}
	// 值流边：o.Id → FindBy 的 id filter（值实参映射过滤列）
	foundEdge := false
	for _, f := range facts {
		if f.Kind == domain.FactSummaryIO {
			foundEdge = true
			break
		}
	}
	if !foundEdge {
		t.Error("接口摘要未产生 summary_io 边（值 → 过滤列）")
	}
}
