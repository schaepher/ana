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
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/schaepher/codeintel/internal/canonicalizer"
	"github.com/schaepher/codeintel/internal/domain"
	"github.com/schaepher/codeintel/internal/logging"
	"go.uber.org/zap"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

var _ domain.IndexerPort = (*Adapter)(nil)

// Adapter 是 SSA 字段追溯适配器。
type Adapter struct {
	// fd 摘要收集（构建期内存态）：function_field_summary 预计算用
	fd map[domain.CanonicalID]*funcData
	// dispatchRegs 接口注册点缓存（Q161 动态边候选元数据）：Index 级
	// 共享一次扫描——放 extractor（每函数新建）会每函数全 prog 扫描
	dispatchRegs dispatchReg
	// workers 按包并发数（Q169/Q170）：默认 1=串行；命令行 --workers N
	// 指定（orchestrator SetWorkers 注入）
	workers int
}

// SetWorkers 设置按包并发数（Q170：--workers 参数；≤1 退串行）。
func (a *Adapter) SetWorkers(n int) {
	a.workers = n
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
	// 阶段进度日志（Q164/Q165 诊断：大仓库构建卡住/内存高时定位阶段）——
	// Info 级写入 .codeintel/codeintel.log（stdout/stderr 保持干净）
	stageStart := time.Now()
	stage := func(name string) {
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		logger.Info("build stage",
			zap.String("stage", name), zap.Duration("elapsed", time.Since(stageStart)),
			zap.Int64("heap_mb", int64(ms.HeapAlloc>>20)))
		stageStart = time.Now()
	}

	prog, ssaPkgs := ssautil.Packages(pkgs, ssa.BuilderMode(0))
	if prog == nil {
		return fmt.Errorf("ssa build failed")
	}
	stage("ssautil.Packages")
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
	stage("释放依赖 AST")

	// 源码标识符索引（token.Pos → 标识符名）：go/ssa v0.26 的 Alloc 名为 tN，
	// 实例路径（x.A）需从 AST 恢复源码变量名
	idents := buildIdentIndex(pkgs, repo.Modules)
	stage("buildIdentIndex")

	// 赋值目标索引（表达式起点 → 目标变量名）：lifting 后 map/slice 字面量
	// 是 MakeMap/MakeSlice 寄存器，容器名从此恢复
	assignTargets := buildAssignTargets(pkgs, repo.Modules)

	// 外部函数摘要（内置 + 用户 field-summary.yaml，§7）
	specs, warnings := loadSummaries(repo.Path)
	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
	}

	a.fd = map[domain.CanonicalID]*funcData{}
	var fallbackTotal atomic.Int64
	// 接口动态派发候选枚举用（⑮：模块内类型池）
	var typePkgs []*types.Package
	for _, p := range pkgs {
		if p.Types != nil {
			typePkgs = append(typePkgs, p.Types)
		}
	}
	// Q166/Q167：按包分组打点 + 总量进度——先全量遍历拿到总数
	// （AllFunctions 按 fn.Pkg 分组，闭包函数归所属包），每包完成
	// 输出 done/total/percent——大仓库一开始就知道总函数量
	byPkg := map[string][]*ssa.Function{}
	for fn := range ssautil.AllFunctions(prog) {
		if !isModuleFunction(fn, repo.Modules) {
			continue // 外部依赖走摘要（Phase 5）
		}
		byPkg[fn.Pkg.Pkg.Path()] = append(byPkg[fn.Pkg.Pkg.Path()], fn)
	}
	totalFuncs := 0
	for _, fns := range byPkg {
		totalFuncs += len(fns)
	}
	// Q174：按函数量倒序排队（map 遍历顺序不稳定）——大包先启动，
	// 降低大包晚启动造成的尾部等待（单慢包长尾）
	pkgOrder := make([]string, 0, len(byPkg))
	for pkgPath := range byPkg {
		pkgOrder = append(pkgOrder, pkgPath)
	}
	sort.Slice(pkgOrder, func(i, j int) bool {
		return len(byPkg[pkgOrder[i]]) > len(byPkg[pkgOrder[j]])
	})
	// Q169/Q170/Q174/Q176：按包 → 包内分块 worker pool——单慢包长尾
	// 由包内分块并行分摊（每块 200 函数，块不跨包）；Q176 包级缓存：
	// 未变更包跳过分析，从缓存加载产物（节点/边/fd）直接 emit + 合并。
	workers := a.workers
	if workers < 1 {
		workers = 1
	}
	const blockSize = 200
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	var fdMu sync.Mutex // 合并保护（mergeFuncData）
	doneFuncs := 0
	var doneMu sync.Mutex
	cacheHits := 0
	for _, pkgPath := range pkgOrder {
		fns := byPkg[pkgPath]
		// Q176：包级缓存——hash 命中直接加载产物
		var pkg *packages.Package
		for i := range pkgs {
			if pkgs[i].PkgPath == pkgPath {
				pkg = pkgs[i]
				break
			}
		}
		hash := ""
		if pkg != nil {
			files := pkg.CompiledGoFiles
			if len(files) == 0 {
				files = pkg.GoFiles // 兜底：Mode 未开 NeedCompiledGoFiles
			}
			if h, err := pkgContentHash(files); err == nil {
				hash = h
			}
		}
		if hash != "" {
			if cached := loadPkgCache(pkgCachePath(repo.Path, pkgPath), hash); cached != nil {
				for _, n := range cached.Nodes {
					if err := emit(domain.Item{Node: n}); err != nil {
						return err
					}
				}
				for _, f := range cached.Facts {
					if err := emit(domain.Item{Fact: f}); err != nil {
						return err
					}
				}
				for id, cfd := range cached.FuncData {
					mergeFuncData(&fdMu, a.fd, domain.CanonicalID(id), fromCachedFD(cfd))
				}
				doneMu.Lock()
				doneFuncs += len(fns)
				cacheHits++
				done := doneFuncs
				percent := done * 100 / totalFuncs
				doneMu.Unlock()
				logger.Info("build progress",
					zap.String("pkg", pkgPath), zap.Int("funcs", len(fns)),
					zap.Int("done", done), zap.Int("total", totalFuncs),
					zap.Int("percent", percent), zap.Bool("cached", true))
				continue
			}
		}
		// 未命中：包内分块并行 + 产物收集（写缓存用）
		var pkgNodes []*domain.CodeEntity
		var pkgFacts []*domain.Fact
		var pkgFD = map[domain.CanonicalID]*funcData{}
		var pkgMu sync.Mutex
		// 包内 emit 拦截：收集产物后转发原 emit
		pkgEmit := func(item domain.Item) error {
			pkgMu.Lock()
			if item.Node != nil {
				pkgNodes = append(pkgNodes, item.Node)
			}
			if item.Fact != nil {
				pkgFacts = append(pkgFacts, item.Fact)
			}
			pkgMu.Unlock()
			return emit(item)
		}
		blockStart := time.Now()
		for start := 0; start < len(fns); start += blockSize {
			end := start + blockSize
			if end > len(fns) {
				end = len(fns)
			}
			block := fns[start:end]
			wg.Add(1)
			go func(block []*ssa.Function) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				for _, fn := range block {
					owner, fd, err := emitFunction(repo, prog, fn, idents, assignTargets, specs, &fallbackTotal, pkgEmit, typePkgs, &a.dispatchRegs)
					if err != nil {
						fmt.Fprintf(os.Stderr, "emitFunction %s: %v\n", fn.Name(), err)
						return
					}
					if owner != "" && fd != nil {
						pkgMu.Lock()
						pkgFD[owner] = fd
						pkgMu.Unlock()
						mergeFuncData(&fdMu, a.fd, owner, fd)
					}
				}
				doneMu.Lock()
				doneFuncs += len(block)
				done := doneFuncs
				percent := done * 100 / totalFuncs
				doneMu.Unlock()
				logger.Info("build progress",
					zap.String("pkg", pkgPath), zap.Int("funcs", len(block)),
					zap.Int("done", done), zap.Int("total", totalFuncs),
					zap.Int("percent", percent),
					zap.Duration("elapsed", time.Since(blockStart)))
			}(block)
		}
		wg.Wait()
		// 写缓存（hash 可用时）
		if hash != "" {
			savePkgCache(pkgCachePath(repo.Path, pkgPath), hash, pkgNodes, pkgFacts, pkgFD)
		}
	}
	logger.Info("pkg cache", zap.Int("hits", cacheHits), zap.Int("total", len(pkgOrder)))
	stage("emitFunction 循环（包内分块 + 缓存）")

	if n := fallbackTotal.Load(); n > 0 {
		fmt.Fprintf(os.Stderr, "warning: %d 个字段访问静态类型解析失败（匿名 struct 等），已回退源码字面量路径\n", n)
	}
	// 轻量别名分析（Q80）：产出间接写排除集 + ALIAS 边（须在 emitSummaries 前）
	aliasRes, err := computeAliases(repo, prog, idents, a.fd, emit)
	if err != nil {
		return fmt.Errorf("alias analysis: %w", err)
	}
	stage("computeAliases")

	// 内存优化：idents/assignTargets 已无后续消费（emitSummaries/
	// emitGlobalInit/emitDispatches 均不依赖）——置 nil 让 GC 回收
	idents, assignTargets = nil, nil
	// function_field_summary 预计算 + INDIRECT_WRITE 边（间接写闭包，消费排除集）
	if err := emitSummaries(a.fd, aliasRes, emit); err != nil {
		return err
	}
	stage("emitSummaries")

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
	stage("emitDispatches")
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

// emitFunction 发射单个函数的全部产出（Q174：局部收集）：
//  1. 函数/方法节点（Phase 1：保证边端点存在，ID 与 AST 适配器一致）
//  2. 字段访问节点与数据流边（Phase 2：field_extractor.go）
//  3. 返回 (ownerID, 局部 funcData)——由分块 worker pool 锁内合并进
//     data（闭包归外层；块间并行时同一 funcData 不再被并发写）
//
// 仅处理有 FuncDecl 源码的顶层函数/方法——闭包（FuncLit）与合成 wrapper 跳过；
// 闭包内字段访问在 Phase 2 归入外层函数（field_trace.md Q14 适配）。
func emitFunction(repo *domain.Repository, prog *ssa.Program, fn *ssa.Function,
	idents map[token.Pos]string, assignTargets []assignTarget,
	specs map[string]summarySpec, fallbackTotal *atomic.Int64, emit domain.EmitFunc,
	pkgs []*types.Package, dispatchRegs *dispatchReg) (domain.CanonicalID, *funcData, error) {
	logger := zap.L()
	logger.Debug("enter emitFunction")
	defer logger.Debug("exit emitFunction")
	if _, ok := fn.Syntax().(*ast.FuncDecl); !ok {
		// 闭包（FuncLit）：字段访问归入外层具名函数（Q14 适配——此前
		// 注释承诺但未实现，闭包内字段写入节点缺失）。合成 wrapper 无
		// 外层（Parent nil）跳过。
		parent := fn.Parent()
		if parent == nil {
			return "", nil, nil
		}
		obj, ok := parent.Object().(*types.Func)
		if !ok || obj == nil {
			return "", nil, nil
		}
		pid, _, _ := funcIdentity(obj)
		if pid == "" {
			return "", nil, nil
		}
		fd := &funcData{}
		err := emitFunctionFields(repo, prog, fn, pid, idents, assignTargets, fd, specs, fallbackTotal, emit, pkgs, dispatchRegs)
		return pid, fd, err
	}
	obj, ok := fn.Object().(*types.Func)
	if !ok || obj == nil {
		return "", nil, nil
	}
	pos := prog.Fset.PositionFor(fn.Pos(), false)
	filePath := relPath(repo.Path, pos.Filename)
	if filePath == "" {
		return "", nil, nil // 仓库外文件
	}
	id, kind, name := funcIdentity(obj)
	if id == "" {
		return "", nil, nil // 匿名结构体上的方法（与 AST 适配器一致）
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
		return "", nil, err
	}
	// 签名参数/返回节点（前端展开用）
	if err := emitSignatureNodes(fn, id, pos, filePath, emit); err != nil {
		return "", nil, err
	}
	fd := &funcData{}
	err := emitFunctionFields(repo, prog, fn, id, idents, assignTargets, fd, specs, fallbackTotal, emit, pkgs, dispatchRegs)
	return id, fd, err
}

// mergeFuncData 锁内合并局部 funcData 进共享 map（Q174 分块并发）：
// direct/indirect 条目均为 append 语义，合并顺序不影响结果集。
func mergeFuncData(fdMu *sync.Mutex, data map[domain.CanonicalID]*funcData,
	owner domain.CanonicalID, fd *funcData) {
	if owner == "" || fd == nil {
		return
	}
	if len(fd.directReads)+len(fd.directWrites)+len(fd.calls)+len(fd.indirectWrites) == 0 {
		return
	}
	fdMu.Lock()
	defer fdMu.Unlock()
	d := data[owner]
	if d == nil {
		d = &funcData{}
		data[owner] = d
	}
	d.directReads = append(d.directReads, fd.directReads...)
	d.directWrites = append(d.directWrites, fd.directWrites...)
	d.calls = append(d.calls, fd.calls...)
	d.indirectWrites = append(d.indirectWrites, fd.indirectWrites...)
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
