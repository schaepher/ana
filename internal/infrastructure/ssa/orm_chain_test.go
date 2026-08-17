package ssa

import (
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

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

	found := false
	for _, f := range facts {
		if f.Kind == domain.FactSummaryIO && string(f.TargetID) == string(vCol.ID) {
			found = true
		}
	}
	if !found {
		t.Errorf("链式 Update 缺 summary_io 边（值 → session.status）")
	}

	for _, n := range nodes {
		if n.Kind == domain.KindFieldAccess && n.Property("func_id") == funcID &&
			n.Property("type_string") == "gorm" && n.Name != "session.status" {
			t.Errorf("Model 范围对象不应产生表.列节点: %s", n.Name)
		}
	}
}

// TestORMWhereFilter：GORM Where("session_id = ?", v) 字符串列名形态——
// 列名剥离 " = ?" 后缀产 filter 虚拟节点（表关联键：值 → 过滤列）。
// 用本地模拟 DB 类型（链式 Model/Where/Count，同 gorm 形态）。
func TestORMWhereFilter(t *testing.T) {
	nodes, facts, _ := indexFixtureFull(t, map[string]string{
		"go.mod": moduleGoMod,
		"field-summary.yaml": `summaries:
  - func: example.com/mtest.(DB).Where
    orm_write: true
    param_index: 1
`,
		"main.go": `package m

type DB struct{}

func (db *DB) Model(v any) *DB { return db }

func (db *DB) Where(cond string, args ...any) *DB { return db }

func (db *DB) Count(c *int64) {}

type ChatMessage struct {
	SessionID string
	Content   string
}

type Session struct {
	ID string
}

func countBySession(db *DB, s *Session) int {
	var count int64
	db.Model(&ChatMessage{}).Where("session_id = ?", s.ID).Count(&count)
	return int(count)
}
`,
	})
	funcID := "symbol:go:example.com/mtest:countBySession"
	vnode := findVirtualNode(t, nodes, funcID, "chat_message.session_id")
	if vnode.Property("access_kind") != "filter" {
		t.Errorf("chat_message.session_id access_kind = %q, want filter（Where 过滤列）", vnode.Property("access_kind"))
	}

	found := false
	for _, f := range facts {
		if f.Kind == domain.FactSummaryIO && string(f.TargetID) == string(vnode.ID) {
			found = true
		}
	}
	if !found {
		t.Error("Where 值 → filter 节点边缺失（表关联键链断点）")
	}
}

// TestORMReadFind：GORM 读路径——Find(&sessions) 对象读出产 read
// 虚拟节点（表.列）+ 边（读出值 → 对象）；读出的 s.ID 作为 Where 实参
// 时，键关联链贯通（session.id.read → ... → chat_message.session_id.filter）。
func TestORMReadFind(t *testing.T) {
	nodes, facts, _ := indexFixtureFull(t, map[string]string{
		"go.mod": moduleGoMod,
		"field-summary.yaml": `summaries:
  - func: example.com/mtest.(DB).Find
    orm_read: true
    param_index: 1
  - func: example.com/mtest.(DB).Where
    orm_write: true
    param_index: 1
  - func: example.com/mtest.(DB).Count
    orm_read: true
    param_index: 1
`,
		"main.go": `package m

type DB struct{}

func (db *DB) Model(v any) *DB { return db }

func (db *DB) Find(out any) *DB { return db }

func (db *DB) Where(cond string, args ...any) *DB { return db }

func (db *DB) Count(c *int64) {}

type Session struct {
	ID    string
	Title string
}

type ChatMessage struct {
	SessionID string
}

func list(db *DB) int {
	var sessions []Session
	db.Find(&sessions)
	count := 0
	for _, s := range sessions {
		var n int64
		db.Model(&ChatMessage{}).Where("session_id = ?", s.ID).Count(&n)
		count += int(n)
	}
	return count
}
`,
	})
	funcID := "symbol:go:example.com/mtest:list"

	idNode := findVirtualNode(t, nodes, funcID, "session.id")
	if idNode.Property("access_kind") != "read" {
		t.Errorf("session.id access_kind = %q, want read（Find 读出）", idNode.Property("access_kind"))
	}
	findVirtualNode(t, nodes, funcID, "session.title")

	found := false
	for _, f := range facts {
		if f.Kind == domain.FactSummaryIO && string(f.SourceID) == string(idNode.ID) {
			found = true
		}
	}
	if !found {
		t.Error("Find 读边缺失（read 节点 → 对象值）")
	}

	filterNode := findVirtualNode(t, nodes, funcID, "chat_message.session_id")
	if filterNode.Property("access_kind") != "filter" {
		t.Errorf("chat_message.session_id access_kind = %q, want filter", filterNode.Property("access_kind"))
	}

	through := false
	for _, f := range facts {
		if f.Kind == domain.FactSummaryIO && string(f.TargetID) == string(filterNode.ID) {
			through = true
		}
	}
	if !through {
		t.Error("s.ID → chat_message.session_id.filter 边缺失（键关联链断）")
	}
}
