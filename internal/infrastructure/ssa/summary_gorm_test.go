package ssa

import (
	"testing"
)

// TestGORMSummarySpecsCoverage：GORM spec 覆盖清单——已支持的 API 断言
// 存在；未支持的输出到测试日志（对照 gorm 官方 API 检查缺口用）。
// 覆盖清单同时是 summary_gorm.go 的注释来源，改清单须同步。
func TestGORMSummarySpecsCoverage(t *testing.T) {
	specs := gormSummarySpecs()
	// 已支持（断言在册）
	have := map[string]bool{}
	for k := range specs {
		have[k] = true
	}
	for _, fn := range []string{"Create", "Save", "Updates", "Update", "Delete",
		"Where", "Not", "Or", "Find", "First", "Take", "Last", "Scan",
		"Exec", "Raw", "Begin"} {
		if !have["gorm.io/gorm.(DB)."+fn] {
			t.Errorf("GORM %s 应有 spec（覆盖清单见 summary_gorm.go）", fn)
		}
	}
	// (Tx) 事务边界
	for _, fn := range []string{"Commit", "Rollback"} {
		if !have["gorm.io/gorm.(Tx)."+fn] {
			t.Errorf("GORM (Tx).%s 应有 spec", fn)
		}
	}
	// 未支持清单（人工对照 gorm 官方 API；t.Log 不失败）——
	// 非数据流/无实体形态不补 spec（原因见 summary_gorm.go 注释）
	var missing []string
	for _, fn := range []string{"Table", "Model", "Select", "Omit", "Joins",
		"Preload", "Pluck", "Count", "Sum", "Row", "Rows", "Transaction",
		"AutoMigrate", "Debug", "Session", "Clauses", "UpdateColumn", "Updates",
		"Association", "Group", "Having", "Order", "Limit", "Offset", "Distinct",
		"Scopes", "Unscoped"} {
		if !have["gorm.io/gorm.(DB)."+fn] {
			missing = append(missing, fn)
		}
	}
	t.Logf("GORM 未支持 API（非数据流/无实体，不补 spec）：%v", missing)
}

// TestGORMSummarySpecsShape：spec 形态——ORM 写带 ORMWrite，读带 ORMRead，
// Where 是 filter 字符串形态（ParamIndex=1 实参字符串）。
func TestGORMSummarySpecsShape(t *testing.T) {
	specs := gormSummarySpecs()
	if s := specs["gorm.io/gorm.(DB).Create"]; !s.ORMWrite {
		t.Errorf("Create 应为 ORMWrite")
	}
	if s := specs["gorm.io/gorm.(DB).Find"]; !s.ORMRead {
		t.Errorf("Find 应为 ORMRead")
	}
	if s := specs["gorm.io/gorm.(DB).Where"]; s.ParamIndex != 1 || !s.ORMWrite {
		t.Errorf("Where 应为 ParamIndex=1 字符串过滤形态，got %+v", s)
	}
	if s := specs["gorm.io/gorm.(DB).Not"]; s.ParamIndex != 1 || !s.ORMWrite {
		t.Errorf("Not 应为 ParamIndex=1 字符串过滤形态，got %+v", s)
	}
	if s := specs["gorm.io/gorm.(DB).Scan"]; !s.ORMRead {
		t.Errorf("Scan 应为 ORMRead（同 Find 形态），got %+v", s)
	}
	if s := specs["gorm.io/gorm.(DB).Exec"]; !s.SQLStmt || !s.SQLWrite {
		t.Errorf("Exec 应为 SQLStmt+SQLWrite，got %+v", s)
	}
	if s := specs["gorm.io/gorm.(DB).Raw"]; !s.SQLStmt || s.SQLWrite {
		t.Errorf("Raw 应为 SQLStmt 读（SQLWrite=false），got %+v", s)
	}
	if s := specs["gorm.io/gorm.(DB).Begin"]; s.TxBoundary != "begin" {
		t.Errorf("Begin 应为 TxBoundary=begin，got %+v", s)
	}
}

// TestGormModelTypeArgTableName：Q239——Model(类型实参) 表名经 TableName()
// 摘要解析（go2o transaction_manager.go 真实形态：Model(wallet.WalletLog{})
// 应 wal_wallet_log 而非错误推断）。
func TestGormModelTypeArgTableName(t *testing.T) {
	repo := indexFixtureRepo(t, map[string]string{
		"go.mod": "module example.com/mtest\n\ngo 1.21\n",
		"field-summary.yaml": `summaries:
  - func: example.com/mtest.(DB).Where
    orm_write: true
    param_index: 1
  - func: example.com/mtest.(DB).Find
    orm_read: true
    param_index: 1
`,
		"main.go": `package dao

type DB struct{}

func (d *DB) Model(v any) *DB { return d }

func (d *DB) Select(s string) *DB { return d }

func (d *DB) Where(q string, v any) *DB { return d }

func (d *DB) Find(v any) {}

type WalletLog struct {
	ID int
}

func (w WalletLog) TableName() string { return "wal_wallet_log" }

func queryBill(db *DB) {
	var total WalletLog
	db.Model(&WalletLog{}).Select("count(*) as transaction_count").Where("wallet_id = ?", 1).Find(&total)
}

func main() {}
`,
	})
	rows, err := repo.Query(`SELECT DISTINCT name FROM nodes WHERE kind='field_access'
		AND name LIKE 'wal%' OR (name LIKE '%wallet%' AND name LIKE '%.%')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	names := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names[n] = true
	}
	rows2, _ := repo.Query(`SELECT id, name, json_extract(properties, '$.access_kind'), json_extract(properties, '$.func_id') FROM nodes WHERE name='wallet_log.wallet_id'`)
	for rows2.Next() {
		var id, nm, acc, fid string
		rows2.Scan(&id, &nm, &acc, &fid)
		t.Logf("wallet_log.wallet_id 来源: %s kind=%s func=%s", id, acc, fid)
	}
	rows2.Close()
	// Model(类型实参) 经 TableName() → wal_wallet_log（而非 wallet_log/transaction_data）
	if !names["wal_wallet_log.wallet_id"] {
		t.Errorf("Model(WalletLog{}) 应产出 wal_wallet_log.wallet_id（TableName 摘要），got %v", names)
	}
}
