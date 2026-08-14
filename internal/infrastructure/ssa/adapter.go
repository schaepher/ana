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
	"go/token"
	"go/types"
	"os"
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
type Adapter struct {
	// fd 摘要收集（构建期内存态）：function_field_summary 预计算用
	fd map[domain.CanonicalID]*funcData
}

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

	prog, ssaPkgs := ssautil.Packages(pkgs, ssa.BuilderMode(0))
	if prog == nil {
		return fmt.Errorf("ssa build failed")
	}
	// 仅构建项目内包的函数体（依赖函数保持 stub，按需惰性创建）；
	// 全程序 prog.Build() 会把依赖体也构建出来，成本高（field_trace.md §9）
	for i, p := range pkgs {
		if !isInModule(p.PkgPath, repo.Module) {
			continue
		}
		if sp := ssaPkgs[i]; sp != nil {
			sp.Build()
		}
	}
	// 源码标识符索引（token.Pos → 标识符名）：go/ssa v0.26 的 Alloc 名为 tN，
	// 实例路径（x.A）需从 AST 恢复源码变量名
	idents := buildIdentIndex(pkgs, repo.Module)

	// 外部函数摘要（内置 + 用户 field-summary.yaml，§7）
	specs, warnings := loadSummaries(repo.Path)
	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
	}

	a.fd = map[domain.CanonicalID]*funcData{}
	for fn := range ssautil.AllFunctions(prog) {
		if !isModuleFunction(fn, repo.Module) {
			continue // 外部依赖走摘要（Phase 5）
		}
		if err := emitFunction(repo, prog, fn, idents, a.fd, specs, emit); err != nil {
			return err
		}
	}
	// 轻量别名分析（Q80）：产出间接写排除集 + ALIAS 边（须在 emitSummaries 前）
	aliasRes, err := computeAliases(repo, prog, idents, emit)
	if err != nil {
		return fmt.Errorf("alias analysis: %w", err)
	}
	// function_field_summary 预计算 + INDIRECT_WRITE 边（间接写闭包，消费排除集）
	return emitSummaries(a.fd, aliasRes, emit)
}

// buildIdentIndex 收集项目内文件的所有标识符（位置 → 名字），供 Alloc 反查源码变量名。
func buildIdentIndex(pkgs []*packages.Package, module string) map[token.Pos]string {
	logger := zap.L()
	logger.Debug("enter buildIdentIndex")
	defer logger.Debug("exit buildIdentIndex")
	idents := map[token.Pos]string{}
	for _, p := range pkgs {
		if !isInModule(p.PkgPath, module) {
			continue
		}
		for _, f := range p.Syntax {
			ast.Inspect(f, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok {
					idents[id.Pos()] = id.Name
				}
				return true
			})
		}
	}
	return idents
}

// isModuleFunction 判断 SSA 函数是否属于项目内包。
func isModuleFunction(fn *ssa.Function, module string) bool {
	return fn.Pkg != nil && fn.Pkg.Pkg != nil && isInModule(fn.Pkg.Pkg.Path(), module)
}

// emitFunction 发射单个函数的全部产出：
//  1. 函数/方法节点（Phase 1：保证边端点存在，ID 与 AST 适配器一致）
//  2. 字段访问节点与数据流边（Phase 2：field_extractor.go）
//
// 仅处理有 FuncDecl 源码的顶层函数/方法——闭包（FuncLit）与合成 wrapper 跳过；
// 闭包内字段访问在 Phase 2 归入外层函数（field_trace.md Q14 适配）。
func emitFunction(repo *domain.Repository, prog *ssa.Program, fn *ssa.Function,
	idents map[token.Pos]string, data map[domain.CanonicalID]*funcData,
	specs map[string]summarySpec, emit domain.EmitFunc) error {
	logger := zap.L()
	logger.Debug("enter emitFunction")
	defer logger.Debug("exit emitFunction")
	if _, ok := fn.Syntax().(*ast.FuncDecl); !ok {
		return nil // 闭包 / 合成 wrapper
	}
	obj, ok := fn.Object().(*types.Func)
	if !ok || obj == nil {
		return nil
	}
	pos := prog.Fset.PositionFor(fn.Pos(), false)
	filePath := relPath(repo.Path, pos.Filename)
	if filePath == "" {
		return nil // 仓库外文件
	}
	id, kind, name := funcIdentity(obj)
	if id == "" {
		return nil // 匿名结构体上的方法（与 AST 适配器一致）
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
			"signature": types.ObjectString(obj, types.RelativeTo(fn.Pkg.Pkg)),
		},
	}
	if err := emit(domain.Item{Node: n}); err != nil {
		return err
	}
	// 签名参数/返回节点（前端展开用）
	if err := emitSignatureNodes(fn, id, pos, filePath, emit); err != nil {
		return err
	}
	fd := data[id]
	if fd == nil {
		fd = &funcData{}
		data[id] = fd
	}
	return emitFunctionFields(repo, prog, fn, id, idents, fd, specs, emit)
}

// emitSignatureNodes 发射函数/方法签名的参数与返回节点（parameter / result）
// 及 has_param / has_result 边——签名结构展示，前端展开函数节点时可见。
// slot：参数 #param.<name>（接收者 #param.recv.<name> 防重名），
// 返回 #result（多返回 #result.<idx>）。
func emitSignatureNodes(fn *ssa.Function, funcID domain.CanonicalID, pos token.Position,
	filePath string, emit domain.EmitFunc) error {
	logger := zap.L()
	logger.Debug("enter emitSignatureNodes")
	defer logger.Debug("exit emitSignatureNodes")
	sig := fn.Signature
	if sig == nil {
		return nil
	}
	// 接收者（方法）：types.Signature.Params() 不含接收者，接收者在 Recv() 单独存在
	if recvVar := sig.Recv(); recvVar != nil {
		name := recvVar.Name()
		if name == "" {
			name = "recv"
		}
		id := domain.CanonicalID(string(funcID) + "#param.recv." + name)
		if err := emit(domain.Item{Node: &domain.CodeEntity{
			ID:        id,
			Kind:      domain.KindParameter,
			Name:      name,
			FilePath:  filePath,
			LineStart: pos.Line,
			LineEnd:   pos.Line,
			Properties: map[string]any{
				"type_string": recvVar.Type().String(),
				"index":       -1, // 接收者不在 Params 索引内
				"receiver":    "true",
				"func_id":     string(funcID),
			},
		}}); err != nil {
			return err
		}
		if err := emit(domain.Item{Fact: &domain.Fact{
			SourceID:   funcID,
			TargetID:   id,
			Kind:       domain.FactHasParam,
			ToolSource: domain.ToolSSA,
			Confidence: 1.0,
		}}); err != nil {
			return err
		}
	}
	// 普通参数
	n := sig.Params().Len()
	for i := 0; i < n; i++ {
		p := sig.Params().At(i)
		name := p.Name()
		if name == "" {
			name = fmt.Sprintf("arg%d", i)
		}
		id := domain.CanonicalID(string(funcID) + "#param." + name)
		if err := emit(domain.Item{Node: &domain.CodeEntity{
			ID:        id,
			Kind:      domain.KindParameter,
			Name:      name,
			FilePath:  filePath,
			LineStart: pos.Line,
			LineEnd:   pos.Line,
			Properties: map[string]any{
				"type_string": p.Type().String(),
				"index":       i,
				"func_id":     string(funcID),
			},
		}}); err != nil {
			return err
		}
		if err := emit(domain.Item{Fact: &domain.Fact{
			SourceID:   funcID,
			TargetID:   id,
			Kind:       domain.FactHasParam,
			ToolSource: domain.ToolSSA,
			Confidence: 1.0,
		}}); err != nil {
			return err
		}
	}
	// 返回
	nr := sig.Results().Len()
	for i := 0; i < nr; i++ {
		r := sig.Results().At(i)
		slot := "result"
		if nr > 1 {
			slot = fmt.Sprintf("result.%d", i)
		}
		id := domain.CanonicalID(string(funcID) + "#" + slot)
		if err := emit(domain.Item{Node: &domain.CodeEntity{
			ID:        id,
			Kind:      domain.KindResult,
			Name:      r.Type().String(),
			FilePath:  filePath,
			LineStart: pos.Line,
			LineEnd:   pos.Line,
			Properties: map[string]any{
				"type_string": r.Type().String(),
				"index":       i,
				"func_id":     string(funcID),
			},
		}}); err != nil {
			return err
		}
		if err := emit(domain.Item{Fact: &domain.Fact{
			SourceID:   funcID,
			TargetID:   id,
			Kind:       domain.FactHasResult,
			ToolSource: domain.ToolSSA,
			Confidence: 1.0,
		}}); err != nil {
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
