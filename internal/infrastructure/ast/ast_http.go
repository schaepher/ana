package ast

import (
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"strconv"
	"strings"

	"golang.org/x/tools/go/packages"
)

// extractStringArg 从实参提取字符串值（字面量 / 同函数 methodVars /
// types.Const / 常量字符串拼接 P1-3），非字符串返回空。
func extractStringArg(pkg *packages.Package, methodVars map[string]string, arg ast.Expr) string {
	switch a := arg.(type) {
	case *ast.BasicLit:
		if a.Kind == token.STRING {
			if v, err := strconv.Unquote(a.Value); err == nil {
				return v
			}
		}
	case *ast.Ident:
		if v, ok := methodVars[a.Name]; ok {
			return v
		}
		if obj := pkg.TypesInfo.ObjectOf(a); obj != nil {
			if c, ok := obj.(*types.Const); ok && c.Val() != nil && c.Val().Kind() == constant.String {
				return constant.StringVal(c.Val())
			}
		}
	case *ast.BinaryExpr:

		if a.Op == token.ADD {
			l := extractStringArg(pkg, methodVars, a.X)
			r := extractStringArg(pkg, methodVars, a.Y)
			if l != "" && r != "" {
				return l + r
			}
		}
	}
	return ""
}

// httpURLString 从 http.Get/NewRequest/NewRequestWithContext 调用提取
// URL 字符串（§18.7，P1-3 补 NewRequestWithContext——与 NewRequest 同
// 参数位，ctx 在 0 位）：Get 第 1 参 / NewRequest 第 2 参 /
// NewRequestWithContext 第 3 参；URL 须含 scheme 或以 / 开头（防
// 误伤同名方法）。动态变量返回 ok=false（盲区）。
func httpURLString(pkg *packages.Package, methodVars map[string]string,
	call *ast.CallExpr, callee *types.Func) (string, bool) {
	if callee == nil {
		return "", false
	}
	idx := 0
	switch callee.Name() {
	case "Get":
	case "NewRequest":
		idx = 1
	case "NewRequestWithContext":
		idx = 2
	default:
		return "", false
	}
	if len(call.Args) <= idx {
		return "", false
	}
	u := extractStringArg(pkg, methodVars, call.Args[idx])
	if u == "" {
		return "", false
	}
	if !strings.Contains(u, "://") && !strings.HasPrefix(u, "/") {
		return "", false
	}
	return u, true
}

// httpMethodOf Q205d：按调用形态取 HTTP method 实参（常量传播）：
// http.Get → "GET"；NewRequest → Args[0]；NewRequestWithContext →
// Args[1]；未知/非常量 → ""（emitHTTP 默认 GET）。此前 emitHTTP 在
// 内部猜 Args[0]，http.Get(字面量 URL) 会把 URL 当 method。
func httpMethodOf(pkg *packages.Package, methodVars map[string]string, call *ast.CallExpr, callee *types.Func) string {
	if callee == nil {
		return ""
	}
	idx := -1
	switch callee.Name() {
	case "Get":
		return "GET"
	case "NewRequest":
		idx = 0
	case "NewRequestWithContext":
		idx = 1
	default:
		return ""
	}
	if len(call.Args) <= idx {
		return ""
	}
	return extractStringArg(pkg, methodVars, call.Args[idx])
}
