// ast_split/migrate.go：把 integration SelfContained 测试迁移为 ssa 包
// 单元测试（go/ast 结构化变换，替代 python 正则方案）。
//
// 变换（对每个 Test 函数）：
//   - writeFile(t, filepath.Join(dir, "x"), content) 语句 → 收集进
//     indexFixtureRepo 的 files map（string 字面量经 ast 原样保留）
//   - dir := t.TempDir() / runCLI init / sqlite.Open / NewRepo 语句删除
//   - runCLIOut query trace-forward/backward/value-trace/fields →
//     repo.TraceForward/TraceBackward/GetValueTrace/GetFunctionFields
//   - 引用 out 的断言 strings.Contains(out, X) → traceHas(rows, X)
//   - module 前缀 example.com/<mod> → example.com/mtest
//
// 用法: go run ./tmp/ast_split/migrate.go <out_prefix> <src_files...>
package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"regexp"
	"sort"
	"strings"
)

// headerFor 生成迁移文件的头部（package 名参数化；helpers 依赖
// codeintel 的 domain 包——迁移目标是 codeintel 内测试时使用）。
func headerFor(pkg string) string {
	return `package ` + pkg + `

import (
	"strings"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

// 本文件由 scripts/asttool migrate 从 integration/ 迁移：
// fixture 自建、断言全为 SSA/sqlite 产物——单元测试化，脱离 scip/CLI
// 管道用 indexFixtureRepo 落库后直接 repo 断言。

// traceHas 检查追溯行中 Name/FullPath 含 substr。
func traceHas(rows []*domain.TraceRow, substr string) bool {
	for _, r := range rows {
		if strings.Contains(r.Name, substr) || strings.Contains(r.FullPath, substr) ||
			strings.Contains(r.FuncID, substr) {
			return true
		}
	}
	return false
}

// vtCandHas 检查追溯行中是否含动态候选边标注（Q161 EdgeOrigin）。
func vtCandHas(rows []*domain.TraceRow) bool {
	for _, r := range rows {
		if r.EdgeOrigin != "" || r.DispatchOrigin != "" {
			return true
		}
	}
	return false
}

// ffsHas 检查函数字段摘要行中 FieldPath 含 substr（fields 查询断言）。
func ffsHas(rows []*domain.FunctionFieldSummary, substr string) bool {
	for _, r := range rows {
		if strings.Contains(r.FieldPath, substr) {
			return true
		}
	}
	return false
}
`
}

func migrateMain(args []string) {
	pkg := "ssa"
	for len(args) > 0 && args[0] == "--pkg" {
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "--pkg needs a value")
			os.Exit(2)
		}
		pkg = args[1]
		args = args[2:]
	}
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: migrate [--pkg <name>] <out_prefix> <src_files...>")
		os.Exit(2)
	}
	outPrefix, srcs := args[0], args[1:]
	var out bytes.Buffer
	out.WriteString(headerFor(pkg))
	out.WriteString("\n")
	written := 0
	for _, srcPath := range srcs {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, srcPath, nil, parser.ParseComments)
		if err != nil {
			fmt.Fprintf(os.Stderr, "parse %s: %v\n", srcPath, err)
			os.Exit(1)
		}
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || !strings.HasPrefix(fd.Name.Name, "Test") || fd.Recv != nil {
				continue
			}
			if err := writeTest(&out, fset, fd); err != nil {
				fmt.Fprintf(os.Stderr, "%s: %s: %v\n", srcPath, fd.Name.Name, err)
				os.Exit(1)
			}
			written++
		}
	}
	src, err := format.Source(out.Bytes())
	if err != nil {
		_ = os.WriteFile("/tmp/migrate_debug.go", out.Bytes(), 0o644)
		fmt.Fprintf(os.Stderr, "format: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(outPrefix+".go", src, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s.go (%d tests)\n", outPrefix, written)
}

// writeTest 变换单个测试函数并输出。
func writeTest(out *bytes.Buffer, fset *token.FileSet, fd *ast.FuncDecl) error {
	if fd.Doc != nil {
		for _, c := range fd.Doc.List {
			out.WriteString(c.Text)
			out.WriteString("\n")
		}
	}
	// 函数头：只打印参数列表（FuncType 会重复输出 func 关键字）
	fmt.Fprintf(out, "func %s(t *testing.T) {\n", fd.Name.Name)
	// 逐语句变换：先收集（保持原顺序），再统一输出
	files := map[string]string{}
	body := fd.Body
	st := &migState{}
	if body != nil {
		// 预扫描：测试体内是否已有 repo.Query（其 rows/err 已声明——
		// 后续 runCLIOut 替换须用 = 而非 :=；递归扫 if 块内）
		ast.Inspect(body, func(n ast.Node) bool {
			switch nn := n.(type) {
			case *ast.CallExpr:
				if sel, ok := nn.Fun.(*ast.SelectorExpr); ok {
					if x, ok := sel.X.(*ast.Ident); ok && x.Name == "repo" && sel.Sel.Name == "Query" {
						st.hasRepoQuery = true
					}
				}
			case *ast.BasicLit:
				if nn.Value == `"fields"` {
					st.hasFields = true
				}
			}
			return true
		})
	}
	var stmts []string // 变换后的语句文本（按原顺序；空 = 删除）
	var mod string    // fixture module 前缀（files 收集后计算）
	var fixtureText string // indexFixtureRepo 调用（多行反引号内容不参与语句缩进）
	if body != nil {
		for _, stmt := range body.List {
			if filesOut, ok := transformStmt(fset, stmt, files, st); ok {
				stmts = append(stmts, filesOut)
				continue
			}
			text, err := stmtText(fset, stmt)
			if err != nil {
				return err
			}
			rowsName := "rows"
			if st.hasRepoQuery {
				rowsName = "vrows"
			}
			stmts = append(stmts, replaceOut(text, rowsName, st.hasFields))
		}
		// module 前缀（files 已收集完成；fixture 源码/yaml 与 body 统一 mtest）
		mod = moduleOf(files)
		// fixture 调用插在首个非空语句之前
		if len(files) > 0 {
			// module 前缀统一 mtest（源码与 yaml 的 iface 路径都要替换）
			if mod != "" && mod != "example.com/mtest" {
				for k, v := range files {
					files[k] = strings.ReplaceAll(v, mod, "example.com/mtest")
				}
			}
			var keys []string
			for k := range files {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			var buf bytes.Buffer
			fmt.Fprintf(&buf, "repo := indexFixtureRepo(t, map[string]string{\n")
			for _, k := range keys {
				fmt.Fprintf(&buf, "\t%q: %s,\n", k, files[k])
			}
			fmt.Fprintf(&buf, "})")
			fixtureText = buf.String()
		}
	}
	if fixtureText != "" {
		if mod != "" && mod != "example.com/mtest" {
			fixtureText = strings.ReplaceAll(fixtureText, mod, "example.com/mtest")
		}
		out.WriteString("\t")
		out.WriteString(fixtureText)
		out.WriteString("\n")
	}
	for _, text := range stmts {
		if text == "" {
			continue
		}
		if mod != "" && mod != "example.com/mtest" {
			text = strings.ReplaceAll(text, mod, "example.com/mtest")
			// 裸包名（断言里 "dyncand.Writer" 形态——字符串字面量内）
			bare := mod[strings.LastIndex(mod, "/")+1:]
			text = strings.ReplaceAll(text, `"`+bare+`.`, `"mtest.`)
		}
		out.WriteString("\t")
		out.WriteString(strings.ReplaceAll(text, "\n", "\n\t"))
		out.WriteString("\n")
	}
	out.WriteString("}\n\n")
	return nil
}

// migState 迁移过程的跨语句状态。
type migState struct {
	hasRepoQuery  bool // 测试体内已有 repo.Query（rows/err 已声明）
	hasFields     bool // 测试内有 fields 查询（断言用 ffsHas）
	dropErrCheck  bool // 上一条删了 db, err := sqlite.Open——其 err 检查待删
}

// transformStmt 处理单个语句：返回 (输出文本, 已消费)。已消费 = 删除
// 或替换（文本直接写入 out）。kept = false 表示保留原语句。
func transformStmt(fset *token.FileSet, stmt ast.Stmt, files map[string]string, st *migState) (string, bool) {
	switch s := stmt.(type) {
	case *ast.ExprStmt:
		call, ok := s.X.(*ast.CallExpr)
		if !ok {
			return "", false
		}
		if isWriteFile(call) {
			path, content := writeFileArgs(call)
			files[path] = content
			return "", true
		}
	case *ast.AssignStmt:
		// dir := t.TempDir() / db, err := sqlite.Open(dir) / repo := sqlite.NewRepo(db)
		if len(s.Lhs) == 1 {
			if ident, ok := s.Lhs[0].(*ast.Ident); ok && ident.Name == "dir" {
				return "", true
			}
			if ident, ok := s.Lhs[0].(*ast.Ident); ok && ident.Name == "repo" {
				return "", true
			}
		}
		if len(s.Lhs) == 2 {
			if ids, ok := s.Lhs[0].(*ast.Ident); ok && ids.Name == "db" {
				st.dropErrCheck = true // 伴随的 if err != nil 检查待删
				return "", true
			}
		}
		// code, out := runCLIOut(...) → rows, err := repo.X(...)
		if len(s.Lhs) == 2 && len(s.Rhs) == 1 {
			if call, ok := s.Rhs[0].(*ast.CallExpr); ok {
				// 符号保持原代码（:= 或 =）；rowsName 与 repo.Query 的
				// rows（*sql.Rows）类型冲突时用 vrows
				rowsName := "rows"
				if st.hasRepoQuery {
					rowsName = "vrows"
				}
				if len(s.Lhs) == 2 {
					if id, ok := s.Lhs[1].(*ast.Ident); ok && id.Name == "_" {
						rowsName = "_" // code, _ := 形态：丢弃输出
					}
				}
				if repl, ok := runCLIOutQuery(call, s.Tok == token.DEFINE, rowsName); ok {
					return repl + "\n\tif err != nil {\n\t\tt.Fatalf(\"query: %v\", err)\n\t}\n", true
				}
			}
		}
	case *ast.IfStmt:
		// if code := runCLI(t, "init", ...); code != 0 {...} → 删除
		if s.Init != nil {
			if assign, ok := s.Init.(*ast.AssignStmt); ok && len(assign.Lhs) == 1 {
				if ident, ok := assign.Lhs[0].(*ast.Ident); ok && ident.Name == "code" {
					return "", true
				}
			}
		}
		// if code != 0 {...} → 删除（runCLIOut 的伴随检查）
		if s.Init == nil && s.Cond != nil {
			if bin, ok := s.Cond.(*ast.BinaryExpr); ok && bin.Op == token.NEQ {
				if ident, ok := bin.X.(*ast.Ident); ok && ident.Name == "code" {
					return "", true
				}
				// if err != nil {...} → 删除（sqlite.Open 被删后的伴随检查）
				if st.dropErrCheck {
					if ident, ok := bin.X.(*ast.Ident); ok && ident.Name == "err" {
						st.dropErrCheck = false
						return "", true
					}
				}
			}
			// if !scipGoAvailable() {...} → 删除（skip 保护）
			if un, ok := s.Cond.(*ast.UnaryExpr); ok && un.Op == token.NOT {
				if call, ok := un.X.(*ast.CallExpr); ok {
					if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "scipGoAvailable" {
						return "", true
					}
				}
			}
		}
	case *ast.DeferStmt:
		// defer db.Close() → 删除
		if call, ok := s.Call.Fun.(*ast.SelectorExpr); ok {
			if ident, ok := call.X.(*ast.Ident); ok && ident.Name == "db" {
				return "", true
			}
		}
	}
	return "", false
}

// isWriteFile writeFile(t, filepath.Join(dir, "x"), content) 调用。
func isWriteFile(call *ast.CallExpr) bool {
	ident, ok := call.Fun.(*ast.Ident)
	if !ok || ident.Name != "writeFile" || len(call.Args) != 3 {
		return false
	}
	join, ok := call.Args[1].(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := join.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	x, ok := sel.X.(*ast.Ident)
	return ok && x.Name == "filepath" && sel.Sel.Name == "Join"
}

// writeFileArgs 提取 path 与 content（BasicLit 原样——含引号/反引号）。
func writeFileArgs(call *ast.CallExpr) (string, string) {
	join := call.Args[1].(*ast.CallExpr)
	path := strings.Trim(join.Args[1].(*ast.BasicLit).Value, `"`)
	lit, ok := call.Args[2].(*ast.BasicLit)
	if !ok {
		return "", ""
	}
	return path, lit.Value
}

// runCLIOutQuery code, out := runCLIOut(t, "query", "X", ...) →
// repo 调用文本。返回 (替换文本, true)。
func runCLIOutQuery(call *ast.CallExpr, isDefine bool, rowsName string) (string, bool) {
	if len(call.Args) < 4 {
		return "", false
	}
	subLit, ok := call.Args[2].(*ast.BasicLit)
	if !ok {
		return "", false
	}
	op := "="
	if isDefine {
		op = ":="
	}
	sub := strings.Trim(subLit.Value, `"`)
	switch sub {
	case "trace-forward", "trace-backward":
		method := "TraceForward"
		if sub == "trace-backward" {
			method = "TraceBackward"
		}
		// args: field, "--func", id, "--repo", dir
		if len(call.Args) < 7 {
			return "", false
		}
		field := exprText(call.Args[3])
		id := exprText(call.Args[5])
		return fmt.Sprintf("\t%s, err %s repo.%s(%s, domain.CanonicalID(%s), 8)",
			rowsName, op, method, field, id), true
	case "value-trace":
		id := exprText(call.Args[3])
		// minConf：原 CLI 带 --min-conf 0 才展开候选（默认 1.0 剪枝）
		minConf := "1.0"
		for _, a := range call.Args[4:] {
			if lit, ok := a.(*ast.BasicLit); ok && lit.Value == `"--min-conf"` {
				minConf = "0"
			}
		}
		return fmt.Sprintf("\t%s, err %s repo.GetValueTrace(domain.CanonicalID(%s), 8, %s, false)", rowsName, op, id, minConf), true
	case "fields":
		id := exprText(call.Args[3])
		return fmt.Sprintf("\tffsRows, err %s repo.GetFunctionFields(domain.CanonicalID(%s))", op, id), true
	}
	return "", false
}

// exprText 表达式源码文本（printer 打印）。
func exprText(e ast.Expr) string {
	var buf bytes.Buffer
	_ = printer.Fprint(&buf, token.NewFileSet(), e)
	return buf.String()
}

// replaceOut 断言文本：CLI 输出 out → rows（查询已改为 repo 调用）。
var outSliceRe = regexp.MustCompile(`out\[:min\(len\(out\), \d+\)\]`)

func replaceOut(text, rowsName string, hasFields bool) string {
	has := "traceHas"
	if hasFields {
		has = "ffsHas"
		rowsName = "ffsRows"
	}
	text = outSliceRe.ReplaceAllString(text, rowsName)
	text = strings.ReplaceAll(text, "strings.Contains(out,", has+"("+rowsName+",")
	// 动态候选标注断言（CLI 文本 "[动态候选]" → rows 的 EdgeOrigin 检查）
	text = regexp.MustCompile(`traceHas\((rows|vrows), "动态候选"\)`).ReplaceAllString(text, `vtCandHas($1)`)
	// db 声明已删（fixture 改为 repo）——遗留的 db.Query/QueryRow 走 repo
	text = strings.ReplaceAll(text, "db.Query", "repo.Query")
	// 调试输出参数由 string(out) 变为 slice——%q/%s 统一改 %v（vet 格式检查）
	if strings.Contains(text, "rows") || strings.Contains(text, "ffsRows") {
		text = strings.ReplaceAll(text, `%q`, `%v`)
		text = regexp.MustCompile(`%s", (rows|vrows|ffsRows)\)`).ReplaceAllString(text, `%v", $1)`)
	}
	return text
}

// stmtText 语句源码文本。
func stmtText(fset *token.FileSet, s ast.Stmt) (string, error) {
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, fset, s); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// moduleOf 从 go.mod 内容提取 module 名（content 是含引号的原样字面量）。
var modRe = regexp.MustCompile(`module\s+(example\.com/\w+)`)

func moduleOf(files map[string]string) string {
	if m := modRe.FindStringSubmatch(files["go.mod"]); m != nil {
		return m[1]
	}
	return ""
}
