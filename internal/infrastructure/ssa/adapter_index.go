package ssa

import (
	"context"
	"fmt"
	"go/types"
	"os"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/schaepher/codeintel/internal/domain"
	"github.com/schaepher/codeintel/internal/logging"
	"go.uber.org/zap"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

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
	packages.PrintErrors(pkgs)

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

	for i, p := range pkgs {
		if !isInModule(p.PkgPath, repo.Modules) {
			continue
		}
		if sp := ssaPkgs[i]; sp != nil {
			sp.Build()
		}
	}

	for i, p := range pkgs {
		if !isInModule(p.PkgPath, repo.Modules) {
			pkgs[i].Syntax = nil
			pkgs[i].TypesInfo = nil
		}
	}
	stage("释放依赖 AST")

	idents := buildIdentIndex(pkgs, repo.Modules)
	stage("buildIdentIndex")

	assignTargets := buildAssignTargets(pkgs, repo.Modules)

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

	byPkg := map[string][]*ssa.Function{}
	for fn := range ssautil.AllFunctions(prog) {
		if !isModuleFunction(fn, repo.Modules) {
			continue
		}
		byPkg[fn.Pkg.Pkg.Path()] = append(byPkg[fn.Pkg.Pkg.Path()], fn)
	}
	totalFuncs := 0
	for _, fns := range byPkg {
		totalFuncs += len(fns)
	}

	pkgOrder := make([]string, 0, len(byPkg))
	for pkgPath := range byPkg {
		pkgOrder = append(pkgOrder, pkgPath)
	}
	sort.Slice(pkgOrder, func(i, j int) bool {
		return len(byPkg[pkgOrder[i]]) > len(byPkg[pkgOrder[j]])
	})

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
				files = pkg.GoFiles
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

		if hash != "" {
			savePkgCache(pkgCachePath(repo.Path, pkgPath), hash, pkgNodes, pkgFacts, pkgFD)
		}
	}
	logger.Info("pkg cache", zap.Int("hits", cacheHits), zap.Int("total", len(pkgOrder)))
	stage("emitFunction 循环（包内分块 + 缓存）")

	if n := fallbackTotal.Load(); n > 0 {
		fmt.Fprintf(os.Stderr, "warning: %d 个字段访问静态类型解析失败（匿名 struct 等），已回退源码字面量路径\n", n)
	}

	aliasRes, err := computeAliases(repo, prog, idents, a.fd, emit)
	if err != nil {
		return fmt.Errorf("alias analysis: %w", err)
	}
	stage("computeAliases")

	idents, assignTargets = nil, nil

	if err := emitSummaries(a.fd, aliasRes, emit); err != nil {
		return err
	}
	stage("emitSummaries")

	a.fd, aliasRes = nil, nil

	if err := emitGlobalInit(repo, prog, emit); err != nil {
		return err
	}

	if err := emitDispatches(repo, prog, typePkgs, emit); err != nil {
		return err
	}
	stage("emitDispatches")
	return nil
}
