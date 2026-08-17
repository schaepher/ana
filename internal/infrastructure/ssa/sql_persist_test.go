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
		"go.mod": moduleGoMod,
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
		"go.mod": moduleGoMod,
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

// TestSQLWhereFilter：表关联——SELECT 的 WHERE 值实参按 ? 顺序映射过滤列：
// 产 filter 虚拟节点（table_b.y）+ 边（值 → 节点），数据流链
// A.X.read → ... → B.Y.filter 的基础。
func TestSQLWhereFilter(t *testing.T) {
	nodes, facts, _ := indexFixtureFull(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

import "database/sql"

func find(db *sql.DB, id int) string {
	row := db.QueryRow("SELECT x FROM table_a WHERE id = ?", id)
	var x string
	row.Scan(&x)
	row2 := db.QueryRow("SELECT * FROM table_b WHERE y = ?", x)
	row2.Scan(&x)
	return x
}
`,
	})
	funcID := "symbol:go:example.com/mtest:find"
	// WHERE 过滤列虚拟节点：table_b.y（access=filter）
	yNode := findVirtualNode(t, nodes, funcID, "table_b.y")
	if yNode.Property("access_kind") != "filter" {
		t.Errorf("table_b.y access_kind = %q, want filter", yNode.Property("access_kind"))
	}
	// 值 → 过滤列边（x 的值流入 table_b.y）
	found := false
	for _, f := range facts {
		if f.Kind == domain.FactSummaryIO && string(f.TargetID) == string(yNode.ID) {
			found = true
		}
	}
	if !found {
		t.Error("WHERE 值 → 过滤列 summary_io 边缺失（表关联数据链断点）")
	}
	// table_a.id 的 WHERE 参数（字面量 id）也产过滤节点
	findVirtualNode(t, nodes, funcID, "table_a.id")
}

// TestSQLScanOutFlow：表关联链贯通——row.Scan(&x) 后 x 值流入第二次
// 查询的 WHERE 过滤列：Scan 摘要（接收者 → out 实参）使
// table_a.x.read → ... → table_b.y.filter 数据流链完整。
func TestSQLScanOutFlow(t *testing.T) {
	nodes, facts, _ := indexFixtureFull(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

import "database/sql"

func find(db *sql.DB, id int) string {
	row := db.QueryRow("SELECT x FROM table_a WHERE id = ?", id)
	var x string
	row.Scan(&x)
	row2 := db.QueryRow("SELECT * FROM table_b WHERE y = ?", x)
	row2.Scan(&x)
	return x
}
`,
	})
	funcID := "symbol:go:example.com/mtest:find"
	// ① x 局部变量节点（funcID#x）存在——Scan 摘要 emit out 实参（alloc）
	var xID domain.CanonicalID
	for _, n := range nodes {
		if string(n.ID) == funcID+"#x" {
			xID = n.ID
		}
	}
	if xID == "" {
		t.Fatal("x 局部变量节点缺失（Scan 摘要未 emit out 实参）")
	}
	// ② Scan 边：row 值（table_a.x.read 出边 target）→ x（data_flows_to）
	rowID := ""
	for _, f := range facts {
		if f.Kind == domain.FactSummaryIO && strings.HasSuffix(string(f.SourceID), "#ext.sql.table_a.x.read@6") {
			rowID = string(f.TargetID)
		}
	}
	if rowID == "" {
		t.Fatal("table_a.x.read → row 值边缺失")
	}
	scanEdge := false
	for _, f := range facts {
		if f.Kind == domain.FactDataFlowsTo && string(f.SourceID) == rowID && f.TargetID == xID {
			scanEdge = true
		}
	}
	if !scanEdge {
		t.Error("Scan 边缺失（row 值 → x 变量）")
	}
	// ③ x（load 归一到 alloc ID）→ table_b.y.filter：链贯通
	toFilter := false
	for _, f := range facts {
		if f.Kind == domain.FactSummaryIO && string(f.SourceID) == string(xID) {
			toFilter = true
		}
	}
	if !toFilter {
		t.Error("x → table_b.y.filter 边缺失（x 使用处流入过滤列）")
	}
}

// TestSQLSelectStar：SELECT * 无列 → 表级 read 虚拟节点（Name=表）。
func TestSQLSelectStar(t *testing.T) {
	nodes, _, _ := indexFixtureFull(t, map[string]string{
		"go.mod": moduleGoMod,
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
		"go.mod": moduleGoMod,
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
		"go.mod": moduleGoMod,
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
