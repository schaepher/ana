package ssa

import "testing"

// TestORMChainDAOSelfContained：⑦ 链式 ORM 自包含用例——自定义 DAO 封装
// （Model(&X{主键}).Where(...).Update("col", v)）经 field-summary.yaml 的
// orm_write 条目映射为 表.列 虚拟节点（不依赖真实 gorm 模块）。
func TestORMChainDAOSelfContained(t *testing.T) {
	repo := indexFixtureRepo(t, map[string]string{
		"field-summary.yaml": `summaries:
  - func: example.com/mtest.(DB).Update
    orm_write: true
    param_index: 1
`,
		"go.mod": "module example.com/mtest\n\ngo 1.21\n",
		"main.go": `package dao

type DB struct{}

type Session struct {
	ID     string
	Status string
}

func (d *DB) Model(v any) *DB { return d }

func (d *DB) Where(q string, v any) *DB { return d }

func (d *DB) Update(col string, v any) {}

// 自定义 DAO 封装：带条件的会话更新（仅含主键的范围对象 + 字符串列名）
func UpdateStatus(db *DB, id, status string) {
	db.Model(&Session{ID: id}).Where("id = ?", id).Update("status", status)
}

func main() {}
`,
	})
	funcID := "symbol:go:example.com/mtest:UpdateStatus"
	rows, err := repo.Query(`SELECT id, name FROM nodes WHERE kind='field_access'
			AND json_extract(properties, '$.func_id') = ?
			AND json_extract(properties, '$.type_string') = 'gorm'`, funcID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			t.Fatal(err)
		}
		if name == "session.status" {
			found = true
		}
	}
	if !found {
		t.Error("DAO 链式 Update 未生成 session.status 表.列 虚拟节点")
	}
}

// TestORMChainFormsSelfContained：⑪ ORM 链式形态覆盖——结构体 Updates
// 链式（Model().Where().Updates(&Y{})）与无 Model 的字符串列名 Update
// （Where().Update("col", v)——表名无法溯源时跳过而非报错）。
func TestORMChainFormsSelfContained(t *testing.T) {
	repo := indexFixtureRepo(t, map[string]string{
		"field-summary.yaml": `summaries:
  - func: example.com/mtest.(DB).Update
    orm_write: true
    param_index: 1
  - func: example.com/mtest.(DB).Updates
    orm_write: true
    param_index: 1
`,
		"go.mod": "module example.com/mtest\n\ngo 1.21\n",
		"main.go": `package ormf

type DB struct{}

type Session struct {
	ID     string
	Status string
}

func (d *DB) Model(v any) *DB { return d }

func (d *DB) Where(q string, v any) *DB { return d }

func (d *DB) Update(col string, v any) {}

func (d *DB) Updates(v any) {}

// 结构体 Updates 链式：Model(范围对象).Where(条件).Updates(结构体)
func UpdateAll(db *DB, id, status string) {
	db.Model(&Session{ID: id}).Where("id = ?", id).Updates(&Session{Status: status})
}

// 无 Model 的字符串列名 Update：receiver 链无结构体实参 → 表名不可推导，
// 应安全跳过（不产节点、不报错）
func UpdateRaw(db *DB, id, status string) {
	db.Where("id = ?", id).Update("status", status)
}

func main() {}
`,
	})
	rows, err := repo.Query(`SELECT name FROM nodes WHERE kind='field_access'
			AND json_extract(properties, '$.func_id') = 'symbol:go:example.com/mtest:UpdateAll'
			AND json_extract(properties, '$.type_string') = 'gorm'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	names := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names[name] = true
	}
	if !names["session.status"] {
		t.Errorf("Updates 结构体链式未生成 session.status: %v", names)
	}
	rows2, err := repo.Query(`SELECT count(*) FROM nodes WHERE kind='field_access'
			AND json_extract(properties, '$.func_id') = 'symbol:go:example.com/mtest:UpdateRaw'
			AND json_extract(properties, '$.type_string') = 'gorm'`)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if rows2.Next() {
		_ = rows2.Scan(&n)
	}
	rows2.Close()
	if n != 0 {
		t.Errorf("无 Model 的 Update 不应产表.列节点（表名不可推导），got %d", n)
	}
}

// TestORMUpdateRecordScopeSelfContained：⑪ ORM——session.Where(...)
// .Update(record, scope) 对象实参形态：record 变量 → 表.列 节点 +
// 对象兜底持久化边（summary_io）。
func TestORMUpdateRecordScopeSelfContained(t *testing.T) {
	repo := indexFixtureRepo(t, map[string]string{
		"field-summary.yaml": `summaries:
  - func: example.com/mtest.(Session).Update
    orm_write: true
    param_index: 1
`,
		"go.mod": "module example.com/mtest\n\ngo 1.21\n",
		"main.go": `package orms

type Session struct{}

type Record struct {
	FinalFee float64
}

func (s *Session) Where(q string, v any) *Session { return s }

func (s *Session) Update(record *Record, scope any) {}

// DAO：带条件的会话更新（对象实参 + 附加条件参数）
func UpdateFee(s *Session, record *Record) {
	s.Where("state = ?", "active").Update(record, nil)
}

func main() {}
`,
	})
	funcID := "symbol:go:example.com/mtest:UpdateFee"
	rows, err := repo.Query(`SELECT id, name FROM nodes WHERE kind='field_access'
			AND json_extract(properties, '$.func_id') = ? AND json_extract(properties, '$.type_string') = 'gorm'`, funcID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	names := map[string]bool{}
	ids := map[string]bool{}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			t.Fatal(err)
		}
		names[name] = true
		ids[id] = true
	}
	if !names["record.final_fee"] {
		t.Errorf("Update(record, scope) 未生成 record.final_fee 表.列 节点: %v", names)
	}
	rows2, err := repo.Query(`SELECT count(*) FROM edges WHERE kind = 'summary_io' AND target_id = ?`,
		funcID+"#ext.gorm.record.final_fee.write@0")
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if rows2.Next() {
		_ = rows2.Scan(&n)
	}
	rows2.Close()
	if n == 0 {

		for id := range ids {
			var cnt int
			rows3, err := repo.Query(`SELECT count(*) FROM edges WHERE kind='summary_io' AND target_id = ?`, id)
			if err != nil {
				t.Fatal(err)
			}
			if rows3.Next() {
				_ = rows3.Scan(&cnt)
			}
			rows3.Close()
			if cnt > 0 {
				n = cnt
			}
		}
	}
	if n == 0 {
		t.Error("Update(record, scope) 缺 summary_io 持久化边（对象值 → 表.列 节点）")
	}
}
