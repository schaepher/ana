package ssa

import (
	"strings"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

// TestORMReadRangeUnOpEdge Q221 回归：ORM 读（Find）对象实参变量被
// range 解引用（*orders 的 UnOp(MUL, Alloc)）时——Q205 双发射分支
// 只设 values 缓存就提前 return，节点未发射 → read → 对象边 FK 失败
// → 真实键关联漏报（examples/repro-order-id-fk 复现）。修复：
// 不提前 return，落入统一发射分支（节点落库）。
func TestORMReadRangeUnOpEdge(t *testing.T) {
	src := `package m

import "xorm.io/xorm"

type Order struct {
	ID         uint64
	PartyID uint64
}

func query(s *xorm.Session, last uint64) error {
	var orders []Order
	if err := s.Table("order_tab").Where("id > ?", last).Find(&orders); err != nil {
		return err
	}
	for _, o := range orders {
		var bs []struct{}
		if err := s.Table("book_tab").Where("party_id = ?", o.PartyID).Find(&bs); err != nil {
			return err
		}
	}
	return nil
}
`
	nodes, facts, _ := indexFixtureFull(t, map[string]string{
		"go.mod": `module example.com/mtest

go 1.21

require xorm.io/xorm v0.0.0

replace xorm.io/xorm => ./xorm
`,
		"xorm/go.mod": "module xorm.io/xorm\n\ngo 1.21\n",
		"xorm/session.go": `package xorm

type Session struct{}

func (s *Session) Table(name string) *Session { return s }
func (s *Session) Where(query interface{}, args ...interface{}) *Session {
	return s
}
func (s *Session) Find(beans interface{}, condiBeans ...interface{}) error { return nil }
`,
		"main.go": src,
	})
	// 1. 对象变量节点落库（#orders——range 解引用 + Find 实参共用）
	ordersNode := false
	for _, n := range nodes {
		if strings.HasSuffix(string(n.ID), "#orders") && n.Kind == domain.KindSSAValue {
			ordersNode = true
		}
	}
	if !ordersNode {
		t.Fatalf("对象变量节点 #orders 应落库（Q221 修复），nodes=%d", len(nodes))
	}
	// 2. read 节点（order_tab.party_id.read）有 summary_io 出边 → #orders
	readID := ""
	for _, n := range nodes {
		if strings.Contains(string(n.ID), "order_tab.party_id.read") {
			readID = string(n.ID)
		}
	}
	if readID == "" {
		t.Fatalf("缺 order_tab.party_id.read 节点")
	}
	outEdge := false
	for _, f := range facts {
		if string(f.SourceID) == readID && f.Kind == domain.FactSummaryIO &&
			strings.HasSuffix(string(f.TargetID), "#orders") {
			outEdge = true
		}
	}
	if !outEdge {
		t.Fatalf("read 节点应有 summary_io 出边 → #orders（Q221 修复）")
	}
	// 3. 字段读链：orders → o.PartyID.read（range 元素读）值流
	fieldRead := false
	for _, f := range facts {
		if strings.Contains(string(f.SourceID), "o.PartyID") && f.Kind == domain.FactDataFlowsTo {
			fieldRead = true
		}
	}
	if !fieldRead {
		t.Fatalf("对象 → 字段读链缺失")
	}
}
