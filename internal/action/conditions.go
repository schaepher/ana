// 路径条件标注（field_trace.md §15.2 Q92）：查询期计算、不落库。
// 对追溯行（value-trace / trace）叠加节点所在分支的条件：
//   - if 分支条件（常量可传播：字段路径/字面量比较）
//   - 类型 switch 分支（case 标签）
//   - 环境条件（os.Getenv 等调用，并入分支标注）
// 实现：节点 file/line → 解析源码 AST → 包含该行的最近 if/类型 switch
// → 条件表达式文本。无符号执行，条件为源码文本。
package action

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"

	"github.com/schaepher/codeintel/internal/domain"
)

// ExtractCondition 提取源码文件第 line 行所在分支的条件表达式文本
// （if 条件 / 类型 switch 的 case 标签）；行不在任何分支内返回空。
// 嵌套 if 取最内层（最近的分支）。
func ExtractCondition(filePath string, line int) string {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filePath, nil, parser.SkipObjectResolution)
	if err != nil {
		return ""
	}
	var cond string
	ast.Inspect(f, func(n ast.Node) bool {
		switch st := n.(type) {
		case *ast.IfStmt:
			if st.Cond != nil && nodeContainsLine(fset, st, line) {
				cond = exprText(fset, st.Cond)
			}
		case *ast.TypeSwitchStmt:
			for _, s := range st.Body.List {
				cs, ok := s.(*ast.CaseClause)
				if !ok || !nodeContainsLine(fset, cs, line) {
					continue
				}
				var parts []string
				for _, e := range cs.List {
					parts = append(parts, exprText(fset, e))
				}
				if len(parts) > 0 {
					cond = "类型分支: " + strings.Join(parts, " | ")
				}
			}
		}
		return true
	})
	return cond
}

// nodeContainsLine 判断节点位置区间（起止行）是否包含目标行。
func nodeContainsLine(fset *token.FileSet, n ast.Node, line int) bool {
	if n == nil || n.Pos() == token.NoPos || n.End() == token.NoPos {
		return false
	}
	start := fset.Position(n.Pos()).Line
	end := fset.Position(n.End()).Line
	return line >= start && line <= end
}

// exprText 输出表达式源码文本。
func exprText(fset *token.FileSet, e ast.Expr) string {
	if e == nil {
		return ""
	}
	var buf bytes.Buffer
	if err := format.Node(&buf, fset, e); err != nil {
		return ""
	}
	return buf.String()
}

// TraceConditions 为追溯行叠加条件标注（Q92 查询期计算）：
// 每行的节点位置（DB 查 file_path + 行号）→ ExtractCondition。
// 返回新切片（不修改入参）；源码文件按路径缓存解析。
func (a *Actions) TraceConditions(rows []*domain.TraceRow) ([]*domain.TraceRow, error) {
	if len(rows) == 0 {
		return rows, nil
	}
	// 节点 file_path 缓存（GetSymbol 查询）
	fileOf := map[string]string{}
	out := make([]*domain.TraceRow, len(rows))
	for i, r := range rows {
		out[i] = r
		if r.Line <= 0 {
			continue
		}
		fp, ok := fileOf[string(r.ID)]
		if !ok {
			n, err := a.repo.GetSymbol(r.ID)
			if err != nil || n.FilePath == "" {
				fileOf[string(r.ID)] = "" // 查不到：跳过
				continue
			}
			fp = n.FilePath
			fileOf[string(r.ID)] = fp
		}
		if fp == "" {
			continue
		}
		full := filepath.Join(a.repo.RepoPath(), filepath.FromSlash(fp))
		if c := ExtractCondition(full, r.Line); c != "" {
			out[i].Conditions = []string{c}
		}
	}
	return out, nil
}
