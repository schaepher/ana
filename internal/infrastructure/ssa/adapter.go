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
	"sort"
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
	// dispatchRegs 接口注册点缓存（Q161 动态边候选元数据）：Index 级
	// 共享一次扫描——放 extractor（每函数新建）会每函数全 prog 扫描
	dispatchRegs dispatchReg
}

// Name 实现 IndexerPort。
func (a *Adapter) Name() string {
	return "ssa"
}

// Index 加载仓库全部包、构建 SSA，并发射字段追溯数据。
func (a *Adapter) Index(ctx context.Context, repo *domain.Repository, pkgs []*packages.Package, emit domain.EmitFunc) error {
	logger := logging.FromContext(ctx)
	logger.Debug("enter (Adapter).Index")
	defer logger.Debug("exit (Adapter).Index")
	packages.PrintErrors(pkgs) // 诊断信息打到 stderr，不中断

	prog, ssaPkgs := ssautil.Packages(pkgs, ssa.BuilderMode(0))
	if prog == nil {
		return fmt.Errorf("ssa build failed")
	}
	// 仅构建项目内包的函数体（依赖函数保持 stub，按需惰性创建）；
	// 全程序 prog.Build() 会把依赖体也构建出来，成本高（field_trace.md §9）
	for i, p := range pkgs {
		if !isInModule(p.PkgPath, repo.Modules) {
			continue
		}
		if sp := ssaPkgs[i]; sp != nil {
			sp.Build()
		}
	}
	// 内存优化：释放非模块包 AST/TypesInfo——NeedSyntax 全开导致依赖包
	// AST 全量加载（go2o 实测 Load 阶段即达峰值 3.3G 级）；go/ssa 函数体
	// 在 Build 时已缓存 AST（Function.Syntax() 不依赖 packages），后续
	// 阶段（标识符索引/字段提取/别名）只遍历模块内包，依赖 AST 可整体
	// 释放（radar 实测 507MB→1MB）。模块内包 AST 保留（SSA 惰性函数体）。
	for i, p := range pkgs {
		if !isInModule(p.PkgPath, repo.Modules) {
			pkgs[i].Syntax = nil
			pkgs[i].TypesInfo = nil
		}
	}
	// 源码标识符索引（token.Pos → 标识符名）：go/ssa v0.26 的 Alloc 名为 tN，
	// 实例路径（x.A）需从 AST 恢复源码变量名
	idents := buildIdentIndex(pkgs, repo.Modules)
	// 赋值目标索引（表达式起点 → 目标变量名）：lifting 后 map/slice 字面量
	// 是 MakeMap/MakeSlice 寄存器，容器名从此恢复
	assignTargets := buildAssignTargets(pkgs, repo.Modules)

	// 外部函数摘要（内置 + 用户 field-summary.yaml，§7）
	specs, warnings := loadSummaries(repo.Path)
	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
	}

	a.fd = map[domain.CanonicalID]*funcData{}
	fallbackTotal := 0
	// 接口动态派发候选枚举用（⑮：模块内类型池）
	var typePkgs []*types.Package
	for _, p := range pkgs {
		if p.Types != nil {
			typePkgs = append(typePkgs, p.Types)
		}
	}
	for fn := range ssautil.AllFunctions(prog) {
		if !isModuleFunction(fn, repo.Modules) {
			continue // 外部依赖走摘要（Phase 5）
		}
		if err := emitFunction(repo, prog, fn, idents, assignTargets, a.fd, specs, &fallbackTotal, emit, typePkgs, &a.dispatchRegs); err != nil {
			return err
		}
	}
	if fallbackTotal > 0 {
		fmt.Fprintf(os.Stderr, "warning: %d 个字段访问静态类型解析失败（匿名 struct 等），已回退源码字面量路径\n", fallbackTotal)
	}
	// 轻量别名分析（Q80）：产出间接写排除集 + ALIAS 边（须在 emitSummaries 前）
	aliasRes, err := computeAliases(repo, prog, idents, a.fd, emit)
	if err != nil {
		return fmt.Errorf("alias analysis: %w", err)
	}
	// 内存优化：idents/assignTargets 已无后续消费（emitSummaries/
	// emitGlobalInit/emitDispatches 均不依赖）——置 nil 让 GC 回收
	idents, assignTargets = nil, nil
	// function_field_summary 预计算 + INDIRECT_WRITE 边（间接写闭包，消费排除集）
	if err := emitSummaries(a.fd, aliasRes, emit); err != nil {
		return err
	}
	// 内存优化：摘要收集（a.fd/aliasRes）消费完毕
	a.fd, aliasRes = nil, nil
	// 全局变量初始化溯源（Q98）：init（隐式函数）的 Store→Global 边
	if err := emitGlobalInit(repo, prog, emit); err != nil {
		return err
	}
	// 接口动态派发（Q91/Q93/Q94）：dispatch_to 边（接口类型 → 候选实现方法）
	if err := emitDispatches(repo, prog, typePkgs, emit); err != nil {
		return err
	}
	return nil
}




// buildIdentIndex 收集项目内文件的所有标识符（位置 → 名字），供 Alloc 反查源码变量名。
func buildIdentIndex(pkgs []*packages.Package, modules []string) map[token.Pos]string {
	logger := zap.L()
	logger.Debug("enter buildIdentIndex")
	defer logger.Debug("exit buildIdentIndex")
	idents := map[token.Pos]string{}
	for _, p := range pkgs {
		if !isInModule(p.PkgPath, modules) {
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
func isModuleFunction(fn *ssa.Function, modules []string) bool {
	return fn.Pkg != nil && fn.Pkg.Pkg != nil && isInModule(fn.Pkg.Pkg.Path(), modules)
}

// emitFunction 发射单个函数的全部产出：
//  1. 函数/方法节点（Phase 1：保证边端点存在，ID 与 AST 适配器一致）
//  2. 字段访问节点与数据流边（Phase 2：field_extractor.go）
//
// 仅处理有 FuncDecl 源码的顶层函数/方法——闭包（FuncLit）与合成 wrapper 跳过；
// 闭包内字段访问在 Phase 2 归入外层函数（field_trace.md Q14 适配）。
func emitFunction(repo *domain.Repository, prog *ssa.Program, fn *ssa.Function,
	idents map[token.Pos]string, assignTargets []assignTarget,
	data map[domain.CanonicalID]*funcData,
	specs map[string]summarySpec, fallbackTotal *int, emit domain.EmitFunc,
	pkgs []*types.Package, dispatchRegs *dispatchReg) error {
	logger := zap.L()
	logger.Debug("enter emitFunction")
	defer logger.Debug("exit emitFunction")
	if _, ok := fn.Syntax().(*ast.FuncDecl); !ok {
		// 闭包（FuncLit）：字段访问归入外层具名函数（Q14 适配——此前
		// 注释承诺但未实现，闭包内字段写入节点缺失）。合成 wrapper 无
		// 外层（Parent nil）跳过。
		parent := fn.Parent()
		if parent == nil {
			return nil
		}
		obj, ok := parent.Object().(*types.Func)
		if !ok || obj == nil {
			return nil
		}
		pid, _, _ := funcIdentity(obj)
		if pid == "" {
			return nil
		}
		pfd := data[pid]
		if pfd == nil {
			pfd = &funcData{}
			data[pid] = pfd
		}
		return emitFunctionFields(repo, prog, fn, pid, idents, assignTargets, pfd, specs, fallbackTotal, emit, pkgs, dispatchRegs)
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
	return emitFunctionFields(repo, prog, fn, id, idents, assignTargets, fd, specs, fallbackTotal, emit, pkgs, dispatchRegs)
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
	// 接收者（方法）：types.Signature.Params() 不含接收者，接收者在 Recv() 单独存在。
	// 独立 kind=receiver，与普通参数区分展示（前端分组/配色）
	if recvVar := sig.Recv(); recvVar != nil {
		name := recvVar.Name()
		if name == "" {
			name = "recv"
		}
		id := domain.CanonicalID(string(funcID) + "#param.recv." + name)
		if err := emit(domain.Item{Node: &domain.CodeEntity{
			ID:        id,
			Kind:      domain.KindReceiver,
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

// isInModule 判断包路径是否属于任一被索引 module（自身或子包；P2-3
// 多 go.mod——任一 module 前缀匹配即项目内）。
func isInModule(pkgPath string, modules []string) bool {
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

// assignTarget 赋值表达式区间 → 目标变量名。
type assignTarget struct {
	name  string
	start token.Pos
	end   token.Pos
}

// buildAssignTargets 构建 赋值表达式区间 → 目标变量名（Q83：lifting 后
// map/slice 字面量为 MakeMap/MakeSlice 寄存器，其 Pos 落在字面量内部，
// 用区间匹配恢复容器名）。按 start 排序返回，供二分查找。
func buildAssignTargets(pkgs []*packages.Package, modules []string) []assignTarget {
	logger := zap.L()
	logger.Debug("enter buildAssignTargets")
	defer logger.Debug("exit buildAssignTargets")
	targets := []assignTarget{}
	for _, p := range pkgs {
		if !isInModule(p.PkgPath, modules) {
			continue
		}
		for _, f := range p.Syntax {
			ast.Inspect(f, func(n ast.Node) bool {
				switch st := n.(type) {
				case *ast.AssignStmt:
					for i, rhs := range st.Rhs {
						name := lhsIdentName(st.Lhs, i)
						if name != "" {
							targets = append(targets, assignTarget{name: name, start: rhs.Pos(), end: rhs.End()})
						}
					}
				case *ast.ValueSpec:
					for i, v := range st.Values {
						if i < len(st.Names) {
							targets = append(targets, assignTarget{name: st.Names[i].Name, start: v.Pos(), end: v.End()})
						}
					}
				}
				return true
			})
		}
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].start < targets[j].start })
	return targets
}

// lhsIdentName 取赋值目标标识符名（多目标取第 i 个；复合目标如 x[0] 取 x）。
func lhsIdentName(lhs []ast.Expr, i int) string {
	if i >= len(lhs) {
		return ""
	}
	switch l := lhs[i].(type) {
	case *ast.Ident:
		return l.Name
	case *ast.IndexExpr:
		if id, ok := l.X.(*ast.Ident); ok {
			return id.Name
		}
	case *ast.SelectorExpr:
		if id, ok := l.X.(*ast.Ident); ok {
			return id.Name
		}
	}
	return ""
}
