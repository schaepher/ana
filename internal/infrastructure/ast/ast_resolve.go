package ast

import (
	"go/ast"
	"go/types"
	"path/filepath"
	"strings"

	"github.com/schaepher/codeintel/internal/canonicalizer"
	"github.com/schaepher/codeintel/internal/domain"
	"go.uber.org/zap"
	"golang.org/x/tools/go/packages"
)

// resolveCallee 将调用表达式解析为被调用的 *types.Func。
func resolveCallee(info *types.Info, fun ast.Expr) (*types.Func, bool) {
	logger := zap.L()
	logger.Debug("enter resolveCallee")
	defer logger.Debug("exit resolveCallee")
	var id *ast.Ident
	switch f := fun.(type) {
	case *ast.Ident:
		id = f
	case *ast.SelectorExpr:
		id = f.Sel
	default:
		return nil, false
	}
	obj, ok := info.Uses[id]
	if !ok {
		return nil, false
	}
	fn, ok := obj.(*types.Func)
	return fn, ok
}

// findCallerDecl 返回调用点所属的最近函数声明。
func findCallerDecl(stack []ast.Node) *ast.FuncDecl {
	logger := zap.L()
	logger.Debug("enter findCallerDecl")
	defer logger.Debug("exit findCallerDecl")
	for i := len(stack) - 1; i >= 0; i-- {
		if fd, ok := stack[i].(*ast.FuncDecl); ok {
			return fd
		}
	}
	return nil
}

// isArgCall 判断 call 是否处于另一个调用点的参数位置（嵌套调用，
// 如 A(B(C())) 里的 B(C())）。参数位置的调用由外层处理为"持有返回
// 参数"链，不建 calls。
func isArgCall(stack []ast.Node, call *ast.CallExpr) bool {
	logger := zap.L()
	logger.Debug("enter isArgCall")
	defer logger.Debug("exit isArgCall")
	for i := len(stack) - 1; i >= 0; i-- {
		outer, ok := stack[i].(*ast.CallExpr)
		if !ok || outer == call {
			continue
		}
		for _, arg := range outer.Args {
			if arg == call {
				return true
			}
		}
		return false
	}
	return false
}

// findFuncDecl 通过位置查找 *types.Func 对应的 FuncDecl（同包内）。
func findFuncDecl(pkg *packages.Package, fn *types.Func) *ast.FuncDecl {
	pos := pkg.Fset.PositionFor(fn.Pos(), false)
	if pos.Filename == "" {
		return nil
	}
	for _, f := range pkg.Syntax {
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok {
				continue
			}
			p := pkg.Fset.PositionFor(fd.Pos(), false)
			if p.Filename == pos.Filename && p.Line == pos.Line {
				return fd
			}
		}
	}
	return nil
}

// derefNamed 解引用指针后取具名类型。
func derefNamed(t types.Type) (*types.Named, bool) {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	n, ok := t.(*types.Named)
	return n, ok
}

// isInterfaceType 判断具名类型是否为接口。
func isInterfaceType(n *types.Named) bool {
	if n == nil {
		return false
	}
	_, ok := n.Underlying().(*types.Interface)
	return ok
}

// isInterfaceMethod 判断 *types.Func 是否为接口方法（接收者类型是接口）。
// 接口方法不作为独立节点：SCIP 适配器不建、AST 适配器调用处也不建。
func isInterfaceMethod(fn *types.Func) bool {
	logger := zap.L()
	logger.Debug("enter isInterfaceMethod")
	defer logger.Debug("exit isInterfaceMethod")
	if fn == nil || fn.Pkg() == nil {
		return false
	}
	sig, _ := fn.Type().(*types.Signature)
	if sig == nil {
		return false
	}
	recv := sig.Recv()
	if recv == nil {
		return false
	}
	t := recv.Type()
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	_, ok = named.Underlying().(*types.Interface)
	return ok
}

// fnID 计算函数/方法的 canonical ID 与领域种类。
// 返回值 (id, kind, ok)。
func fnID(fn *types.Func) (domain.CanonicalID, domain.EntityKind) {
	logger := zap.L()
	logger.Debug("enter fnID")
	defer logger.Debug("exit fnID")
	if fn == nil || fn.Pkg() == nil {
		return "", ""
	}
	path := fn.Pkg().Path()
	sig, _ := fn.Type().(*types.Signature)
	if sig == nil {
		return "", ""
	}
	if recv := sig.Recv(); recv != nil {
		t := recv.Type()
		if p, ok := t.(*types.Pointer); ok {
			t = p.Elem()
		}
		named, ok := t.(*types.Named)
		if !ok {
			return "", ""
		}
		return canonicalizer.GoSymbolID(path, canonicalizer.MethodName(named.Obj().Name(), fn.Name())), domain.KindMethod
	}
	return canonicalizer.GoSymbolID(path, fn.Name()), domain.KindFunction
}

// nodeFor 为函数/方法生成轻量节点（ID 与 SCIP 一致，行号/文件来自位置信息，
// signature 由 go/types 生成，与 SCIP 节点通过 properties 合并）。
func nodeFor(repo *domain.Repository, pkg *packages.Package, fn *types.Func, id domain.CanonicalID,
	kind domain.EntityKind, extra map[string]bool) *domain.CodeEntity {
	logger := zap.L()
	logger.Debug("enter nodeFor")
	defer logger.Debug("exit nodeFor")
	n := &domain.CodeEntity{ID: id, Kind: kind}
	if fn != nil && fn.Pkg() != nil {
		pos := pkg.Fset.PositionFor(fn.Pos(), false)
		n.FilePath = relPath(repo.Path, pos.Filename)
		n.LineStart = pos.Line
		n.LineEnd = pos.Line

		if kind == domain.KindMethod {
			sig, _ := fn.Type().(*types.Signature)
			if sig != nil && sig.Recv() != nil {
				t := sig.Recv().Type()
				if p, ok := t.(*types.Pointer); ok {
					t = p.Elem()
				}
				if named, ok := t.(*types.Named); ok {
					n.Name = canonicalizer.MethodName(named.Obj().Name(), fn.Name())
				}
			}
		} else {
			n.Name = fn.Name()
		}
		n.Properties = map[string]any{

			"signature": types.ObjectString(fn, types.RelativeTo(pkg.Types)),
		}
		for flag := range extra {
			n.Properties[flag] = "true"
		}
	}
	return n
}

// ensurePackageNode 保障包节点存在（SCIP 已建则跳过）。
// 注意：加载失败/无源码的包（pkg.Syntax 为空，如编译错误的包或测试变体）
// 仍会创建节点，只是不带 file_path，避免 index out of range。
func ensurePackageNode(repo *domain.Repository, pkg *packages.Package, emit domain.EmitFunc) error {
	logger := zap.L()
	logger.Debug("enter ensurePackageNode")
	defer logger.Debug("exit ensurePackageNode")
	n := &domain.CodeEntity{
		ID:   packageID(pkg.PkgPath),
		Kind: domain.KindPackage,
		Name: pathBase(pkg.PkgPath),
	}
	if len(pkg.Syntax) > 0 {
		n.FilePath = relPath(repo.Path, pkg.Fset.PositionFor(pkg.Syntax[0].Pos(), false).Filename)
	}
	return emit(domain.Item{Node: n})
}
func packageID(pkgPath string) domain.CanonicalID {
	logger := zap.L()
	logger.Debug("enter packageID")
	defer logger.Debug("exit packageID")
	return canonicalizer.GoSymbolID(pkgPath, pathBase(pkgPath))
}
func pathBase(p string) string {
	logger := zap.L()
	logger.Debug("enter pathBase")
	defer logger.Debug("exit pathBase")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// relPath 将绝对路径转为仓库相对路径；仓库外文件返回空串。
func relPath(repoPath, abs string) string {
	logger := zap.L()
	logger.Debug("enter relPath")
	defer logger.Debug("exit relPath")
	if abs == "" {
		return ""
	}
	rel, err := filepath.Rel(repoPath, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return ""
	}
	return filepath.ToSlash(rel)
}

// isInModule 判断包路径是否属于任一被索引 module（自身或子包；P2-3
// 多 go.mod——任一 module 前缀匹配即项目内）。
func isInModule(pkgPath string, modules []string) bool {
	logger := zap.L()
	logger.Debug("enter isInModule")
	defer logger.Debug("exit isInModule")
	for _, m := range modules {
		if m == "" {
			continue
		}
		if pkgPath == m || strings.HasPrefix(pkgPath, m+"/") {
			return true
		}
	}
	return false
}
