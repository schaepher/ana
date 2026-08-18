package action

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestActionsEntryLogs：Q206 所有 Actions 入口方法必须带 info 日志——
// 入口 logger.Info("enter (Actions).Xxx", 入参...) + defer exit 日志。
// 静态 AST 检查（不依赖运行时 logger）：解析本包源码，对每个导出的
// (a *Actions) 方法（排除小写内部 helper）断言函数体包含 enter/exit
// Info 调用（enter 消息含方法名）。
func TestActionsEntryLogs(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	methods := map[string]bool{} // 导出方法名 → 是否通过
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		fset := token.NewFileSet()
		pf, err := parser.ParseFile(fset, f, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range pf.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Recv == nil || len(fd.Recv.List) != 1 {
				continue
			}
			recv := fd.Recv.List[0].Type
			star, ok := recv.(*ast.StarExpr)
			if !ok {
				continue
			}
			ident, ok := star.X.(*ast.Ident)
			if !ok || ident.Name != "Actions" || !fd.Name.IsExported() {
				continue
			}
			name := fd.Name.Name
			body := fd.Body
			if body == nil {
				continue
			}
			hasEnter := false
			hasExit := false
			for _, stmt := range body.List {
				if es, ok := stmt.(*ast.ExprStmt); ok {
					if c, ok := es.X.(*ast.CallExpr); ok {
						if sel, ok := c.Fun.(*ast.SelectorExpr); ok {
							if ci, ok := sel.X.(*ast.Ident); ok && ci.Name == "logger" {
								msg := ""
								if len(c.Args) > 0 {
									if bl, ok := c.Args[0].(*ast.BasicLit); ok {
										msg = strings.Trim(bl.Value, `"`)
									}
								}
								if sel.Sel.Name == "Info" {
									if strings.HasPrefix(msg, "enter (Actions)."+name) {
										hasEnter = true
									}
									if strings.HasPrefix(msg, "exit (Actions)."+name) {
										hasExit = true
									}
								}
							}
						}
					}
				}
				// defer logger.Info("exit ...")
				if ds, ok := stmt.(*ast.DeferStmt); ok {
					if c, ok := ds.Call.Fun.(*ast.SelectorExpr); ok {
						if ci, ok := c.X.(*ast.Ident); ok && ci.Name == "logger" && c.Sel.Name == "Info" {
							if len(ds.Call.Args) > 0 {
								if bl, ok := ds.Call.Args[0].(*ast.BasicLit); ok {
									if strings.HasPrefix(strings.Trim(bl.Value, `"`), "exit (Actions)."+name) {
										hasExit = true
									}
								}
							}
						}
					}
				}
			}
			if !hasEnter || !hasExit {
				t.Errorf("(Actions).%s 缺少入口日志：enter=%v exit=%v", name, hasEnter, hasExit)
			}
			methods[name] = true
		}
	}
	// 至少覆盖主要入口（防止检查逻辑空转）
	for _, m := range []string{"Roots", "Search", "Expand", "Relations", "RelationsAll", "ER",
		"ValueTrace", "Trace", "ModuleCalls", "Symbol", "Unused", "SummaryChain", "Table", "Callers"} {
		if !methods[m] {
			t.Errorf("未找到 (Actions).%s（检查逻辑可能空转）", m)
		}
	}
}
