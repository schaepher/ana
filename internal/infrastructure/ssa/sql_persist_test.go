package ssa

import (
	"strings"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

// findVirtualNode 按 Name 前缀查找虚拟节点（SQL 持久化节点 users.name）。
func findVirtualNode(t *testing.T, nodes []*domain.CodeEntity, funcID, namePrefix string) *domain.CodeEntity {
	t.Helper()
	for _, n := range nodes {
		if n.Kind != domain.KindFieldAccess || n.Property("func_id") != funcID {
			continue
		}
		if strings.HasPrefix(n.Name, namePrefix) {
			return n
		}
	}
	t.Fatalf("虚拟节点缺失: func=%s name~%s", funcID, namePrefix)
	return nil
}

// TestSQLInsertPersist：db.Exec("INSERT INTO users(name) VALUES(?)", u.Name)
// → 虚拟节点 users.name + summary_io 边（字段值 → 虚拟节点）（Q97 持久化映射）。
func TestSQLInsertPersist(t *testing.T) {
	nodes, facts, _ := indexFixtureFull(t, map[string]string{
		"go.mod":  moduleGoMod,
		"main.go": `package m

import "database/sql"

type User struct {
	Name string
}

func save(db *sql.DB, u *User) {
	db.Exec("INSERT INTO users(name) VALUES(?)", u.Name)
}
`,
	})
	funcID := "symbol:go:example.com/mtest:save"
	vnode := findVirtualNode(t, nodes, funcID, "users.name")
	// summary_io 边：u.Name 值 → 虚拟节点（query table 写入方定位）
	found := false
	for _, f := range facts {
		if f.Kind == domain.FactSummaryIO && string(f.TargetID) == string(vnode.ID) {
			found = true
			ln := 0.0
			switch v := f.Metadata["line_num"].(type) {
			case float64:
				ln = v
			case int:
				ln = float64(v)
			}
			if ln != 10 {
				t.Errorf("summary_io line_num = %v, want 10（Exec 调用行）", f.Metadata["line_num"])
			}
		}
	}
	if !found {
		t.Errorf("summary_io 边缺失（u.Name → users.name）")
	}
}

// TestSQLSelectRead：P0-2——SELECT 读路径：QueryRow 的 SQL 解析出列 →
// read 虚拟节点（表.列）+ 读边（虚拟节点 → 读出的 Row 值）。
// query table 读取方闭环的数据基础。
func TestSQLSelectRead(t *testing.T) {
	nodes, facts, _ := indexFixtureFull(t, map[string]string{
		"go.mod":  moduleGoMod,
		"main.go": `package m

import "database/sql"

type User struct {
	Name string
	Age  int
}

func get(db *sql.DB, id int) *User {
	row := db.QueryRow("SELECT name, age FROM users WHERE id = ?", id)
	var u User
	row.Scan(&u.Name, &u.Age)
	return &u
}
`,
	})
	funcID := "symbol:go:example.com/mtest:get"
	// 每列一个 read 虚拟节点（access_kind=read）
	nameNode := findVirtualNode(t, nodes, funcID, "users.name")
	if nameNode.Property("access_kind") != "read" {
		t.Errorf("users.name access_kind = %q, want read", nameNode.Property("access_kind"))
	}
	ageNode := findVirtualNode(t, nodes, funcID, "users.age")
	if ageNode.Property("access_kind") != "read" {
		t.Errorf("users.age access_kind = %q, want read", ageNode.Property("access_kind"))
	}
	// 读边：虚拟节点 → 读出的 Row 值（source=节点, kind=summary_io）
	seen := map[string]bool{}
	for _, f := range facts {
		if f.Kind == domain.FactSummaryIO && string(f.SourceID) == string(nameNode.ID) {
			seen["name->row"] = true
		}
		if f.Kind == domain.FactSummaryIO && string(f.SourceID) == string(ageNode.ID) {
			seen["age->row"] = true
		}
	}
	if !seen["name->row"] || !seen["age->row"] {
		t.Errorf("读边缺失（虚拟节点 → Row 值）: %v", seen)
	}
}

// TestSQLSelectStar：SELECT * 无列 → 表级 read 虚拟节点（Name=表）。
func TestSQLSelectStar(t *testing.T) {
	nodes, _, _ := indexFixtureFull(t, map[string]string{
		"go.mod":  moduleGoMod,
		"main.go": `package m

import "database/sql"

func list(db *sql.DB) {
	rows, _ := db.Query("SELECT * FROM users")
	rows.Close()
}
`,
	})
	vnode := findVirtualNode(t, nodes, "symbol:go:example.com/mtest:list", "users")
	if vnode.Property("access_kind") != "read" {
		t.Errorf("users access_kind = %q, want read", vnode.Property("access_kind"))
	}
}

// TestSQLUpdatePersist：UPDATE 的表列提取。
func TestSQLUpdatePersist(t *testing.T) {
	nodes, _, _ := indexFixtureFull(t, map[string]string{
		"go.mod":  moduleGoMod,
		"main.go": `package m

import "database/sql"

type User struct {
	Age int
}

func update(db *sql.DB, u *User) {
	db.Exec("UPDATE users SET age=? WHERE id=1", u.Age)
}
`,
	})
	findVirtualNode(t, nodes, "symbol:go:example.com/mtest:update", "users.age")
}

// TestSQLTxBoundary：事务边界识别（Begin/Commit → 事务虚拟节点）（Q97）。
func TestSQLTxBoundary(t *testing.T) {
	nodes, _, _ := indexFixtureFull(t, map[string]string{
		"go.mod":  moduleGoMod,
		"main.go": `package m

import "database/sql"

func tx(db *sql.DB) {
	t, _ := db.Begin()
	t.Commit()
}
`,
	})
	funcID := "symbol:go:example.com/mtest:tx"
	findVirtualNode(t, nodes, funcID, "sql.tx.begin")
	findVirtualNode(t, nodes, funcID, "sql.tx.commit")
}
