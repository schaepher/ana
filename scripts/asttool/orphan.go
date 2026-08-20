// orphan.go：列出 Go 文件的孤立顶层注释（asttool orphan <file...>）。
//
// 孤立 = 注释块不属于任何顶层声明（不在 decl 的 Doc 中、不在 decl 体内、
// 不在 const/type/var 块内），且不在 package 文档区（package 关键字前）。
// 典型来源：方法实现被拆走后文档注释残留（与实现文件注释重复）。
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
)

func orphanMain(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "用法: asttool orphan <file.go> [<file.go>...]")
		os.Exit(2)
	}
	total := 0
	for _, path := range args {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
			continue
		}
		cmap := ast.NewCommentMap(fset, f, f.Comments)
		// 归属集合：遍历所有节点（含 Field/ValueSpec/语句——const/type
		// 块内、函数体内的注释都归属对应节点）
		belong := map[*ast.CommentGroup]bool{}
		if f.Doc != nil {
			belong[f.Doc] = true
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if n == nil {
				return true
			}
			for _, g := range cmap[n] {
				belong[g] = true
			}
			return true
		})
		// 孤立 = 未归属且不在 package 行之前
		pkgLine := fset.Position(f.Package).Line
		for _, g := range f.Comments {
			if belong[g] {
				continue
			}
			start := fset.Position(g.Pos()).Line
			end := fset.Position(g.End()).Line
			if end < pkgLine {
				continue // package 文档区
			}
			fmt.Printf("%s:%d-%d\n", path, start, end)
			total++
		}
	}
	fmt.Printf("--- total %d orphan comment groups ---\n", total)
}
