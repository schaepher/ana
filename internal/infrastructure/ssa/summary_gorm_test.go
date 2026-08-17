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
		"Where", "Find", "First", "Take", "Last"} {
		if !have["gorm.io/gorm.(DB)."+fn] {
			t.Errorf("GORM %s 应有 spec（覆盖清单见 summary_gorm.go）", fn)
		}
	}
	// 未支持清单（人工对照 gorm 官方 API；t.Log 不失败）
	var missing []string
	for _, fn := range []string{"Table", "Model", "Not", "Or", "Select", "Omit",
		"Joins", "Preload", "Scan", "Pluck", "Count", "Exec", "Raw", "Row", "Rows",
		"Transaction", "AutoMigrate", "Session", "UpdateColumn", "Updates"} {
		if !have["gorm.io/gorm.(DB)."+fn] {
			missing = append(missing, fn)
		}
	}
	t.Logf("GORM 未支持 API（候选补充）：%v", missing)
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
}
