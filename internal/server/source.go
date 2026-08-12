// source.go 提供 /api/source：按需读取仓库文件并解析出函数/方法的
// 源码区间（go/parser 定位声明，不依赖索引时的行号精确性）。
package server

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/schaepher/codeintel/internal/domain"
	"github.com/schaepher/codeintel/internal/logging"
)

// handleSource 返回函数/方法的源码文本：GET /api/source?id=<canonical id>
func (s *Server) handleSource(w http.ResponseWriter, r *http.Request) {
	logger := logging.FromContext(s.ctx)
	logger.Debug("enter (Server).handleSource")
	defer logger.Debug("exit (Server).handleSource")
	id := r.URL.Query().Get("id")
	if id == "" {
		writeErr(w, http.StatusBadRequest, "missing id")
		return
	}
	n, err := s.repo.GetSymbol(domain.CanonicalID(id))
	if err != nil {
		writeErr(w, http.StatusNotFound, "symbol not found: "+id)
		return
	}
	if n.Kind != domain.KindFunction && n.Kind != domain.KindMethod {
		writeErr(w, http.StatusBadRequest, "source only for function/method: "+id)
		return
	}
	code, line, err := extractFuncSource(s.root, n)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, map[string]any{
		"file": n.FilePath,
		"line": line,
		"code": code,
	})
}

// extractFuncSource 读取仓库文件并解析出函数/方法的源码文本。
// 匹配策略：LineStart 精确匹配 → 行范围包含 → 按名称（方法名 (T).m
// 解析接收者）匹配，保证文件修改后行号漂移仍能定位。
func extractFuncSource(root string, n *domain.CodeEntity) (code string, line int, err error) {
	if n.FilePath == "" {
		return "", 0, fmt.Errorf("node has no file")
	}
	full, err := filepath.Abs(filepath.Join(root, n.FilePath))
	if err != nil {
		return "", 0, err
	}
	// 防目录穿越：解析结果必须仍在仓库根内
	cleanRoot, err := filepath.Abs(root)
	if err != nil {
		return "", 0, err
	}
	if full != cleanRoot && !strings.HasPrefix(full, cleanRoot+string(os.PathSeparator)) {
		return "", 0, fmt.Errorf("path escapes repo root: %s", n.FilePath)
	}
	content, err := os.ReadFile(full)
	if err != nil {
		return "", 0, fmt.Errorf("read %s: %w", n.FilePath, err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, full, content, parser.SkipObjectResolution)
	if err != nil {
		return "", 0, fmt.Errorf("parse %s: %w", n.FilePath, err)
	}
	decl := matchFuncDecl(fset, f, n)
	if decl == nil {
		return "", 0, fmt.Errorf("function not found in %s", n.FilePath)
	}
	startOff := fset.Position(decl.Pos()).Offset
	endOff := fset.Position(decl.End()).Offset
	return string(content[startOff:endOff]), fset.Position(decl.Pos()).Line, nil
}

// matchFuncDecl 按 LineStart → 行范围 → 名称 顺序匹配函数/方法声明。
func matchFuncDecl(fset *token.FileSet, f *ast.File, n *domain.CodeEntity) *ast.FuncDecl {
	decls := make([]*ast.FuncDecl, 0)
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok {
			decls = append(decls, fd)
		}
	}
	// 1. LineStart 精确匹配
	for _, fd := range decls {
		if fset.Position(fd.Pos()).Line == n.LineStart {
			return fd
		}
	}
	// 2. 行范围包含（索引行号可能指向声明内其他行）
	for _, fd := range decls {
		start := fset.Position(fd.Pos()).Line
		end := fset.Position(fd.End()).Line
		if n.LineStart >= start && n.LineStart <= end {
			return fd
		}
	}
	// 3. 名称匹配（文件已修改，行号漂移）
	return findFuncByName(f, n)
}

// findFuncByName 按节点名称匹配：函数名为裸名；方法名 (T).m 解析接收者。
func findFuncByName(f *ast.File, n *domain.CodeEntity) *ast.FuncDecl {
	name := n.Name
	recvName, methodName := "", name
	if i := strings.Index(name, ")."); i >= 0 {
		recvName = strings.TrimPrefix(name[:i+1], "(")
		methodName = name[i+2:]
	}
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Name.Name != methodName {
			continue
		}
		if recvName != "" {
			if fd.Recv == nil || len(fd.Recv.List) == 0 {
				continue
			}
			rt := fd.Recv.List[0].Type
			if st, ok := rt.(*ast.StarExpr); ok {
				rt = st.X
			}
			if id, ok := rt.(*ast.Ident); !ok || id.Name != recvName {
				continue
			}
		} else if fd.Recv != nil {
			continue
		}
		return fd
	}
	return nil
}
