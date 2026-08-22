// asttool rename：符号感知重命名（Q235-4，借鉴 GitNexus「重命名禁用
// 查找替换」——Q232 move 脚本文本替换误删代码行教训）。
//
//	asttool rename <file.go> <old> <new> [--scope pkg|file] [--dry-run] [--include-tests]
//
// 语义：
//   - AST Ident 节点替换——字符串字面量/注释/import 路径天然不动
//   - 声明跟随：包级函数+调用处 / 类型+使用处 / 方法+选择器调用 /
//     局部变量（含参数、短声明）+ 引用
//   - 遮蔽：局部声明遮蔽包级同名符号时，局部作用域内引用不替换
//     （Go 词法作用域：声明点起生效）
//   - 方法重命名近似：同文件内 SelectorExpr.Sel 全替换（无类型信息，
//     文件内有同名结构体字段时需人工复核）
//   - 冲突检测：新名在目标作用域已存在 → 报错不写文件
//   - --scope file（默认）只改指定文件；pkg 同目录全部 .go（不含
//     _test.go，--include-tests 显式开启）
//   - --dry-run 输出改动清单不写文件
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

	"golang.org/x/tools/go/ast/astutil"
)

type renameMode int

const (
	modePkg renameMode = iota // 包级符号（函数/类型/var/const）
	modeMethod                // 方法（Recv 绑定）
	modeLocal                 // 函数内局部变量
)

// scopeFrame 词法作用域帧：本作用域声明名 → 声明位置。
type scopeFrame struct {
	decls map[string]token.Pos
}

type renamer struct {
	fset     *token.FileSet
	old, new string
	mode     renameMode
	recvType string        // modeMethod：方法的 Recv 类型名（冲突检测用）
	stack    []*scopeFrame // 作用域栈（根=包级）
	changed  bool
	seen     map[*ast.Ident]bool // 声明处 ident 已处理（防 astutil 二次命中）
	report   []string            // dry-run 清单
}

func newRenamer(fset *token.FileSet, old, new string, mode renameMode, recvType string) *renamer {
	return &renamer{fset: fset, old: old, new: new, mode: mode, recvType: recvType,
		seen: map[*ast.Ident]bool{}}
}

// pkgDecls 收集文件包级声明名集合。
func pkgDecls(f *ast.File) map[string]token.Pos {
	out := map[string]token.Pos{}
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			if d.Recv == nil {
				out[d.Name.Name] = d.Name.Pos()
			}
		case *ast.GenDecl:
			for _, s := range d.Specs {
				switch s := s.(type) {
				case *ast.ValueSpec:
					for _, n := range s.Names {
						out[n.Name] = n.Pos()
					}
				case *ast.TypeSpec:
					out[s.Name.Name] = s.Name.Pos()
				}
			}
		}
	}
	return out
}

// methodNames 收集 Recv 类型的方法名集合（冲突检测）。
func methodNames(f *ast.File, recvType string) map[string]token.Pos {
	out := map[string]token.Pos{}
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Recv == nil || len(fd.Recv.List) == 0 {
			continue
		}
		t := recvTypeName(fd.Recv.List[0].Type)
		if t == recvType {
			out[fd.Name.Name] = fd.Name.Pos()
		}
	}
	return out
}

// localScopeStart 局部重命名：old 声明所在函数体的作用域起点（栈下标）。
// 冲突检测与替换范围用；-1 表示未找到局部声明。
func (r *renamer) localDeclScope(name string) (int, token.Pos) {
	for i := len(r.stack) - 1; i >= 1; i-- {
		if p, ok := r.stack[i].decls[name]; ok {
			return i, p
		}
	}
	return -1, token.NoPos
}

// nearestDecl 从 innermost 向外找最近声明（返回作用域下标与位置）。
// p <= pos：声明点起生效（含声明处本身——遮蔽声明处 ident 命中局部
// 声明而非包级，防被包级重命名误改）。
func (r *renamer) nearestDecl(name string, pos token.Pos) (int, token.Pos) {
	for i := len(r.stack) - 1; i >= 0; i-- {
		if p, ok := r.stack[i].decls[name]; ok {
			if i == 0 || p <= pos {
				return i, p
			}
		}
	}
	return -1, token.NoPos
}

// markDecl 声明处 ident：直接替换（声明处不经过遮蔽判定）。
func (r *renamer) markDecl(id *ast.Ident) {
	if r.seen[id] {
		return
	}
	r.seen[id] = true
	if id.Name == r.old {
		id.Name = r.new
		r.changed = true
		if r.fset != nil {
			r.report = append(r.report, fmt.Sprintf("%s:%d: %s → %s",
				r.fset.Position(id.Pos()).Filename, r.fset.Position(id.Pos()).Line, r.old, r.new))
		}
	}
}

// identReplacer 处理引用处 ident 的替换。
func (r *renamer) identReplacer(id *ast.Ident) {
	if r.seen[id] || id.Name != r.old {
		return
	}
	if r.mode == modeMethod {
		// 方法名不出现在局部声明遮蔽链——选择器 Sel 与声明处统一替换
		id.Name = r.new
		r.changed = true
		return
	}
	idx, p := r.nearestDecl(r.old, id.Pos())
	if r.mode == modePkg {
		// 包级重命名：仅当最近的声明是包级（根）或无声明（引用）时替换
		if idx == 0 || idx < 0 {
			id.Name = r.new
			r.changed = true
		}
		return
	}
	// 局部重命名：最近生效声明是局部（非根）
	if idx > 0 && p < id.Pos() {
		id.Name = r.new
		r.changed = true
	}
}

// renameFile 重命名单文件，返回是否改动。
func (r *renamer) renameFile(f *ast.File) {
	// 根作用域 = 包级声明
	root := &scopeFrame{decls: pkgDecls(f)}
	r.stack = []*scopeFrame{root}

	astutil.Apply(f, func(c *astutil.Cursor) bool {
		switch n := c.Node().(type) {
		case *ast.FuncDecl:
			// 方法声明处
			if n.Recv != nil && r.mode == modeMethod && n.Name.Name == r.old {
				r.markDecl(n.Name)
			}
			if n.Recv == nil && r.mode == modePkg && n.Name.Name == r.old {
				r.markDecl(n.Name)
			}
			// 函数体是新作用域；Recv/参数 Field 名由 *ast.Field 登记
			r.stack = append(r.stack, &scopeFrame{decls: map[string]token.Pos{}})
		case *ast.BlockStmt, *ast.IfStmt, *ast.SwitchStmt, *ast.TypeSwitchStmt,
			*ast.SelectStmt, *ast.ForStmt, *ast.RangeStmt, *ast.CaseClause,
			*ast.CommClause:
			r.stack = append(r.stack, &scopeFrame{decls: map[string]token.Pos{}})
		case *ast.ValueSpec:
			for _, id := range n.Names {
				i := len(r.stack) - 1
				r.stack[i].decls[id.Name] = id.Pos()
				// 声明处替换须匹配作用域：modePkg 仅根（包级）声明处；
				// modeLocal 仅非根（局部遮蔽声明处不替换——它声明的是
				// 遮蔽符号，包级重命名不动）
				if (r.mode == modePkg && i == 0) || (r.mode == modeLocal && i > 0) {
					r.markDecl(id)
				}
			}
		case *ast.AssignStmt:
			if n.Tok == token.DEFINE {
				for _, e := range n.Lhs {
					if id, ok := e.(*ast.Ident); ok {
						i := len(r.stack) - 1
						r.stack[i].decls[id.Name] = id.Pos()
						if (r.mode == modePkg && i == 0) || (r.mode == modeLocal && i > 0) {
							r.markDecl(id)
						}
					}
				}
			}
		case *ast.Field:
			for _, id := range n.Names {
				i := len(r.stack) - 1
				r.stack[i].decls[id.Name] = id.Pos()
				if (r.mode == modePkg && i == 0) || (r.mode == modeLocal && i > 0) {
					r.markDecl(id)
				}
			}
		case *ast.TypeSpec:
			i := len(r.stack) - 1
			r.stack[i].decls[n.Name.Name] = n.Name.Pos()
			if (r.mode == modePkg && i == 0) || (r.mode == modeLocal && i > 0) {
				r.markDecl(n.Name)
			}
		case *ast.SelectorExpr:
			// 方法调用选择器（方法模式统一替换）
			if r.mode == modeMethod && n.Sel.Name == r.old {
				r.markDecl(n.Sel)
			}
		case *ast.Ident:
			r.identReplacer(n)
		}
		return true
	}, func(c *astutil.Cursor) bool {
		switch c.Node().(type) {
		case *ast.FuncDecl, *ast.BlockStmt, *ast.IfStmt, *ast.SwitchStmt,
			*ast.TypeSwitchStmt, *ast.SelectStmt, *ast.ForStmt, *ast.RangeStmt,
			*ast.CaseClause, *ast.CommClause:
			r.stack = r.stack[:len(r.stack)-1]
		}
		return true
	})
}

// resolveMode 判定 old 的类别（包级/方法/局部）并做冲突检测。
// 返回 mode、recvType（方法时）、错误。
func resolveMode(f *ast.File, old, new string, global *renameMode) (renameMode, string, error) {
	pkg := pkgDecls(f)
	_, hasPkg := pkg[old]
	// 方法：收集所有 Recv 类型的方法集合
	methods := map[string]map[string]token.Pos{}
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Recv == nil || len(fd.Recv.List) == 0 {
			continue
		}
		t := recvTypeName(fd.Recv.List[0].Type)
		if methods[t] == nil {
			methods[t] = map[string]token.Pos{}
		}
		methods[t][fd.Name.Name] = fd.Name.Pos()
	}
	var hasMethodType string
	for t, ms := range methods {
		if _, ok := ms[old]; ok {
			hasMethodType = t
			break
		}
	}
	if hasPkg && hasMethodType != "" {
		return 0, "", fmt.Errorf("%q 同时是包级符号与方法（%s），请分别处理", old, hasMethodType)
	}
	switch {
	case hasPkg:
		if _, dup := pkg[new]; dup {
			return 0, "", fmt.Errorf("新名 %q 与包级声明冲突", new)
		}
		return modePkg, "", nil
	case hasMethodType != "":
		if _, dup := methods[hasMethodType][new]; dup {
			return 0, "", fmt.Errorf("新名 %q 与 %s 的方法冲突", new, hasMethodType)
		}
		return modeMethod, hasMethodType, nil
	}
	// 局部：old 在文件内某函数体有声明（ast.Inspect 找）
	locals := map[string]token.Pos{}
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.ValueSpec:
			for _, id := range n.Names {
				if id.Name == old {
					locals[old] = id.Pos()
				}
			}
		case *ast.AssignStmt:
			if n.Tok == token.DEFINE {
				for _, e := range n.Lhs {
					if id, ok := e.(*ast.Ident); ok && id.Name == old {
						locals[old] = id.Pos()
					}
				}
			}
		case *ast.Field:
			for _, id := range n.Names {
				if id.Name == old {
					locals[old] = id.Pos()
				}
			}
		}
		return true
	})
	if _, ok := locals[old]; !ok {
		// pkg 模式：声明可能在别的文件（引用文件）——沿用全局判定
		if global != nil {
			return *global, "", nil
		}
		return 0, "", fmt.Errorf("符号 %q 未在文件中找到（包级/方法/局部均无）", old)
	}
	return modeLocal, "", nil
}

// renameSymbol 执行重命名。files 为入口文件；scope=file 只改入口文件，
// pkg 时扩展同目录 .go 文件（不含 _test.go，includeTests 开启时含）。
// 返回改动文件列表。
func renameSymbol(files []string, oldName, newName, scope string, includeTests, dryRun bool) ([]string, error) {
	changed := []string{}
	if oldName == newName {
		return nil, nil
	}
	if scope == "pkg" && len(files) > 0 {
		// --scope pkg：入口文件同目录全部 .go（不含 _test.go，
		// includeTests 开启时含）
		dir := filepath.Dir(files[0])
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		files = nil
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
				continue
			}
			if strings.HasSuffix(e.Name(), "_test.go") && !includeTests {
				continue
			}
			files = append(files, filepath.Join(dir, e.Name()))
		}
	}
	// pkg 模式预扫描：全局声明类别 + new 名跨文件冲突检测
	var global *renameMode
	if scope == "pkg" {
		m := renameMode(-1)
		for _, f := range files {
			fs := token.NewFileSet()
			b, err := os.ReadFile(f)
			if err != nil {
				return nil, err
			}
			file, err := parser.ParseFile(fs, f, b, 0)
			if err != nil {
				return nil, err
			}
			pd := pkgDecls(file)
			if _, ok := pd[oldName]; ok {
				m = modePkg
			}
			if _, dup := pd[newName]; dup && newName != oldName {
				return nil, fmt.Errorf("新名 %q 与包级声明冲突（%s）", newName, f)
			}
		}
		if m >= 0 {
			global = &m
		}
	}
	for _, f := range files {
		fs := token.NewFileSet()
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		file, err := parser.ParseFile(fs, f, b, parser.ParseComments)
		if err != nil {
			return nil, err
		}
		mode, recvType, err := resolveMode(file, oldName, newName, global)
		if err != nil {
			return nil, err
		}
		r := newRenamer(fs, oldName, newName, mode, recvType)
		r.renameFile(file)
		if !r.changed {
			continue
		}
		if dryRun {
			changed = append(changed, f)
			continue
		}
		// 重写文件（ast.File 到源码：用 printer）
		var sb strings.Builder
		if err := printer.Fprint(&sb, fs, file); err != nil {
			return nil, err
		}
		if err := os.WriteFile(f, []byte(sb.String()), 0o644); err != nil {
			return nil, err
		}
		changed = append(changed, f)
	}
	return changed, nil
}

// renameMain 子命令入口。
func renameMain(args []string) {
	scope := "file"
	dryRun := false
	includeTests := false
	rest := []string{}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--scope":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "rename: --scope 需要 pkg|file")
				os.Exit(2)
			}
			scope = args[i]
		case "--dry-run":
			dryRun = true
		case "--include-tests":
			includeTests = true
		default:
			rest = append(rest, args[i])
		}
	}
	if len(rest) < 3 {
		fmt.Fprintln(os.Stderr, "usage: asttool rename <file.go> <old> <new> [--scope pkg|file] [--dry-run]")
		os.Exit(2)
	}
	file, oldName, newName := rest[0], rest[1], rest[2]
	changed, err := renameSymbol([]string{file}, oldName, newName, scope, includeTests, dryRun)
	if err != nil {
		fmt.Fprintln(os.Stderr, "rename:", err)
		os.Exit(1)
	}
	if dryRun {
		for _, f := range changed {
			fmt.Println(f)
		}
		return
	}
	for _, f := range changed {
		fmt.Println("renamed:", f)
	}
	if len(changed) == 0 {
		fmt.Println("no changes")
	}
}
