// ast_split/split.go：按声明名分组拆分 Go 文件（go/ast + go/printer）。
//
// 用法：split <src.go> <out1.go:name1,name2,...> <out2.go:...> ...
//   - 从 src.go 提取指定名称的顶层声明（函数/方法/类型/常量/变量），
//     写入各 out 文件（package + 声明 + 原文件全部 import——需再跑
//     goimports 清理未用导入）
//   - 名称键：方法用 "RecvName.MethodName"，函数/类型/常量用名称；
//     也可用行号（如 "L123"）精确指定
//   - src.go 删除已搬走的声明；文件名不匹配（未分组的声明）留在原文件
//
// 用法示例：
//   split repo.go repo_write.go:SaveBatch,SaveBatchStats repo_symbol.go:GetSymbol
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

type group struct {
	file   string
	names  map[string]bool // 声明键 → 命中
}

func splitMain(args []string) {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: split <src.go> <out.go:name,...> ...")
		os.Exit(2)
	}
	src := args[0]
	var groups []*group
	for _, arg := range args[1:] {
		i := strings.Index(arg, ":")
		if i < 0 {
			fmt.Fprintf(os.Stderr, "bad group %q (want out.go:name,...)\n", arg)
			os.Exit(2)
		}
		g := &group{file: arg[:i], names: map[string]bool{}}
		for _, n := range strings.Split(arg[i+1:], ",") {
			if n = strings.TrimSpace(n); n != "" {
				g.names[n] = false
			}
		}
		groups = append(groups, g)
	}

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, src, nil, parser.ParseComments)
	if err != nil {
		fmt.Fprintf(os.Stderr, "parse: %v\n", err)
		os.Exit(1)
	}

	// 声明键 → 组
	keyGroup := map[string]*group{}
	for _, g := range groups {
		for n := range g.names {
			keyGroup[n] = g
		}
	}

	// 遍历声明分拣
	var kept []ast.Decl
	moved := map[*group][]ast.Decl{}
	for _, decl := range f.Decls {
		key := declKey(fset, decl) // "L68:Repo.SaveBatch"
		name := key
		if i := strings.Index(key, ":"); i >= 0 {
			name = key[i+1:] // "Repo.SaveBatch"（方法含 receiver）
		}
		short := name
		if i := strings.LastIndex(name, "."); i >= 0 {
			short = name[i+1:] // "SaveBatch"（纯名）
		}
		// 匹配优先级：完整键 > Recv.Name > 纯名
		if g, ok := keyGroup[key]; ok {
			g.names[key] = true
			moved[g] = append(moved[g], decl)
			continue
		}
		if g, ok := keyGroup[name]; ok {
			g.names[name] = true
			moved[g] = append(moved[g], decl)
			continue
		}
		if g, ok := keyGroup[short]; ok {
			g.names[short] = true
			moved[g] = append(moved[g], decl)
			continue
		}
		kept = append(kept, decl)
	}

	// 未匹配的名称报错
	for _, g := range groups {
		for n, hit := range g.names {
			if !hit {
				fmt.Fprintf(os.Stderr, "WARN %s: 声明 %q 未找到\n", g.file, n)
			}
		}
		if len(moved[g]) == 0 {
			fmt.Fprintf(os.Stderr, "WARN %s: 无声明搬入\n", g.file)
		}
	}

	// 输出新文件
	for _, g := range groups {
		decls := moved[g]
		if len(decls) == 0 {
			continue
		}
		out := g.file
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "mkdir: %v\n", err)
			os.Exit(1)
		}
		w, err := os.Create(out)
		if err != nil {
			fmt.Fprintf(os.Stderr, "create %s: %v\n", out, err)
			os.Exit(1)
		}
		fmt.Fprintf(w, "package %s\n\n", f.Name.Name)
		// import 块（原文件全部——goimports 清理未用）
		for _, imp := range f.Imports {
			path := imp.Path.Value
			name := ""
			if imp.Name != nil {
				name = imp.Name.Name + " "
			}
			fmt.Fprintf(w, "import %s%s\n", name, path)
		}
		fmt.Fprintln(w)
		for _, d := range decls {
			if err := printer.Fprint(w, fset, d); err != nil {
				fmt.Fprintf(os.Stderr, "print decl: %v\n", err)
				os.Exit(1)
			}
			fmt.Fprintln(w)
		}
		w.Close()
		fmt.Printf("wrote %s (%d decls)\n", out, len(decls))
	}

	// 重写原文件（保留剩余声明；移除已搬移声明范围内的注释——
	// printer 打印 File 时 f.Comments 按位置输出，函数搬走后其 Doc/内部
	// 注释会变成孤立注释）
	var movedAll []ast.Decl
	for _, g := range groups {
		movedAll = append(movedAll, moved[g]...)
	}
	var comments []*ast.CommentGroup
	for _, cg := range f.Comments {
		line := fset.Position(cg.Pos()).Line
		removed := false
		for _, d := range movedAll {
			if line >= fset.Position(d.Pos()).Line && line <= fset.Position(d.End()).Line {
				removed = true
				break
			}
		}
		if !removed {
			comments = append(comments, cg)
		}
	}
	f.Comments = comments
	f.Decls = kept
	w, err := os.Create(src)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rewrite %s: %v\n", src, err)
		os.Exit(1)
	}
	if err := printer.Fprint(w, fset, f); err != nil {
		fmt.Fprintf(os.Stderr, "print src: %v\n", err)
		os.Exit(1)
	}
	w.Close()
	fmt.Printf("rewrote %s (%d decls kept)\n", src, len(kept))
}

// declKey 声明键：方法 "Recv.Method"，其他用名称；行号 L%d 附加。
func declKey(fset *token.FileSet, decl ast.Decl) string {
	line := fset.Position(decl.Pos()).Line
	prefix := fmt.Sprintf("L%d:", line)
	switch d := decl.(type) {
	case *ast.FuncDecl:
		name := d.Name.Name
		if d.Recv != nil && len(d.Recv.List) > 0 {
			recv := recvTypeName(d.Recv.List[0].Type)
			return prefix + recv + "." + name
		}
		return prefix + name
	case *ast.GenDecl:
		for _, spec := range d.Specs {
			switch s := spec.(type) {
			case *ast.TypeSpec:
				return prefix + s.Name.Name
			case *ast.ValueSpec:
				if len(s.Names) > 0 {
					return prefix + s.Names[0].Name
				}
			}
		}
	}
	return prefix + "?"
}

func recvTypeName(t ast.Expr) string {
	switch x := t.(type) {
	case *ast.StarExpr:
		return recvTypeName(x.X)
	case *ast.Ident:
		return x.Name
	case *ast.IndexExpr:
		return recvTypeName(x.X)
	case *ast.IndexListExpr:
		return recvTypeName(x.X)
	}
	return "?"
}
