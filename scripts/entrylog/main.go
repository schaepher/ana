// entrylog 为 Go 项目所有顶层函数/方法注入 enter/exit 调试日志（zap，debug 级）。
//
// 对每个函数在函数体开头注入：
//
//	无 ctx 参数：logger := zap.L()
//	有 ctx 参数：logger := logging.FromContext(ctx)   // 从 ctx 取 logger，缺失回退全局
//	logger.Debug("enter <name>")
//	defer logger.Debug("exit <name>")
//
// 用法：
//
//	go run ./scripts/entrylog -dir <项目根目录>
//
// 实现：AST 只读分析定位插入点，正文用文本插入（保留原文件格式与注释，
// 避免 format.Node 对游离注释的重排）。import 缺失时同样文本补入。
// 幂等：已注入的函数（首语句 logger 赋值 + enter Debug 调用）跳过，可重复运行。
// 安全：函数体内已有 logger 标识符（参数/局部变量）时跳过注入，避免遮蔽。
// 排除：_test.go、scripts/、internal/logging（helper 自身注入会无限递归）。
package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// 注入代码用到的包
const (
	zapImport     = "go.uber.org/zap"
	loggingImport = "github.com/schaepher/codeintel/internal/logging"
)

var skipDirs = map[string]bool{
	"scripts":          true,
	"internal/logging": true, // FromContext 注入自身会无限递归
	".git":             true,
	".codeintel":       true,
	"vendor":           true,
}

func main() {
	root := flag.String("dir", ".", "要处理的 Go 模块根目录")
	flag.Parse()

	var processed, injected, skipped int
	err := filepath.WalkDir(*root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			rel, rerr := filepath.Rel(*root, path)
			if rerr == nil && skipDirs[rel] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		n, s, err := processFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  [error] %s: %v\n", path, err)
			return nil
		}
		processed++
		injected += n
		skipped += s
		return nil
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "walk: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("处理完成: 文件 %d，注入函数 %d，跳过（已有 logger 标识符/已注入）%d\n",
		processed, injected, skipped)
}

// insert 是一次文本插入（offset 为字节偏移）。
type insert struct {
	offset int
	text   string
}

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
		bodyStart := fset.Position(fn.Body.Lbrace).Offset + 1 // `{` 之后
		singleLine := fset.Position(fn.Body.Lbrace).Line == fset.Position(fn.Body.Rbrace).Line
		if singleLine {
			// { return x } → 拆行：{ 后插注入（末尾 \t），} 前插换行
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

	// import 补入（文本方式，插在 import 块末尾或 package 行后）
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
	// 单行/无 import：新建一个 import 块（Go 允许多个 import 语句）
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
	case *ast.IndexExpr: // 泛型
		return typeName(tt.X)
	default:
		return "?"
	}
}
