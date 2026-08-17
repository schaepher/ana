// Package orchestrator 实现 Index Orchestrator（TD.md 6.1 全量构建流程）：
//   - 并行启动所有适配器 goroutine，每个独立超时（默认 10 分钟）
//   - 适配器流式数据 → 分批（1000 条/事务）写入 SQLite
//   - 某适配器失败：已提交数据保留，构建标记降级
//   - SCIP 失败：构建失败（符号权威缺失）
package orchestrator

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/schaepher/codeintel/internal/domain"
	"github.com/schaepher/codeintel/internal/infrastructure/ast"
	"github.com/schaepher/codeintel/internal/infrastructure/git"
	"github.com/schaepher/codeintel/internal/infrastructure/scip"
	"github.com/schaepher/codeintel/internal/infrastructure/ssa"
	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
	"github.com/schaepher/codeintel/internal/logging"
	"golang.org/x/tools/go/packages"
	"go.uber.org/zap"
)

// 常量
const (
	AdapterTimeout = 10 * time.Minute // 适配器级超时（TD.md 9.1）
	BatchSize      = 10000            // 分批事务大小（TD.md 5.2；Q171/Q174 双缓冲+加大摊薄事务——大仓库 36 万 item 时减少 flush 次数）
)

// AdapterResult 记录单个适配器的执行结果。
type AdapterResult struct {
	Name     string
	Duration time.Duration
	Err      error
}

// BuildResult 全量构建报告。
type BuildResult struct {
	Status       string
	Nodes        int
	Edges        int
	Duration     time.Duration
	CommitSHA    string
	Adapter      []AdapterResult
	SkippedEdges int // 因外键冲突跳过的边（日志用）
}

// Orchestrator 编排全量构建。
type Orchestrator struct {
	Repo     *domain.Repository
	Adapters []domain.IndexerPort
	RepoImpl *sqlite.Repo
}

// New 创建 Orchestrator，默认挂载 MVP 适配器（SCIP/AST/Git）。
func New(repo *domain.Repository, db *sqlite.DB) *Orchestrator {
	logger := zap.L()
	logger.Debug("enter New")
	defer logger.Debug("exit New")
	return &Orchestrator{
		Repo:     repo,
		RepoImpl: sqlite.NewRepo(db),
		Adapters: []domain.IndexerPort{
			&scip.Adapter{},
			&ast.Adapter{},
			&git.Adapter{},
			&ssa.Adapter{},
		},
	}
}

// SetWorkers 设置 ssa 适配器按包并发数（Q170：CLI --workers 注入）。
func (o *Orchestrator) SetWorkers(n int) {
	for _, a := range o.Adapters {
		if w, ok := a.(interface{ SetWorkers(int) }); ok {
			w.SetWorkers(n)
		}
	}
}

// FullBuild 执行全量构建并返回报告（TD.md 5.2 并行流程）。
func (o *Orchestrator) FullBuild(ctx context.Context) (*BuildResult, error) {
	logger := logging.FromContext(ctx)
	logger.Debug("enter (Orchestrator).FullBuild")
	defer logger.Debug("exit (Orchestrator).FullBuild")
	start := time.Now()

	// 清空旧数据（全量重建语义）：DROP+CREATE 图数据表（比 DELETE 全表
	// 快且释放空间；build_metadata 与配置表保留）
	if err := o.RepoImpl.ResetGraphTables(); err != nil {
		return nil, fmt.Errorf("reset graph tables: %w", err)
	}
	// Q168 诊断：orchestrator 级阶段耗时（loadPackages/写库为 ssa 阶段
	// 日志盲区——大仓库 20 分钟定位）
	orchestraStart := time.Now()
	orchStage := func(name string) {
		logger.Info("orchestrator stage",
			zap.String("stage", name), zap.Duration("elapsed", time.Since(orchestraStart)))
		orchestraStart = time.Now()
	}
	pkgs, err := o.loadPackages(ctx)
	if err != nil {
		return nil, err
	}
	orchStage("loadPackages")
	results, skipped, err := o.runAdapters(ctx, pkgs, nil, nil)
	if err != nil {
		return nil, err
	}
	orchStage("runAdapters")
	return o.finishBuild(start, results, skipped, "all")
}

// IncrementalBuild 增量构建（TD.md 5.2 增量语义，MVP：全量分析 + 增量写入）：
//  1. 删除变更文件旧数据（节点级联删边与摘要）
//  2. 适配器全量运行，写库时只保留与变更文件相关的产出
//     （节点 file_path ∈ 变更文件；边/摘要的端点属于变更文件）
//  3. build_metadata 记录 tool_name=incremental
//
// 语义正确性：全量分析保证跨包间接写闭包等结果完整（分析成本与 init 相同），
// 增量只裁剪写入范围——未变更数据原样保留，不产生全量 DELETE 的碎片。
func (o *Orchestrator) IncrementalBuild(ctx context.Context, changedFiles []string) (*BuildResult, error) {
	logger := logging.FromContext(ctx)
	logger.Debug("enter (Orchestrator).IncrementalBuild")
	defer logger.Debug("exit (Orchestrator).IncrementalBuild")
	start := time.Now()

	// 1. 删除变更文件的旧节点（级联删边与摘要行）
	changed := map[string]bool{}
	for _, f := range changedFiles {
		changed[f] = true
	}
	if err := deleteFiles(o.RepoImpl, changedFiles); err != nil {
		return nil, fmt.Errorf("delete changed files: %w", err)
	}

	// 2. 全量分析 + 增量写库过滤：
	//    节点：file_path ∈ 变更文件；边/摘要：端点属于变更文件。
	//    端点 file_path 从 DB 查（写库协程单写者，删除后变更文件端点已不在，
	//    查不到即视为变更文件——保留写入）
	endpointFile := map[string]string{}
	endpointInChanged := func(id string) bool {
		if fp, ok := endpointFile[id]; ok {
			return changed[fp]
		}
		var fp sql.NullString
		if err := o.RepoImpl.QueryRow("SELECT file_path FROM nodes WHERE id = ?", id).Scan(&fp); err != nil || !fp.Valid {
			return true // 节点已删除（属于变更文件）或不存在：保留边/摘要
		}
		endpointFile[id] = fp.String
		return changed[fp.String]
	}
	keep := func(item domain.Item) bool {
		switch {
		case item.Node != nil:
			return changed[item.Node.FilePath]
		case item.Fact != nil:
			return endpointInChanged(string(item.Fact.SourceID)) || endpointInChanged(string(item.Fact.TargetID))
		case item.Summary != nil:
			return endpointInChanged(string(item.Summary.FunctionID))
		}
		return false
	}
	pkgs, err := o.loadPackages(ctx)
	if err != nil {
		return nil, err
	}
	results, skipped, err := o.runAdapters(ctx, pkgs, keep, changedFiles)
	if err != nil {
		return nil, err
	}
	return o.finishBuild(start, results, skipped, "incremental")
}

// loadPackages 统一加载仓库 go/packages（内存优化：AST/SSA 适配器共享
// 一次类型检查，避免各自 Load 翻倍）。返回共享结果供适配器复用。
// loadPackages 统一加载仓库 go/packages（内存优化：AST/SSA 适配器共享
// 一次类型检查，避免各自 Load 翻倍）。返回共享结果供适配器复用。
// P2-3 多 go.mod：每个 module 单独 Load（go/packages 不能跨 module），
// 按 PkgPath 去重合并（同一包路径只属于一个 module，Go 语义保证）。
func (o *Orchestrator) loadPackages(ctx context.Context) ([]*packages.Package, error) {
	dirs := []string{o.Repo.Path}
	for i, d := range o.Repo.ModuleDirs {
		if i == 0 {
			continue // 根 module 目录即仓库根
		}
		dirs = append(dirs, filepath.Join(o.Repo.Path, d))
	}
	seen := map[string]bool{}
	var out []*packages.Package
	for _, dir := range dirs {
		cfg := &packages.Config{
			Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedSyntax |
				packages.NeedTypes | packages.NeedTypesInfo | packages.NeedImports | packages.NeedDeps,
			Dir: dir,
			// Tests 默认 false：不加载 _test.go（与适配器既有语义一致）
		}
		pkgs, err := packages.Load(cfg, "./...")
		if err != nil {
			return nil, fmt.Errorf("go/packages load (%s): %w", dir, err)
		}
		for _, p := range pkgs {
			if seen[p.PkgPath] {
				continue
			}
			seen[p.PkgPath] = true
			out = append(out, p)
		}
	}
	return out, nil
}

// DiscoverModules 递归扫描仓库根下的 go.mod（跳过 .git/.codeintel/vendor/
// node_modules），返回 module 路径与相对仓库根的目录（根 go.mod 在前）。
// P2-3 多 go.mod monorepo。
func DiscoverModules(repoPath string) (modules []string, dirs []string, err error) {
	rootMod, err := readGoModModule(filepath.Join(repoPath, "go.mod"))
	if err != nil {
		return nil, nil, err
	}
	modules = append(modules, rootMod)
	dirs = append(dirs, ".")
	var walk func(dir string) error
	walk = func(dir string) error {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			switch e.Name() {
			case ".git", ".codeintel", "vendor", "node_modules":
				continue
			}
			sub := filepath.Join(dir, e.Name())
			if _, err := os.Stat(filepath.Join(sub, "go.mod")); err == nil {
				m, err := readGoModModule(filepath.Join(sub, "go.mod"))
				if err != nil {
					continue // 无法解析的 go.mod 跳过
				}
				rel, _ := filepath.Rel(repoPath, sub)
				modules = append(modules, m)
				dirs = append(dirs, filepath.ToSlash(rel))
				continue // module 目录内不再嵌套扫描（Go 语义：嵌套 module 独立）
			}
			if err := walk(sub); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(repoPath); err != nil {
		return nil, nil, err
	}
	return modules, dirs, nil
}

// readGoModModule 解析 go.mod 的 module 指令。
func readGoModModule(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read go.mod: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "module "); ok {
			m := strings.TrimSpace(rest)
			if i := strings.Index(m, " "); i >= 0 {
				m = m[:i]
			}
			if m != "" {
				return m, nil
			}
		}
	}
	return "", fmt.Errorf("go.mod 无 module 指令: %s", path)
}

// deleteFiles 删除指定文件的节点（级联删边与摘要行）；分批避免 SQLite
// 参数上限（999）。
func deleteFiles(repo *sqlite.Repo, files []string) error {
	const batchSize = 400
	for i := 0; i < len(files); i += batchSize {
		end := i + batchSize
		if end > len(files) {
			end = len(files)
		}
		batch := files[i:end]
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",")
		args := make([]any, len(batch))
		for j, f := range batch {
			args[j] = f
		}
		if _, err := repo.Exec("DELETE FROM nodes WHERE file_path IN ("+placeholders+")", args...); err != nil {
			return err
		}
	}
	return nil
}

// runAdapters 并行执行适配器并写库（keep 为 nil 时全部写入；否则只保留
// keep(item) 为 true 的条目）。pkgs 为共享加载的 go/packages 结果
// （AST/SSA 复用，避免重复类型检查）。返回各适配器结果与跳过的 FK 冲突边数。
func (o *Orchestrator) runAdapters(ctx context.Context, pkgs []*packages.Package, keep func(domain.Item) bool, changedFiles []string) ([]AdapterResult, int, error) {
	logger := logging.FromContext(ctx)
	runStart := time.Now()
	// 增量更新：AST 适配器文件级跳过（§20.3 唯一真实加速点）；nil = 全量。
	// 每次运行重置，避免上次增量残留影响后续全量构建。
	for _, a := range o.Adapters {
		if inc, ok := a.(interface{ SetChangedFiles([]string) }); ok {
			inc.SetChangedFiles(changedFiles)
		}
	}
	var (
		results []AdapterResult
		skipped int
		mu      sync.Mutex
	)
	// 适配器 → 数据通道 → 消费协程 → flushCh（双缓冲）→ flush 协程（单写者）。
	// Q171：flush 与消费解耦——SQLite 并发写无收益（同连接写锁串行化），
	// 但 flush 慢时（大仓库百万行单批 ~300ms）双缓冲让消费/emit 不阻塞
	ch := make(chan domain.Item, 4096)
	// Q174：backpressure 采集——producer 因 channel 满阻塞的时间占比
	// （flush 慢时生产 worker 阻塞于写库；日志显示 CPU vs wall）
	var bpTotal time.Duration
	var bpMu sync.Mutex
	flushCh := make(chan *batchT, 2) // 消费填一个、flush 写一个
	flushed := make(chan struct{})

	// flush 协程（单写者：SQLite 写锁串行）
	var flushWg sync.WaitGroup
	flushWg.Add(1)
	go func() {
		defer flushWg.Done()
		for b := range flushCh {
			if err := o.flush(b, &mu, &skipped); err != nil {
				fmt.Fprintf(os.Stderr, "write batch: %v\n", err)
			}
		}
	}()

	// 消费协程：填 batch → 满则交给 flushCh，换新 batch 继续
	consumeStart := time.Now()
	consumeCount := 0
	go func() {
		defer close(flushed)
		batch := newBatch()
		for item := range ch {
			if keep != nil && !keep(item) {
				continue
			}
			if item.Node != nil {
				batch.nodes = append(batch.nodes, item.Node)
			}
			if item.Fact != nil {
				batch.edges = append(batch.edges, item.Fact)
			}
			if item.Summary != nil {
				batch.summaries = append(batch.summaries, item.Summary)
			}
			if item.Origins != nil {
				batch.origins = append(batch.origins, item.Origins...)
			}
			consumeCount++
			if consumeCount%500 == 0 {
				logger.Info("consume progress", zap.Int("items", consumeCount),
					zap.Duration("elapsed", time.Since(consumeStart)))
			}
			if len(batch.nodes) >= BatchSize || len(batch.edges) >= BatchSize || len(batch.summaries) >= BatchSize || len(batch.origins) >= BatchSize {
				flushCh <- batch
				batch = newBatch()
			}
		}
		flushCh <- batch // 最后一批（空批 flush 内部跳过）
		close(flushCh)
	}()

	// 并行跑适配器（独立超时，失败不中断他人）
	var wg sync.WaitGroup
	for _, a := range o.Adapters {
		wg.Add(1)
		go func(adapter domain.IndexerPort) {
			defer wg.Done()
			adapterCtx, cancel := context.WithTimeout(ctx, AdapterTimeout)
			defer cancel()
			r := AdapterResult{Name: adapter.Name()}
			adapterStart := time.Now()
			r.Err = adapter.Index(adapterCtx, o.Repo, pkgs, func(item domain.Item) error {
				select {
				case ch <- item:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			})
			// 采样：channel 满时 send 阻塞耗时（backpressure 指标）
			bpMu.Lock()
			bpTotal += time.Since(adapterStart)
			bpMu.Unlock()
			r.Duration = time.Since(adapterStart)
			mu.Lock()
			results = append(results, r)
			mu.Unlock()
		}(a)
	}
	wg.Wait()
	logger.Info("orchestrator stage", zap.String("stage", "adapters done"),
		zap.Duration("elapsed", time.Since(runStart)))
	close(ch)
	<-flushed // 等待消费协程排空（含最后一批入 flushCh）
	flushWg.Wait() // 等待 flush 协程写完
	logger.Info("orchestrator stage", zap.String("stage", "flush done"),
		zap.Duration("elapsed", time.Since(runStart)))
	return results, skipped, nil
}

// finishBuild 汇总构建状态并写 build_metadata（TD.md 9.2 降级矩阵）。
func (o *Orchestrator) finishBuild(start time.Time, results []AdapterResult, skipped int, toolName string) (*BuildResult, error) {
	// 汇总状态（TD.md 9.2 降级矩阵）
	status := domain.BuildSuccess
	errorMsgs := ""
	for _, r := range results {
		if r.Err == nil {
			continue
		}
		if r.Name == "scip" {
			status = domain.BuildFailed // SCIP 失败 → 构建失败
		} else if status != domain.BuildFailed {
			status = domain.BuildDegraded
		}
		if errorMsgs != "" {
			errorMsgs += "; "
		}
		errorMsgs += fmt.Sprintf("%s: %v", r.Name, r.Err)
	}

	nodes, edges, _ := o.RepoImpl.Counts()
	duration := time.Since(start)

	build := &BuildResult{
		Status:       status,
		Nodes:        nodes,
		Edges:        edges,
		Duration:     duration,
		CommitSHA:    headCommitSHA(o.Repo.Path),
		Adapter:      results,
		SkippedEdges: skipped,
	}

	// 写构建元数据（Nodes/Edges：relations --memory auto 判断缓存，P0④）
	meta := &domain.BuildMeta{
		BuildID:    newBuildID(),
		CommitSHA:  build.CommitSHA,
		ToolName:   toolName,
		Status:     status,
		DurationMs: duration.Milliseconds(),
		ErrorMsg:   errorMsgs,
		Nodes:      nodes,
		Edges:      edges,
	}
	if err := o.RepoImpl.Save(meta); err != nil {
		return build, fmt.Errorf("save build metadata: %w", err)
	}
	return build, nil
}

type batchT struct {
	nodes     []*domain.CodeEntity
	edges     []*domain.Fact
	summaries []*domain.FunctionFieldSummary
	origins   []*domain.SummaryOrigin // Q161 摘要来源
}

func newBatch() *batchT {
	logger := zap.L()
	logger.Debug("enter newBatch")
	defer logger.Debug("exit newBatch")
	return &batchT{}
}

// flush 将当前批次写入数据库。
func (o *Orchestrator) flush(b *batchT, mu *sync.Mutex, skipped *int) error {
	logger := zap.L()
	logger.Debug("enter (Orchestrator).flush")
	defer logger.Debug("exit (Orchestrator).flush")
	if len(b.nodes) == 0 && len(b.edges) == 0 && len(b.summaries) == 0 {
		return nil
	}
	// Q168 诊断：flush 每次耗时（写库协程慢时定位）
	flushStart := time.Now()
	res, err := o.RepoImpl.SaveBatchStats(b.nodes, b.edges, b.summaries, b.origins)
	if time.Since(flushStart) > 100*time.Millisecond {
		logger.Info("flush slow", zap.Duration("elapsed", time.Since(flushStart)),
			zap.Int("nodes", len(b.nodes)), zap.Int("edges", len(b.edges)),
			zap.Int("summaries", len(b.summaries)), zap.Int("origins", len(b.origins)))
	}
	if err != nil {
		return err
	}
	mu.Lock()
	*skipped += res.SkippedEdges
	mu.Unlock()
	b.nodes = b.nodes[:0]
	b.edges = b.edges[:0]
	b.summaries = b.summaries[:0]
	return nil
}

// GetRepo 返回仓储（查询命令共用）。
func (o *Orchestrator) GetRepo() *sqlite.Repo {
	logger := zap.L()
	logger.Debug("enter (Orchestrator).GetRepo")
	defer logger.Debug("exit (Orchestrator).GetRepo")
	return o.RepoImpl
}

func newBuildID() string {
	logger := zap.L()
	logger.Debug("enter newBuildID")
	defer logger.Debug("exit newBuildID")
	h := sha256.Sum256([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
	return hex.EncodeToString(h[:8])
}

func headCommitSHA(repoPath string) string {
	logger := zap.L()
	logger.Debug("enter headCommitSHA")
	defer logger.Debug("exit headCommitSHA")
	if _, err := os.Stat(filepath.Join(repoPath, ".git")); err != nil {
		return ""
	}
	out, err := exec.Command("git", "-C", repoPath, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
