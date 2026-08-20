// ast_split/analyze.go：列出大 Go 文件的顶层声明清单（函数/方法/类型/
// 变量/常量），为按主题分组拆分做准备。
//
// 用法：go run ./tmp/ast_split/analyze.go <file.go> [<file.go>...]
// 输出每行：start-end | 行数 | kind | receiver | name
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
)

func analyzeMain(args []string) {
	for _, path := range args {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			fmt.Fprintf(os.Stderr, "parse %s: %v\n", path, err)
			continue
		}
		fmt.Printf("== %s (%d lines)\n", filepath.Base(path), fset.File(f.Pos()).LineCount())
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				start := fset.Position(d.Pos()).Line
				end := fset.Position(d.End()).Line
				recv := ""
				if d.Recv != nil && len(d.Recv.List) > 0 {
					recv = recvType(d.Recv.List[0].Type)
				}
				kind := "func"
				if recv != "" {
					kind = "method"
				}
				fmt.Printf("  %4d-%-4d | %4d | %-6s | %-28s | %s\n",
					start, end, end-start+1, kind, recv, d.Name.Name)
			case *ast.GenDecl:
				if d.Tok == token.IMPORT {
					continue
				}
				start := fset.Position(d.Pos()).Line
				end := fset.Position(d.End()).Line
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						fmt.Printf("  %4d-%-4d | %4d | type   | %-28s | %s\n",
							start, end, end-start+1, "", s.Name.Name)
					case *ast.ValueSpec:
						for _, n := range s.Names {
							fmt.Printf("  %4d-%-4d | %4d | %-6s | %-28s | %s\n",
								start, end, end-start+1, d.Tok.String(), "", n.Name)
						}
					}
				}
			}
		}
	}
}

func recvType(t ast.Expr) string {
	switch x := t.(type) {
	case *ast.StarExpr:
		return "*" + recvType(x.X)
	case *ast.Ident:
		return x.Name
	case *ast.IndexExpr:
		return recvType(x.X)
	case *ast.IndexListExpr:
		return recvType(x.X)
	case *ast.SelectorExpr:
		return recvType(x.X) + "." + x.Sel.Name
	}
	return ""
}
