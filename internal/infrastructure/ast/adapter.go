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

	"github.com/schaepher/codeintel/internal/canonicalizer"
	"github.com/schaepher/codeintel/internal/domain"
	"github.com/schaepher/codeintel/internal/logging"
	"go.uber.org/zap"
)

// Adapter 是基于 go/packages 的调用图分析器。
type Adapter struct{}

var _ domain.IndexerPort = (*Adapter)(nil)

// Name 实现 IndexerPort。
func (a *Adapter) Name() string {
	logger := zap.L()
	logger.Debug("enter (Adapter).Name")
	defer logger.Debug("exit (Adapter).Name")
	return "codegraph"
}

// Index 加载仓库全部包并产出 CALLS / IMPORTS 边。
func (a *Adapter) Index(ctx context.Context, repo *domain.Repository, emit domain.EmitFunc) error {
	logger := logging.FromContext(ctx)
	logger.Debug("enter (Adapter).Index")
	defer logger.Debug("exit (Adapter).Index")
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax |
			packages.NeedTypes | packages.NeedTypesInfo | packages.NeedImports | packages.NeedDeps,
		Dir: repo.Path,
		// Tests 默认 false：不加载 _test.go（测试符号不入图）
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return fmt.Errorf("go/packages load: %w", err)
	}
	packages.PrintErrors(pkgs) // 诊断信息打到 stderr，不中断

	// 服务入口标记：函数若调用 net/http 或 grpc 包，标记 serves_http / serves_grpc
	serviceFlags := map[domain.CanonicalID]map[string]bool{}
	// 预收集 .pb.go 中定义的 RegisterXxxServer（gRPC 注册函数）。
	// 注意：不能用调用点所在包的 Fset 解析被调用函数的位置（跨包偏移
	// 不匹配），须在各自包内收集。
	registerServers := collectRegisterServers(pkgs, repo.Module)

	for _, pkg := range pkgs {
		if !isInModule(pkg.PkgPath, repo.Module) {
			continue // 仅处理项目内包
		}
		if err := a.processPackage(repo, pkg, emit, serviceFlags, registerServers); err != nil {
			return err
		}
	}
	return nil
}

// collectRegisterServers 遍历项目内包，收集定义在 .pb.go 中、函数名匹配
// RegisterXxxServer 的注册函数（key: "pkgPath:funcName"）。
func collectRegisterServers(pkgs []*packages.Package, module string) map[string]bool {
	out := map[string]bool{}
	for _, pkg := range pkgs {
		if !isInModule(pkg.PkgPath, module) {
			continue
		}
		for _, f := range pkg.Syntax {
			file := pkg.Fset.PositionFor(f.Pos(), false).Filename
			if !strings.HasSuffix(file, ".pb.go") {
				continue
			}
			for _, decl := range f.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Recv != nil {
					continue
				}
				if isRegisterServerName(fn.Name.Name) {
					out[pkg.PkgPath+":"+fn.Name.Name] = true
				}
			}
		}
	}
	return out
}

func (a *Adapter) processPackage(repo *domain.Repository, pkg *packages.Package, emit domain.EmitFunc,
	serviceFlags map[domain.CanonicalID]map[string]bool, registerServers map[string]bool) error {
	logger := zap.L()
	logger.Debug("enter (Adapter).processPackage")
	defer logger.Debug("exit (Adapter).processPackage")
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
		if err := a.processFile(repo, pkg, f, emit, serviceFlags, registerServers); err != nil {
			return err
		}
	}
	return nil
}

// processFile 遍历单个 AST：定位每个调用点，连接调用者与被调用者。
func (a *Adapter) processFile(repo *domain.Repository, pkg *packages.Package, f *ast.File, emit domain.EmitFunc,
	serviceFlags map[domain.CanonicalID]map[string]bool, registerServers map[string]bool) error {
	logger := zap.L()
	logger.Debug("enter (Adapter).processFile")
	defer logger.Debug("exit (Adapter).processFile")
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
		if !ok {
			return true
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
		// 服务入口标记：调用 net/http / grpc 包的函数作为顶层入口。
		// 注意：外部调用点随后会直接 return（不建 CALLS 边），因此标记
		// fires 时必须立即 emit 节点，否则节点永远不会带上标记。
		if callee.Pkg() != nil {
			p := callee.Pkg().Path()
			flags := serviceFlags[callerID]
			if flags == nil {
				flags = map[string]bool{}
				serviceFlags[callerID] = flags
			}
			marked := false
			if p == "net/http" || strings.HasPrefix(p, "net/http/") {
				if !flags["serves_http"] {
					flags["serves_http"] = true
					marked = true
				}
			}
			if p == "google.golang.org/grpc" || strings.HasPrefix(p, "google.golang.org/grpc/") {
				if !flags["serves_grpc"] {
					flags["serves_grpc"] = true
					marked = true
				}
			}
			// gRPC 服务注册：protoc 生成的 RegisterXxxServer（定义在 .pb.go），
			// 其第二个参数是服务实现，作为顶层服务入口
			if registerServers[callee.Pkg().Path()+":"+callee.Name()] {
				if !flags["serves_grpc"] {
					flags["serves_grpc"] = true
					marked = true
				}
				if impl := serviceImplNode(pkg, call, repo); impl != nil {
					if err := emit(domain.Item{Node: impl}); err != nil {
						return false
					}
				}
			}
			if marked {
				if err := emit(domain.Item{Node: nodeFor(repo, pkg, caller, callerID, callerKind, serviceFlags[callerID])}); err != nil {
					return false
				}
			}
		}
		if callee.Pkg() == nil || !isInModule(callee.Pkg().Path(), repo.Module) {
			return true // 内建/外部函数不建边
		}
		calleeID, calleeKind := fnID(callee)
		if calleeID == "" || calleeID == callerID {
			return true
		}
		// 保障两端节点存在（INSERT OR IGNORE，不覆盖 SCIP 的完整节点）
		if err := emit(domain.Item{Node: nodeFor(repo, pkg, caller, callerID, callerKind, serviceFlags[callerID])}); err != nil {
			return false
		}
		if err := emit(domain.Item{Node: nodeFor(repo, pkg, callee, calleeID, calleeKind, nil)}); err != nil {
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

// isRegisterServerName 判断函数名是否匹配 protoc 生成惯例 RegisterXxxServer。
func isRegisterServerName(name string) bool {
	return len(name) > len("RegisterServer") &&
		strings.HasPrefix(name, "Register") &&
		strings.HasSuffix(name, "Server")
}

// serviceImplNode 提取 RegisterXxxServer 调用的第二个参数（服务实现），
// 生成标记 serves_grpc 的节点（作为顶层服务入口）。参数形态支持：
//
//	pb.RegisterGreeterServer(s, &greeterImpl{})   // 复合字面量
//	pb.RegisterGreeterServer(s, newGreeterServer()) // 构造函数
//	pb.RegisterGreeterServer(s, impl)               // 变量
//
// 返回 nil 表示无法解析为项目内类型。
func serviceImplNode(pkg *packages.Package, call *ast.CallExpr, repo *domain.Repository) *domain.CodeEntity {
	if len(call.Args) < 2 {
		return nil
	}
	t := pkg.TypesInfo.TypeOf(call.Args[1])
	if t == nil {
		return nil
	}
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok {
		return nil // 匿名/接口类型无法定位到具体实现
	}
	obj := named.Obj()
	if obj.Pkg() == nil || !isInModule(obj.Pkg().Path(), repo.Module) {
		return nil
	}
	pos := pkg.Fset.PositionFor(obj.Pos(), false)
	return &domain.CodeEntity{
		ID:        canonicalizer.GoSymbolID(obj.Pkg().Path(), obj.Name()),
		Kind:      domain.KindStruct,
		Name:      obj.Name(),
		FilePath:  relPath(repo.Path, pos.Filename),
		LineStart: pos.Line,
		LineEnd:   pos.Line,
		Properties: map[string]any{
			"serves_grpc": "true",
		},
	}
}

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
			return "", "" // 匿名结构体上的方法，跳过
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

func isInModule(pkgPath, module string) bool {
	logger := zap.L()
	logger.Debug("enter isInModule")
	defer logger.Debug("exit isInModule")
	return pkgPath == module || strings.HasPrefix(pkgPath, module+"/")
}
