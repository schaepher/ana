// Package ssa 实现字段追溯适配器（docs/field_trace.md v2.2）。
// 基于 go/packages + go/ssa 构建 SSA IR，产出字段访问节点与数据流边，
// 接替 2026-08-13 移除的 Joern 适配器（TD.md 12.7）。
//
// Phase 1（骨架）：加载 + SSA 构建，发射函数/方法节点（保证后续边端点存在）。
// Phase 2+：字段提取（field_access + data_flows_to）、跨过程边、间接写、摘要。
package ssa

import (
	"context"
	"fmt"
	"go/ast"
	"go/types"
	"path/filepath"
	"strings"

	"github.com/schaepher/codeintel/internal/canonicalizer"
	"github.com/schaepher/codeintel/internal/domain"
	"github.com/schaepher/codeintel/internal/logging"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
	"go.uber.org/zap"
)

var _ domain.IndexerPort = (*Adapter)(nil)

// Adapter 是 SSA 字段追溯适配器。
type Adapter struct{}

// Name 实现 IndexerPort。
func (a *Adapter) Name() string {
	return "ssa"
}

// Index 加载仓库全部包、构建 SSA，并发射字段追溯数据。
func (a *Adapter) Index(ctx context.Context, repo *domain.Repository, emit domain.EmitFunc) error {
	logger := logging.FromContext(ctx)
	logger.Debug("enter (Adapter).Index")
	defer logger.Debug("exit (Adapter).Index")
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax |
			packages.NeedTypes | packages.NeedTypesInfo | packages.NeedImports | packages.NeedDeps,
		Dir: repo.Path,
		// Tests 默认 false：不加载 _test.go（与 AST 适配器一致）
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return fmt.Errorf("go/packages load: %w", err)
	}
	packages.PrintErrors(pkgs) // 诊断信息打到 stderr，不中断

	prog, _ := ssautil.Packages(pkgs, ssa.BuilderMode(0))
	if prog == nil {
		return fmt.Errorf("ssa build failed")
	}

	for _, p := range pkgs {
		if !isInModule(p.PkgPath, repo.Module) {
			continue // 仅处理项目内包（外部依赖走摘要，Phase 5）
		}
		if err := emitPackageFunctions(repo, p, prog, emit); err != nil {
			return err
		}
	}
	return nil
}

// emitPackageFunctions 发射项目内顶层函数/方法节点。
// ssautil.AllFunctions 遍历全程序（方法不在 Package.Members 中，须全量过滤）：
// 仅保留有 FuncDecl 源码的函数——闭包（FuncLit）与合成 wrapper 跳过；
// 闭包内字段访问在 Phase 2 归入外层函数（field_trace.md Q14 适配）。
func emitPackageFunctions(repo *domain.Repository, pkg *packages.Package,
	prog *ssa.Program, emit domain.EmitFunc) error {
	logger := zap.L()
	logger.Debug("enter emitPackageFunctions")
	defer logger.Debug("exit emitPackageFunctions")
	for fn := range ssautil.AllFunctions(prog) {
		if fn.Pkg == nil || fn.Pkg.Pkg == nil || !isInModule(fn.Pkg.Pkg.Path(), repo.Module) {
			continue
		}
		if _, ok := fn.Syntax().(*ast.FuncDecl); !ok {
			continue // 闭包 / 合成 wrapper：不建节点
		}
		obj, ok := fn.Object().(*types.Func)
		if !ok || obj == nil {
			continue // 合成函数无 types 对象（理论上已被 FuncDecl 过滤排除）
		}
		pos := prog.Fset.PositionFor(fn.Pos(), false)
		filePath := relPath(repo.Path, pos.Filename)
		if filePath == "" {
			continue // 仓库外文件
		}
		id, kind, name := funcIdentity(obj)
		if id == "" {
			continue // 匿名结构体上的方法，跳过（与 AST 适配器一致）
		}
		n := &domain.CodeEntity{
			ID:        id,
			Kind:      kind,
			Name:      name,
			FilePath:  filePath,
			LineStart: pos.Line,
			LineEnd:   pos.Line,
			Properties: map[string]any{
				// ObjectString 对方法包含接收者：func (s *Service) CreatePayment(req string) error
				"signature": types.ObjectString(obj, types.RelativeTo(pkg.Types)),
			},
		}
		if err := emit(domain.Item{Node: n}); err != nil {
			return err
		}
	}
	return nil
}

// funcIdentity 从 types.Func 生成 canonical ID / kind / name，与 AST 适配器 fnID 一致：
// 方法统一 (T).method（值/指针接收者不区分），匿名结构体上的方法返回空。
func funcIdentity(fn *types.Func) (domain.CanonicalID, domain.EntityKind, string) {
	logger := zap.L()
	logger.Debug("enter funcIdentity")
	defer logger.Debug("exit funcIdentity")
	if fn == nil || fn.Pkg() == nil {
		return "", "", ""
	}
	path := fn.Pkg().Path()
	sig, _ := fn.Type().(*types.Signature)
	if sig == nil {
		return "", "", ""
	}
	if recv := sig.Recv(); recv != nil {
		t := recv.Type()
		if p, ok := t.(*types.Pointer); ok {
			t = p.Elem()
		}
		named, ok := t.(*types.Named)
		if !ok {
			return "", "", "" // 匿名结构体上的方法，跳过
		}
		name := canonicalizer.MethodName(named.Obj().Name(), fn.Name())
		return canonicalizer.GoSymbolID(path, name), domain.KindMethod, name
	}
	return canonicalizer.GoSymbolID(path, fn.Name()), domain.KindFunction, fn.Name()
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

// isInModule 判断包路径是否在 go.mod module 前缀内。
func isInModule(pkgPath, module string) bool {
	return pkgPath == module || strings.HasPrefix(pkgPath, module+"/")
}
