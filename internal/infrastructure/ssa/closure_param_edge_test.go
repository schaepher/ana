package ssa

import (
	"strings"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

// TestClosureParamFindTarget Q222 同款形态：*[]Order 闭包形参直接作为
// Find 对象实参。emitValue 的 Parameter 分支（Q178）假设签名节点已由
// emitSignatureNodes 发射——但该发射只对顶层函数（FuncDecl）执行，
// 闭包（FuncLit）由 emitFunction 的闭包分支处理，不发射签名节点 →
// 返回未落库 ID → read → 对象边端点缺失 → 真实键关联漏报
// （examples/repro-order-id-fk 的闭包变体）。
func TestClosureParamFindTarget(t *testing.T) {
	src := `package m

import "xorm.io/xorm"

type Order struct {
	ID      uint64
	PartyID uint64
}

func withTx(s *xorm.Session, fn func(*xorm.Session, *[]Order) error) error {
	return fn(s, nil)
}

func run(s *xorm.Session) error {
	return withTx(s, func(tx *xorm.Session, target *[]Order) error {
		return tx.Table("order_tab").Find(target)
	})
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
func (s *Session) Find(beans interface{}, condiBeans ...interface{}) error { return nil }
`,
		"main.go": src,
	})
	// 1. 闭包形参 target 的节点应落库（run 函数命名空间，Parameter 分支 ID）
	paramNode := false
	for _, n := range nodes {
		if strings.HasSuffix(string(n.ID), "#param.target") && n.Kind == domain.KindSSAValue {
			paramNode = true
		}
	}
	if !paramNode {
		t.Fatalf("闭包形参 #param.target 节点应落库（Q222 同款），nodes=%d", len(nodes))
	}
	// 2. order_tab 的 read 节点有 summary_io 出边 → #param.target
	hasEdge := false
	for _, f := range facts {
		if f.Kind == domain.FactSummaryIO &&
			strings.Contains(string(f.SourceID), "order_tab.") &&
			strings.HasSuffix(string(f.TargetID), "#param.target") {
			hasEdge = true
		}
	}
	if !hasEdge {
		t.Fatalf("order_tab read 节点应有 summary_io 出边 → #param.target")
	}
}

// TestClosureParamWhereValue 闭包形参作为 Where 条件值实参（事务/回调
// 场景高频）：filter 节点 → 值节点的边端点 #param.lastID 应落库。
func TestClosureParamWhereValue(t *testing.T) {
	src := `package m

import "xorm.io/xorm"

type Order struct {
	ID      uint64
	PartyID uint64
}

func withLastID(s *xorm.Session, fn func(*xorm.Session, uint64) error) error {
	return fn(s, 0)
}

func run(s *xorm.Session) error {
	return withLastID(s, func(tx *xorm.Session, lastID uint64) error {
		var orders []Order
		return tx.Table("order_tab").Where("id > ?", lastID).Find(&orders)
	})
}
`
	nodes, _, _ := indexFixtureFull(t, map[string]string{
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
	// 闭包形参 lastID（Where 条件值）的节点应落库
	paramNode := false
	for _, n := range nodes {
		if strings.HasSuffix(string(n.ID), "#param.lastID") && n.Kind == domain.KindSSAValue {
			paramNode = true
		}
	}
	if !paramNode {
		t.Fatalf("闭包形参 #param.lastID（Where 条件值）节点应落库，nodes=%d", len(nodes))
	}
}

// TestNestedClosureORMRead 嵌套闭包（闭包内再闭包）中的 ORM 读：
// emitFunction 闭包分支对 parent 也是闭包的 fn 直接跳过（无 Object），
// 内层闭包的字段访问/ORM 调用整块丢失。
func TestNestedClosureORMRead(t *testing.T) {
	src := `package m

import "xorm.io/xorm"

type Order struct {
	ID      uint64
	PartyID uint64
}

func withTx(s *xorm.Session, fn func(*xorm.Session) error) error {
	return fn(s)
}

func run(s *xorm.Session) error {
	return withTx(s, func(tx *xorm.Session) error {
		var orders []Order
		if err := tx.Table("order_tab").Find(&orders); err != nil {
			return err
		}
		return withTx(tx, func(tx2 *xorm.Session) error {
			var bs []struct{}
			return tx2.Table("book_tab").Find(&bs)
		})
	})
}
`
	nodes, _, _ := indexFixtureFull(t, map[string]string{
		"go.mod": `module example.com/mtest

go 1.21

require xorm.io/xorm v0.0.0

replace xorm.io/xorm => ./xorm
`,
		"xorm/go.mod": "module xorm.io/xorm\n\ngo 1.21\n",
		"xorm/session.go": `package xorm

type Session struct{}

func (s *Session) Table(name string) *Session { return s }
func (s *Session) Find(beans interface{}, condiBeans ...interface{}) error { return nil }
`,
		"main.go": src,
	})
	// 内层闭包的 ORM 读（book_tab）节点应存在
	innerRead := false
	for _, n := range nodes {
		if strings.Contains(string(n.ID), "book_tab.") {
			innerRead = true
		}
	}
	if !innerRead {
		t.Fatalf("嵌套闭包内的 ORM 读（book_tab）节点应存在，nodes=%d", len(nodes))
	}
}
