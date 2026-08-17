package ast

import (
	"go/ast"
	"go/types"

	"golang.org/x/tools/go/packages"
)

// funcValueRef 解析函数值引用（P2-1）：g（函数名 Ident）或 obj.M
// （方法值 SelectorExpr）；非函数值返回 ok=false。
func funcValueRef(pkg *packages.Package, expr ast.Expr) (*types.Func, bool) {
	switch e := expr.(type) {
	case *ast.Ident:
		if fn, ok := pkg.TypesInfo.ObjectOf(e).(*types.Func); ok {
			return fn, true
		}
	case *ast.SelectorExpr:
		if sel := pkg.TypesInfo.Selections[e]; sel != nil {
			if fn, ok := sel.Obj().(*types.Func); ok {
				return fn, true
			}
		}
	}
	return nil, false
}

// argFuncRef 将调用参数解析为函数引用（作为参数传入的回调）：
//
//	Ident（home）                          → 具名函数
//	SelectorExpr（s.PageHome / pkg.F）     → 方法/包函数引用
//	CallExpr（http.HandlerFunc(home)）     → 解包 HandlerFunc 包装
//
// 非函数引用（变量/字面量/匿名函数）返回 nil。
func argFuncRef(pkg *packages.Package, arg ast.Expr) *types.Func {
	var obj types.Object
	switch a := arg.(type) {
	case *ast.Ident:
		obj = pkg.TypesInfo.Uses[a]
	case *ast.SelectorExpr:
		obj = pkg.TypesInfo.Uses[a.Sel]
	case *ast.CallExpr:

		if sel, ok := a.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "HandlerFunc" && len(a.Args) == 1 {
			if id, ok := a.Args[0].(*ast.Ident); ok {
				obj = pkg.TypesInfo.Uses[id]
			}
		}
	}
	if obj == nil {
		return nil
	}
	fn, _ := obj.(*types.Func)
	return fn
}
