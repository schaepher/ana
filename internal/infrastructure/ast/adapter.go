// Package ast 实现调用图适配器（对应 TD.md 的 CodeGraph 适配器角色，置信度 0.8）。
// 基于 golang.org/x/tools/go/packages 的 AST + 类型信息，纯 Go 无外部进程：
//   - CALLS 边：调用者函数 → 被调用函数/方法（精确调用点）
//   - IMPORTS 边：包 → 直接依赖的项目内包
package ast

import (
	"context"
	"fmt"
	"go/ast"
	"go/types"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/packages"

	"codeintel/internal/canonicalizer"
	"codeintel/internal/domain"
)

// Adapter 是基于 go/packages 的调用图分析器。
type Adapter struct{}

var _ domain.IndexerPort = (*Adapter)(nil)

// Name 实现 IndexerPort。
func (a *Adapter) Name() string { return "codegraph" }

// Index 加载仓库全部包并产出 CALLS / IMPORTS 边。
func (a *Adapter) Index(ctx context.Context, repo *domain.Repository, emit domain.EmitFunc) error {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax |
			packages.NeedTypes | packages.NeedTypesInfo | packages.NeedImports | packages.NeedDeps,
		Dir:  repo.Path,
		Tests: true,
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return fmt.Errorf("go/packages load: %w", err)
	}
	packages.PrintErrors(pkgs) // 诊断信息打到 stderr，不中断

	for _, pkg := range pkgs {
		if !isInModule(pkg.PkgPath, repo.Module) {
			continue // 仅处理项目内包
		}
		if err := a.processPackage(repo, pkg, emit); err != nil {
			return err
		}
	}
	return nil
}

func (a *Adapter) processPackage(repo *domain.Repository, pkg *packages.Package, emit domain.EmitFunc) error {
	if err := ensurePackageNode(repo, pkg, emit); err != nil {
		return err
	}

	// IMPORTS 边：直接依赖的项目内包
	for importPath := range pkg.Imports {
		if !isInModule(importPath, repo.Module) {
			continue
		}
		if err := emit(domain.Item{Fact: &domain.Fact{
			SourceID:   packageID(pkg.PkgPath),
			TargetID:   packageID(importPath),
			Kind:       domain.FactImports,
			ToolSource: domain.ToolCodeGraph,
			Confidence: 0.8,
		}}); err != nil {
			return err
		}
	}

	for _, f := range pkg.Syntax {
		if err := a.processFile(repo, pkg, f, emit); err != nil {
			return err
		}
	}
	return nil
}

// processFile 遍历单个 AST：定位每个调用点，连接调用者与被调用者。
func (a *Adapter) processFile(repo *domain.Repository, pkg *packages.Package, f *ast.File, emit domain.EmitFunc) error {
	filePath := relPath(repo.Path, pkg.Fset.PositionFor(f.Pos(), false).Filename)
	if filePath == "" {
		return nil
	}

	var stack []ast.Node
	ast.Inspect(f, func(n ast.Node) bool {
		if n == nil {
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			return true
		}
		stack = append(stack, n)

		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		callee, ok := resolveCallee(pkg.TypesInfo, call.Fun)
		if !ok || callee.Pkg() == nil || !isInModule(callee.Pkg().Path(), repo.Module) {
			return true // 内建/外部函数不建边
		}
		callerDecl := findCallerDecl(stack)
		if callerDecl == nil {
			return true // 包级初始化中的调用，MVP 不建边
		}
		caller, ok := pkg.TypesInfo.Defs[callerDecl.Name].(*types.Func)
		if !ok {
			return true
		}
		callerID, callerKind := fnID(caller)
		if callerID == "" {
			return true
		}
		calleeID, calleeKind := fnID(callee)
		if calleeID == "" || calleeID == callerID {
			return true
		}
		// 保障两端节点存在（INSERT OR IGNORE，不覆盖 SCIP 的完整节点）
		if err := emit(domain.Item{Node: nodeFor(repo, pkg, caller, callerID, callerKind)}); err != nil {
			return false
		}
		if err := emit(domain.Item{Node: nodeFor(repo, pkg, callee, calleeID, calleeKind)}); err != nil {
			return false
		}
		if err := emit(domain.Item{Fact: &domain.Fact{
			SourceID:   callerID,
			TargetID:   calleeID,
			Kind:       domain.FactCalls,
			ToolSource: domain.ToolCodeGraph,
			Confidence: 0.8,
			Metadata:   map[string]any{"line_num": pkg.Fset.PositionFor(call.Pos(), false).Line},
		}}); err != nil {
			return false
		}
		return true
	})
	return nil
}

// resolveCallee 将调用表达式解析为被调用的 *types.Func。
func resolveCallee(info *types.Info, fun ast.Expr) (*types.Func, bool) {
	var id *ast.Ident
	switch f := fun.(type) {
	case *ast.Ident:
		id = f
	case *ast.SelectorExpr:
		id = f.Sel
	default:
		return nil, false // 函数值调用/类型转换等，MVP 不追踪
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
	for i := len(stack) - 1; i >= 0; i-- {
		if fd, ok := stack[i].(*ast.FuncDecl); ok {
			return fd
		}
	}
	return nil
}

// fnID 计算函数/方法的 canonical ID 与领域种类。
// 返回值 (id, kind, ok)。
func fnID(fn *types.Func) (domain.CanonicalID, domain.EntityKind) {
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
			return "", "" // 匿名结构体上的方法，跳过
		}
		return canonicalizer.GoSymbolID(path, canonicalizer.MethodName(named.Obj().Name(), fn.Name())), domain.KindMethod
	}
	return canonicalizer.GoSymbolID(path, fn.Name()), domain.KindFunction
}

// nodeFor 为函数/方法生成轻量节点（ID 与 SCIP 一致，行号/文件来自位置信息，
// signature 由 go/types 生成，与 SCIP 节点通过 properties 合并）。
func nodeFor(repo *domain.Repository, pkg *packages.Package, fn *types.Func, id domain.CanonicalID, kind domain.EntityKind) *domain.CodeEntity {
	n := &domain.CodeEntity{ID: id, Kind: kind}
	if fn != nil && fn.Pkg() != nil {
		pos := pkg.Fset.PositionFor(fn.Pos(), false)
		n.FilePath = relPath(repo.Path, pos.Filename)
		n.LineStart = pos.Line
		n.LineEnd = pos.Line
		// 方法/函数名：方法为 (T).m，函数为 m
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
			// ObjectString 对方法包含接收者：func (s *Service) CreatePayment(req string) error
			"signature": types.ObjectString(fn, types.RelativeTo(pkg.Types)),
		}
	}
	return n
}

// ensurePackageNode 保障包节点存在（SCIP 已建则跳过）。
func ensurePackageNode(repo *domain.Repository, pkg *packages.Package, emit domain.EmitFunc) error {
	return emit(domain.Item{Node: &domain.CodeEntity{
		ID:       packageID(pkg.PkgPath),
		Kind:     domain.KindPackage,
		Name:     pathBase(pkg.PkgPath),
		FilePath: relPath(repo.Path, pkg.Fset.PositionFor(pkg.Syntax[0].Pos(), false).Filename),
	}})
}

func packageID(pkgPath string) domain.CanonicalID {
	return canonicalizer.GoSymbolID(pkgPath, pathBase(pkgPath))
}

func pathBase(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// relPath 将绝对路径转为仓库相对路径；仓库外文件返回空串。
func relPath(repoPath, abs string) string {
	if abs == "" {
		return ""
	}
	rel, err := filepath.Rel(repoPath, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return ""
	}
	return filepath.ToSlash(rel)
}

func isInModule(pkgPath, module string) bool {
	return pkgPath == module || strings.HasPrefix(pkgPath, module+"/")
}
