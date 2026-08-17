package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
)

// processFile 分析并注入单个 Go 文件；返回 (注入数, 跳过数, 错误)。
func processFile(path string) (int, int, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, err
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return 0, 0, fmt.Errorf("parse: %w", err)
	}

	needZap := false
	needLogging := false
	var injected, skipped int
	var inserts []insert

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		if alreadyInjected(fn) {
			skipped++
			continue
		}
		if hasLoggerIdent(fn) {
			skipped++
			continue
		}
		ctxParam := findCtxParam(fn)
		bodyStart := fset.Position(fn.Body.Lbrace).Offset + 1
		singleLine := fset.Position(fn.Body.Lbrace).Line == fset.Position(fn.Body.Rbrace).Line
		if singleLine {

			inserts = append(inserts,
				insert{offset: bodyStart, text: bodyText(ctxParam, funcName(fn), true)},
				insert{offset: fset.Position(fn.Body.Rbrace).Offset, text: "\n"},
			)
		} else {
			inserts = append(inserts, insert{offset: bodyStart, text: bodyText(ctxParam, funcName(fn), false)})
		}
		injected++
		if ctxParam != "" {
			needLogging = true
		} else {
			needZap = true
		}
	}
	if injected == 0 {
		return 0, skipped, nil
	}

	importOffset, importIsBlock := importInsertOffset(fset, file)
	if needZap && !hasImport(file, zapImport) {
		inserts = append(inserts, insert{offset: importOffset, text: importText(importIsBlock, zapImport)})
	}
	if needLogging && !hasImport(file, loggingImport) {
		inserts = append(inserts, insert{offset: importOffset, text: importText(importIsBlock, loggingImport)})
	}

	out := applyInserts(string(src), inserts)
	if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
		return 0, 0, err
	}
	return injected, skipped, nil
}

// applyInserts 按 offset 降序应用插入（避免偏移失效）。
func applyInserts(src string, inserts []insert) string {
	sortInserts(inserts)
	for _, ins := range inserts {
		src = src[:ins.offset] + ins.text + src[ins.offset:]
	}
	return src
}
func sortInserts(ins []insert) {
	for i := 1; i < len(ins); i++ {
		for j := i; j > 0 && ins[j].offset > ins[j-1].offset; j-- {
			ins[j], ins[j-1] = ins[j-1], ins[j]
		}
	}
}

// importInsertOffset 返回 import 补入位置，并报告 import 是否为括号块：
//   - 括号块：最后一个 spec 之后
//   - 单行 import：该行末尾
//   - 无 import：package 声明之后
func importInsertOffset(fset *token.FileSet, file *ast.File) (offset int, isBlock bool) {
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.IMPORT {
			continue
		}
		if gd.Lparen.IsValid() {
			if len(gd.Specs) > 0 {
				return fset.Position(gd.Specs[len(gd.Specs)-1].End()).Offset, true
			}
			return fset.Position(gd.Lparen).Offset, true
		}
		return fset.Position(gd.End()).Offset, false
	}
	return fset.Position(file.Name.End()).Offset + 1, false
}

// importText 生成 import 补入文本（配合 importInsertOffset 使用）。
func importText(isBlock bool, path string) string {
	if isBlock {
		return "\n\t" + strconv.Quote(path)
	}

	return "\n\nimport (\n\t" + strconv.Quote(path) + "\n)"
}
func hasImport(file *ast.File, path string) bool {
	for _, imp := range file.Imports {
		if strings.Trim(imp.Path.Value, `"`) == path {
			return true
		}
	}
	return false
}

// bodyText 生成注入到函数体开头的语句文本。
// singleLine 时以 \t 结尾（配合 Rbrace 前补 \n，将原单行内容拆为独立行）。
func bodyText(ctxParam, name string, singleLine bool) string {
	init := "logger := zap.L()"
	if ctxParam != "" {
		init = "logger := logging.FromContext(" + ctxParam + ")"
	}
	t := "\n\t" + init +
		"\n\tlogger.Debug(\"enter " + name + "\")" +
		"\n\tdefer logger.Debug(\"exit " + name + "\")"
	if singleLine {
		t += "\n\t"
	}
	return t
}

// alreadyInjected 检测函数体首语句是否已是注入模式（logger := … + enter Debug）。
func alreadyInjected(fn *ast.FuncDecl) bool {
	list := fn.Body.List
	if len(list) < 2 {
		return false
	}
	assign, ok := list[0].(*ast.AssignStmt)
	if !ok || assign.Tok != token.DEFINE || len(assign.Lhs) != 1 {
		return false
	}
	id, ok := assign.Lhs[0].(*ast.Ident)
	if !ok || id.Name != "logger" {
		return false
	}
	enter, ok := list[1].(*ast.ExprStmt)
	if !ok {
		return false
	}
	call, ok := enter.X.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Debug" || len(call.Args) == 0 {
		return false
	}
	lit, ok := call.Args[0].(*ast.BasicLit)
	return ok && lit.Kind == token.STRING && strings.HasPrefix(lit.Value, `"enter `)
}

// hasLoggerIdent 检查函数签名与函数体内是否已有 "logger" 标识符。
func hasLoggerIdent(fn *ast.FuncDecl) bool {
	found := false
	ast.Inspect(fn, func(n ast.Node) bool {
		if found {
			return false
		}
		if id, ok := n.(*ast.Ident); ok && id.Name == "logger" {
			found = true
			return false
		}
		return true
	})
	return found
}

// findCtxParam 返回第一个类型为 context.Context 的参数名；无则返回空串。
func findCtxParam(fn *ast.FuncDecl) string {
	if fn.Type.Params == nil {
		return ""
	}
	for _, field := range fn.Type.Params.List {
		if !isContextType(field.Type) {
			continue
		}
		if len(field.Names) > 0 {
			return field.Names[0].Name
		}
	}
	return ""
}

// isContextType 启发式判断类型是否为 context.Context（SelectorExpr）。
func isContextType(t ast.Expr) bool {
	sel, ok := t.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == "context" && sel.Sel.Name == "Context"
}

// funcName 生成日志用的函数名：方法带接收者类型，如 (Service).CreatePayment。
func funcName(fn *ast.FuncDecl) string {
	if fn.Recv != nil && len(fn.Recv.List) > 0 {
		name := typeName(fn.Recv.List[0].Type)
		return fmt.Sprintf("(%s).%s", name, fn.Name.Name)
	}
	return fn.Name.Name
}
func typeName(t ast.Expr) string {
	switch tt := t.(type) {
	case *ast.Ident:
		return tt.Name
	case *ast.StarExpr:
		return typeName(tt.X)
	case *ast.IndexExpr:
		return typeName(tt.X)
	default:
		return "?"
	}
}
