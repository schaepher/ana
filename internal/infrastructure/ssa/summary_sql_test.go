package ssa

import (
	"strings"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

// TestParseSQLStmt：⑬ 猎 bug——SQL 语句启发式解析形态矩阵
// （parseSQLStmt 此前无直接单测；曾有切片 panic 历史）。
func TestParseSQLStmt(t *testing.T) {
	cases := []struct {
		sql       string
		table     string
		cols      []string
		whereCols []string
	}{
		{"INSERT INTO users(name, email) VALUES(?, ?)", "users", []string{"name", "email"}, nil},
		{"INSERT INTO users VALUES(?, ?)", "users", nil, nil},
		{"INSERT INTO `users`(`name`) VALUES(?)", "users", []string{"name"}, nil},
		{"insert into users (name) values (?)", "users", []string{"name"}, nil},
		{"UPDATE users SET name=?, email=? WHERE id = ?", "users", []string{"name", "email"}, []string{"id"}},
		{"UPDATE users SET name = ?", "users", []string{"name"}, nil},
		{"DELETE FROM users WHERE id = ?", "users", nil, []string{"id"}},
		{"SELECT name FROM users WHERE id = ?", "users", []string{"name"}, []string{"id"}},
		{"SELECT u.name FROM users u JOIN orders o ON u.id = o.uid", "users", []string{"name"}, nil},
		{"SELECT * FROM users", "users", nil, nil},

		{"SELECT x FROM table_a WHERE id = ?", "table_a", []string{"x"}, []string{"id"}},
		{"SELECT * FROM table_b WHERE y = ?", "table_b", nil, []string{"y"}},
		{"SELECT * FROM table_b WHERE a.y = ? AND z = ?", "table_b", nil, []string{"y", "z"}},
		{"SELECT * FROM t WHERE id = ? ORDER BY id", "t", nil, []string{"id"}},
		{"UPDATE users SET name = ? WHERE id = ?", "users", []string{"name"}, []string{"id"}},
		{"not sql at all", "", nil, nil},
		{"", "", nil, nil},
	}
	for _, c := range cases {
		table, cols, whereCols := parseSQLStmt(c.sql)
		if table != c.table {
			t.Errorf("parseSQLStmt(%q) table = %q, want %q", c.sql, table, c.table)
		}
		if len(cols) != len(c.cols) {
			t.Errorf("parseSQLStmt(%q) cols = %v, want %v", c.sql, cols, c.cols)
			continue
		}
		for i := range cols {
			if cols[i] != c.cols[i] {
				t.Errorf("parseSQLStmt(%q) cols[%d] = %q, want %q", c.sql, i, cols[i], c.cols[i])
			}
		}
		if len(whereCols) != len(c.whereCols) {
			t.Errorf("parseSQLStmt(%q) whereCols = %v, want %v", c.sql, whereCols, c.whereCols)
			continue
		}
		for i := range whereCols {
			if whereCols[i] != c.whereCols[i] {
				t.Errorf("parseSQLStmt(%q) whereCols[%d] = %q, want %q", c.sql, i, whereCols[i], c.whereCols[i])
			}
		}
	}
}

// TestWhereColDollar：Q158——$N 占位符（PostgreSQL 风格）的 WHERE 过滤
// 列提取（go2o memberRepo 用 gof Connector 的 "level= $1" 形态）。
func TestWhereColDollar(t *testing.T) {
	nodes, _, _ := indexFixtureFull(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

import "database/sql"

func f(db *sql.DB) {
	db.QueryRow("SELECT id FROM mm_member WHERE level= $1", 2)
}
`,
	})
	found := false
	for _, n := range nodes {
		if n.Kind == domain.KindFieldAccess && n.Property("type_string") == "sql" &&
			n.Name == "mm_member.level" && n.Property("access_kind") == "filter" {
			found = true
		}
	}
	if !found {
		t.Error("$N 占位符的 WHERE 过滤列未提取（mm_member.level filter 缺失）")
	}
}

// TestSQLQueryCallbackClosure：Q201——Query(sql, callback) 形态的读出值
// 进入回调闭包形参（rows），read 节点边指向闭包形参（归属父函数）
// 而非返回值（返回值与回调形参静态无连接，链断在闭包）。
// 闭包内 rows.Scan(&id) 后 id 参与后续值流，跨函数链贯通
// （go2o 实测：settleRiseData 的 pf_riseinfo.person_id 读出值因闭包
// 断链，person_id → usr_person 键关联缺失）。
func TestSQLQueryCallbackClosure(t *testing.T) {
	nodes, facts := indexFixture(t, map[string]string{
		"go.mod": moduleGoMod,
		"field-summary.yaml": `
summaries:
  - iface: example.com/mtest.Conn
    method: Query
    kind: sql
    sql_write: false
    where_arg: 0
`,
		"main.go": `package m

type Rows struct{}

type Conn interface {
	Query(sql string, cb func(*Rows))
}

func settle(c Conn) {
	var ids []int
	c.Query("SELECT id FROM users WHERE name = ?", func(r *Rows) {
		var id int
		_ = r
		ids = append(ids, id)
	})
	_ = ids
}
`,
	})
	var readID string
	for _, n := range nodes {
		if n.Kind == domain.KindFieldAccess && n.Name == "users.id" &&
			n.Property("access_kind") == "read" {
			readID = string(n.ID)
		}
	}
	if readID == "" {
		t.Fatal("users.id read 节点缺失（SQL 摘要未触发）")
	}
	var outs []string
	for _, f := range facts {
		if f.SourceID == domain.CanonicalID(readID) && f.Kind == domain.FactSummaryIO {
			outs = append(outs, string(f.TargetID))
		}
	}
	found := false
	for _, tgt := range outs {
		if strings.Contains(tgt, "settle#param.r") {
			found = true
		}
	}
	if !found {
		t.Errorf("users.id.read 出边应指向回调闭包形参 settle#param.r（非返回值），got %v", outs)
	}
}
