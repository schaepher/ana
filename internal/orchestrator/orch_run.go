package orchestrator

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/schaepher/codeintel/internal/domain"
	"github.com/schaepher/codeintel/internal/infrastructure/ast"
	"github.com/schaepher/codeintel/internal/infrastructure/git"
	"github.com/schaepher/codeintel/internal/infrastructure/scip"
	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
	"github.com/schaepher/codeintel/internal/infrastructure/ssa"
	"github.com/schaepher/codeintel/internal/logging"
	"go.uber.org/zap"
	"golang.org/x/tools/go/packages"
)

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

// runAdapters 并行执行适配器并写库（keep 为 nil 时全部写入；否则只保留
// keep(item) 为 true 的条目）。pkgs 为共享加载的 go/packages 结果
// （AST/SSA 复用，避免重复类型检查）。返回各适配器结果与跳过的 FK 冲突边数。
func (o *Orchestrator) runAdapters(ctx context.Context, pkgs []*packages.Package, keep func(domain.Item) bool, changedFiles []string) ([]AdapterResult, int, error) {
	logger := logging.FromContext(ctx)
	runStart := time.Now()

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

	ch := make(chan domain.Item, 4096)
	// Q174：backpressure 采集——producer 因 channel 满阻塞的时间占比
	// （flush 慢时生产 worker 阻塞于写库；日志显示 CPU vs wall）
	var bpTotal time.Duration
	var bpMu sync.Mutex
	flushCh := make(chan *batchT, 2)
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
		flushCh <- batch
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
	<-flushed
	flushWg.Wait()

	o.retryFailedFK(&skipped)
	logger.Info("orchestrator stage", zap.String("stage", "flush done"),
		zap.Duration("elapsed", time.Since(runStart)))
	return results, skipped, nil
}

// finishBuild 汇总构建状态并写 build_metadata（TD.md 9.2 降级矩阵）。
func (o *Orchestrator) finishBuild(start time.Time, results []AdapterResult, skipped int, toolName string) (*BuildResult, error) {

	status := domain.BuildSuccess
	errorMsgs := ""
	for _, r := range results {
		if r.Err == nil {
			continue
		}
		if r.Name == "scip" {
			status = domain.BuildFailed
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

	o.failedEdges = append(o.failedEdges, res.FailedEdges...)
	o.failedSummaries = append(o.failedSummaries, res.FailedSummaries...)
	o.failedOrigins = append(o.failedOrigins, res.FailedOrigins...)
	b.nodes = b.nodes[:0]
	b.edges = b.edges[:0]
	b.summaries = b.summaries[:0]
	return nil
}

// retryFailedFK 构建尾部重试 FK 失败项（P2）：全部节点落库后，跨批
// 依赖已满足 → 绝大多数重试成功；仍失败的为真缺节点（如 Git 追踪到
// 未索引文件），计入跳过数（SkippedEdges 语义：最终跳过）。
func (o *Orchestrator) retryFailedFK(skipped *int) {
	logger := zap.L()
	if len(o.failedEdges) == 0 && len(o.failedSummaries) == 0 && len(o.failedOrigins) == 0 {
		return
	}
	logger.Info("retry FK-failed items",
		zap.Int("edges", len(o.failedEdges)),
		zap.Int("summaries", len(o.failedSummaries)),
		zap.Int("origins", len(o.failedOrigins)))
	res, err := o.RepoImpl.SaveBatchStats(nil, o.failedEdges, o.failedSummaries, o.failedOrigins)
	o.failedEdges = nil
	o.failedSummaries = nil
	o.failedOrigins = nil
	if err != nil {
		logger.Warn("retry FK-failed items", zap.Error(err))
		return
	}

	*skipped += res.SkippedEdges
}

// GetRepo 返回仓储（查询命令共用）。
func (o *Orchestrator) GetRepo() *sqlite.Repo {
	logger := zap.L()
	logger.Debug("enter (Orchestrator).GetRepo")
	defer logger.Debug("exit (Orchestrator).GetRepo")
	return o.RepoImpl
}
