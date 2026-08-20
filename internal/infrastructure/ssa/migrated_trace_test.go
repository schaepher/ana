package ssa

import (
	"strings"

	"github.com/schaepher/codeintel/internal/domain"
)

// 本文件由 tmp/ast_split/migrate.go 从 integration/ 迁移（2026-08-17）：
// fixture 自建、断言全为 SSA/sqlite 产物——单元测试化，脱离 scip/CLI
// 管道用 indexFixtureRepo 落库后直接 repo 断言。

// traceHas 检查追溯行中 Name/FullPath 含 substr。
func traceHas(rows []*domain.TraceRow, substr string) bool {
	for _, r := range rows {
		if strings.Contains(r.Name, substr) || strings.Contains(r.FullPath, substr) ||
			strings.Contains(r.FuncID, substr) {
			return true
		}
	}
	return false
}

// vtCandHas 检查追溯行中是否含动态候选边标注（Q161 EdgeOrigin）。
func vtCandHas(rows []*domain.TraceRow) bool {
	for _, r := range rows {
		if r.EdgeOrigin != "" || r.DispatchOrigin != "" {
			return true
		}
	}
	return false
}

// ffsHas 检查函数字段摘要行中 FieldPath 含 substr（fields 查询断言）。
func ffsHas(rows []*domain.FunctionFieldSummary, substr string) bool {
	for _, r := range rows {
		if strings.Contains(r.FieldPath, substr) {
			return true
		}
	}
	return false
}
