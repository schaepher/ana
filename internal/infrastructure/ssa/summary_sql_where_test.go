package ssa

import (
	"reflect"
	"testing"
)

// TestWhereColsOf Q220：where 条件串列名提取——AND/OR 拆分大小写不敏感
// （go2o 实测 lowercase " and " 整串未拆分 → 列名含 " = ? and ..." 垃圾）、
// 尾部子句清理（LIMIT/OFFSET/ORDER BY）、多操作符（<>/LIKE/BETWEEN/
// IS NULL/IN）、$N 与 ? 占位符、裸列名（In("amount") 形态）。
func TestWhereColsOf(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		// Q220 回归：lowercase " and " 必须拆分（此前整串当一个列名）
		{"lowercase and", "user_type = ? and user_id = ?", []string{"user_type", "user_id"}},
		{"lowercase and + $N", "ad_id=$1 and id=$2", []string{"ad_id", "id"}},
		{"triple cond + <>", "tpl_code=? and tpl_type=? and id <> ?", []string{"tpl_code", "tpl_type", "id"}},
		{"literal operand", "parent = 0 and code = ?", []string{"parent", "code"}},
		{"uppercase AND", "id= $1 AND merchant_id= $2", []string{"id", "merchant_id"}},
		{"OR", "code = ? OR name = ?", []string{"code", "name"}},
		{"multi-line", "buyer_id=? \n AND order_type=? \n AND status = ?", []string{"buyer_id", "order_type", "status"}},
		// 操作符形态
		{"LIKE", "name LIKE $1", []string{"name"}},
		{"BETWEEN", "unix_date BETWEEN $2", []string{"unix_date"}},
		{"IS NULL", "b.id is null", []string{"b.id"}},
		{"IN paren", "col IN (?, ?, ?)", []string{"col"}},
		{"gt space", "value> $2", []string{"value"}},
		// 尾部子句（此前 LIMIT/OFFSET 残留进列名）
		{"LIMIT", "alias = $1 LIMIT 1", []string{"alias"}},
		{"ORDER BY LIMIT OFFSET", "nav_group = $2  order by id ASC LIMIT $3 OFFSET $4", []string{"nav_group"}},
		// 既有形态回归
		{"single =", "order_id = ?", []string{"order_id"}},
		{"bare col (In 形态)", "amount", []string{"amount"}},
		{"empty", "", nil},
	}
	for _, c := range cases {
		got := whereColsOf(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: whereColsOf(%q) = %v, want %v", c.name, c.in, got, c.want)
		}
	}
}
