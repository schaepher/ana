package ssa

import (
	"go/ast"
	"sort"

	"go.uber.org/zap"
	"golang.org/x/tools/go/packages"
)

// buildAssignTargets 构建 赋值表达式区间 → 目标变量名（Q83：lifting 后
// map/slice 字面量为 MakeMap/MakeSlice 寄存器，其 Pos 落在字面量内部，
// 用区间匹配恢复容器名）。按 start 排序返回，供二分查找。
func buildAssignTargets(pkgs []*packages.Package, modules []string) []assignTarget {
	logger := zap.L()
	logger.Debug("enter buildAssignTargets")
	defer logger.Debug("exit buildAssignTargets")
	targets := []assignTarget{}
	for _, p := range pkgs {
		if !isInModule(p.PkgPath, modules) {
			continue
		}
		for _, f := range p.Syntax {
			ast.Inspect(f, func(n ast.Node) bool {
				switch st := n.(type) {
				case *ast.AssignStmt:
					for i, rhs := range st.Rhs {
						name := lhsIdentName(st.Lhs, i)
						if name != "" {
							targets = append(targets, assignTarget{name: name, start: rhs.Pos(), end: rhs.End()})
						}
					}
				case *ast.ValueSpec:
					for i, v := range st.Values {
						if i < len(st.Names) {
							targets = append(targets, assignTarget{name: st.Names[i].Name, start: v.Pos(), end: v.End()})
						}
					}
				}
				return true
			})
		}
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].start < targets[j].start })
	return targets
}

// lhsIdentName 取赋值目标标识符名（多目标取第 i 个；复合目标如 x[0] 取 x）。
func lhsIdentName(lhs []ast.Expr, i int) string {
	if i >= len(lhs) {
		return ""
	}
	switch l := lhs[i].(type) {
	case *ast.Ident:
		return l.Name
	case *ast.IndexExpr:
		if id, ok := l.X.(*ast.Ident); ok {
			return id.Name
		}
	case *ast.SelectorExpr:
		if id, ok := l.X.(*ast.Ident); ok {
			return id.Name
		}
	}
	return ""
}
