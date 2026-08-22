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
		table, cols, whereCols, _ := parseSQLStmt(c.sql)
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

// TestParseSQLJoinPairs：Q239——JOIN ON 键对提取（sqlJoinPair）。
// INNER/LEFT JOIN + ON 等值键对（别名映射）；CROSS JOIN 无 ON / 逗号
// 连接 / 子查询 JOIN 放弃（无键信号）。
func TestParseSQLJoinPairs(t *testing.T) {
	cases := []struct {
		sql   string
		pairs []sqlJoinPair
	}{
		// go2o 真实形态：INNER JOIN + ON 键对（a.code = s.city_code）
		{"SELECT s.id,a.name FROM sys_sub_station s INNER JOIN sys_district a ON a.code = s.city_code",
			[]sqlJoinPair{{"sys_district", "code", "sys_sub_station", "city_code"}}},
		{"SELECT s.id FROM sys_sub_station s LEFT JOIN sys_district a ON a.code = s.city_code",
			[]sqlJoinPair{{"sys_district", "code", "sys_sub_station", "city_code"}}},
		{"SELECT it.id FROM sale_sub_item it INNER JOIN sale_normal_order ord ON ord.id = it.order_id INNER JOIN mm_member m ON m.member_id = ord.buyer_id",
			[]sqlJoinPair{{"sale_normal_order", "id", "sale_sub_item", "order_id"},
				{"mm_member", "member_id", "sale_normal_order", "buyer_id"}}},
		// 无别名 JOIN（users.id = orders.uid）
		{"SELECT u.name FROM users u JOIN orders ON u.id = orders.uid",
			[]sqlJoinPair{{"users", "id", "orders", "uid"}}},
		// AND 多键对（SQL 书写顺序：左 = From）
		{"SELECT * FROM a JOIN b ON a.x = b.y AND a.m = b.n",
			[]sqlJoinPair{{"a", "x", "b", "y"}, {"a", "m", "b", "n"}}},
		// CROSS JOIN 无 ON → 放弃
		{"SELECT * FROM a CROSS JOIN b", nil},
		// 逗号连接 → 放弃
		{"SELECT * FROM a, b WHERE a.id = b.a_id", nil},
		// 子查询 JOIN → 放弃
		{"SELECT * FROM a JOIN (SELECT id FROM b) x ON x.id = a.b_id", nil},
		// 无 JOIN
		{"SELECT name FROM users WHERE id = ?", nil},
	}
	for _, c := range cases {
		_, _, _, pairs := parseSQLStmt(c.sql)
		if len(pairs) != len(c.pairs) {
			t.Errorf("%q: joinPairs = %+v, want %+v", c.sql, pairs, c.pairs)
			continue
		}
		for i, want := range c.pairs {
			if pairs[i] != want {
				t.Errorf("%q: pair[%d] = %+v, want %+v", c.sql, i, pairs[i], want)
			}
		}
	}
}

// TestSQLJoinEmit：Q239——JOIN ON 键对产出 origin=join 的虚拟节点与
// data_flows_to 边（from 表列 read → to 表列 filter）。go2o 真实形态。
func TestSQLJoinEmit(t *testing.T) {
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

func station(c Conn) {
	c.Query("SELECT s.id,a.name FROM sys_sub_station s INNER JOIN sys_district a ON a.code = s.city_code WHERE a.parent IN ($1)", func(r *Rows) {})
}
`,
	})
	// to 侧 filter 节点（origin=join）
	var filterNode *domain.CodeEntity
	for _, n := range nodes {
		if n.Kind != domain.KindFieldAccess {
			continue
		}
		if n.Name == "sys_sub_station.city_code" && n.Property("access_kind") == "filter" {
			filterNode = n
			if n.Property("origin") != "join" {
				t.Errorf("JOIN filter 节点应标 origin=join，got %v", n.Property("origin"))
			}
		}
	}
	if filterNode == nil {
		t.Fatalf("未产出 JOIN to 侧 filter 节点（sys_sub_station.city_code filter）")
	}
	// from 侧 read 节点（origin=join）
	fromID := ""
	for _, n := range nodes {
		if n.Kind == domain.KindFieldAccess && n.Name == "sys_district.code" &&
			n.Property("access_kind") == "read" {
			fromID = string(n.ID)
			if n.Property("origin") != "join" {
				t.Errorf("JOIN read 节点应标 origin=join，got %v", n.Property("origin"))
			}
		}
	}
	if fromID == "" {
		t.Fatalf("未产出 JOIN from 侧 read 节点（sys_district.code read）")
	}
	// data_flows_to 边：from read → to filter
	found := false
	for _, f := range facts {
		if string(f.SourceID) == fromID && string(f.TargetID) == string(filterNode.ID) &&
			f.Kind == domain.FactDataFlowsTo {
			found = true
		}
	}
	if !found {
		t.Errorf("缺 JOIN 值流边（sys_district.code → sys_sub_station.city_code）")
	}
}

// TestParseSQLSubqueryParen：Q239——子查询右括号不得并进表名/列名
// （(SELECT COUNT(1) FROM mm_member) → mm_member；Q220a 同类 where 串误解析）。
func TestParseSQLSubqueryParen(t *testing.T) {
	cases := []struct {
		sql       string
		table     string
		whereCols []string
	}{
		{"SELECT (SELECT COUNT(1) FROM mm_member) as totalMembers", "mm_member", nil},
		{"SELECT (SELECT COUNT(1) FROM mm_member WHERE status = ?) as n", "mm_member", []string{"status"}},
		{"SELECT * FROM mm_member) WHERE status = ?", "mm_member", []string{"status"}},
		// 子查询 FROM（派生表）按 Q6 精神放弃——不产出假表
		{"SELECT * FROM (SELECT id FROM b) x WHERE x.id = ?", "", nil},
	}
	for _, c := range cases {
		table, _, whereCols, _ := parseSQLStmt(c.sql)
		if table != c.table {
			t.Errorf("%q: table = %q, want %q", c.sql, table, c.table)
		}
		if len(whereCols) != len(c.whereCols) {
			t.Errorf("%q: whereCols = %v, want %v", c.sql, whereCols, c.whereCols)
			continue
		}
		for i, w := range c.whereCols {
			if whereCols[i] != w {
				t.Errorf("%q: whereCols[%d] = %q, want %q", c.sql, i, whereCols[i], w)
			}
		}
	}
}

// TestSQLDynamicSprintf：Q239——fmt.Sprintf 动态拼接 SQL 还原：
// `...WHERE %s` 的 %s ← where 实参（常量 / 嵌套 Sprintf / 跨函数参数）
// → 还原后走统一解析（表/where 列）。go2o rbac_dao_impl 真实形态。
func TestSQLDynamicSprintf(t *testing.T) {
	nodes, _ := indexFixture(t, map[string]string{
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

import "fmt"

type Rows struct{}

type Conn interface {
	Query(sql string, cb func(*Rows))
}

func pagingPermRole(c Conn, begin, end int, where string) {
	s := fmt.Sprintf("SELECT COUNT(1) FROM rbac_role WHERE %s", where)
	c.Query(s, func(r *Rows) {})
}

func caller(c Conn) {
	pagingPermRole(c, 0, 10, "role_id = ?")
}

func main() {}
`,
	})
	var tableNode, filterNode *domain.CodeEntity
	for _, n := range nodes {
		if n.Kind != domain.KindFieldAccess {
			continue
		}
		if n.Name == "rbac_role" || n.Name == "rbac_role.role_id" {
			if n.Name == "rbac_role" && n.Property("access_kind") == "read" {
				tableNode = n
			}
			if n.Name == "rbac_role.role_id" && n.Property("access_kind") == "filter" {
				filterNode = n
			}
		}
	}
	if tableNode == nil {
		t.Errorf("动态 SQL 未还原表 rbac_role（Sprintf %%s 未解析）")
	}
	if filterNode == nil {
		t.Errorf("动态 SQL 未还原 where 列 role_id（跨函数参数追溯失败）")
	}
}
