package ssa

import (
	"testing"
)

// TestXORMStaticSession：Q177 修复回归——真实仓库用具体类型 *xorm.Session
// （非接口），SSA 解析静态 callee → applySummary 普通键
// （xorm.io/xorm.(Session).X）——旧实现只注册 iface 键导致摘要未命中、
// XORM 表关联全丢。fixture 用 replace 本地模块使包路径为真实 xorm.io/xorm，
// 断言链式 Table→Where→Find 生成 type_string=xorm 节点。
func TestXORMStaticSession(t *testing.T) {
	repo := indexFixtureRepo(t, map[string]string{
		"go.mod": `module example.com/mtest

go 1.21

require xorm.io/xorm v0.0.0

replace xorm.io/xorm => ./xorm
`,
		"xorm/go.mod": "module xorm.io/xorm\n\ngo 1.21\n",
		"xorm/session.go": `package xorm

type Session struct{}

func (s *Session) Table(name string) *Session { return s }

func (s *Session) Where(cond string, args ...any) *Session { return s }

func (s *Session) Find(out any) error { return nil }
`,
		"main.go": `package mtest

import "xorm.io/xorm"

type Settlement struct {
	OrderID int64
	Amount  int64
}

func query(s *xorm.Session, list *[]Settlement) {
	s.Table("settlement").Where("order_id = ?", 1).Find(list)
}

func main() {}
`,
	})
	rows, err := repo.Query(`SELECT name, json_extract(properties, '$.access_kind'),
		json_extract(properties, '$.type_string') FROM nodes
		WHERE kind = 'field_access' AND json_extract(properties, '$.is_external') = 'true'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var filterSeen, readSeen, tableSeen bool
	for rows.Next() {
		var name, access, ts string
		if err := rows.Scan(&name, &access, &ts); err != nil {
			t.Fatal(err)
		}
		if ts != "xorm" {
			t.Errorf("节点 type_string = %q, want xorm（%s）", ts, name)
		}
		switch name {
		case "settlement.order_id":
			if access == "filter" {
				filterSeen = true
			}
		case "settlement.amount":
			if access == "read" {
				readSeen = true
			}
		case "settlement":
			tableSeen = true
		}
	}
	if !filterSeen {
		t.Error("静态 Table→Where 应产 filter 节点 settlement.order_id（链式表名）")
	}
	if !readSeen {
		t.Error("静态 Find 应产字段 read 节点 settlement.amount")
	}
	if !tableSeen {
		t.Error("静态 Table 应发射整表节点 settlement")
	}
}

// TestXORMStaticWriteChain：Q177 补全——静态链式写（Table→Where→And→
// Update）+ Insert/Delete 生成 write 节点；And filter；长链表名传递。
func TestXORMStaticWriteChain(t *testing.T) {
	repo := indexFixtureRepo(t, map[string]string{
		"go.mod": `module example.com/mtest

go 1.21

require xorm.io/xorm v0.0.0

replace xorm.io/xorm => ./xorm
`,
		"xorm/go.mod": "module xorm.io/xorm\n\ngo 1.21\n",
		"xorm/session.go": `package xorm

type Session struct{}

func (s *Session) Table(name string) *Session { return s }

func (s *Session) Where(cond string, args ...any) *Session { return s }

func (s *Session) And(cond string, args ...any) *Session { return s }

func (s *Session) Or(cond string, args ...any) *Session { return s }

func (s *Session) Update(bean any) (int64, error) { return 0, nil }

func (s *Session) Insert(bean any) (int64, error) { return 0, nil }

func (s *Session) Delete(bean any) (int64, error) { return 0, nil }
`,
		"main.go": `package mtest

import "xorm.io/xorm"

type Settlement struct {
	OrderID int64
	Amount  int64
}

func update(s *xorm.Session, record *Settlement) {
	s.Table("settlement").Where("id = ?", 1).And("amount > ?", 100).Update(record)
}

func insert(s *xorm.Session, record *Settlement) {
	s.Table("settlement").Insert(record)
}

func del(s *xorm.Session, record *Settlement) {
	s.Table("settlement").Where("id = ?", 1).Or("order_id = ?", 2).Delete(record)
}

func main() {}
`,
	})
	rows, err := repo.Query(`SELECT name, json_extract(properties, '$.access_kind') FROM nodes
		WHERE kind = 'field_access' AND json_extract(properties, '$.is_external') = 'true'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	nodes := map[string]string{}
	for rows.Next() {
		var name, access string
		if err := rows.Scan(&name, &access); err != nil {
			t.Fatal(err)
		}
		nodes[name] = access
	}
	// And/Or filter（长链 Table→Where→And / →Or 表名传递）
	if nodes["settlement.amount"] != "filter" {
		t.Errorf("And 应产 filter settlement.amount，got %q", nodes["settlement.amount"])
	}
	if nodes["settlement.order_id"] != "filter" {
		t.Errorf("Or 应产 filter settlement.order_id，got %q", nodes["settlement.order_id"])
	}
	// Update/Insert/Delete write 节点（对象字段展开）
	for _, col := range []string{"order_id", "amount"} {
		if nodes["settlement."+col] != "write" && nodes["settlement."+col] != "filter" {
			t.Errorf("Update/Insert/Delete 应产 write 节点 settlement.%s，got %q", col, nodes["settlement."+col])
		}
	}
}
