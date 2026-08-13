// Package orchestrator 实现 Index Orchestrator（TD.md 6.1 全量构建流程）：
//   - 并行启动所有适配器 goroutine，每个独立超时（默认 10 分钟）
//   - 适配器流式数据 → 分批（1000 条/事务）写入 SQLite
//   - 某适配器失败：已提交数据保留，构建标记降级
//   - SCIP 失败：构建失败（符号权威缺失）
package orchestrator

import (
	"context"
	"crypto/sha256"
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
	"go.uber.org/zap"
)

// 常量
const (
	AdapterTimeout = 10 * time.Minute // 适配器级超时（TD.md 9.1）
	BatchSize      = 1000             // 分批事务大小（TD.md 5.2）
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

// FullBuild 执行全量构建并返回报告（TD.md 5.2 并行流程）。
func (o *Orchestrator) FullBuild(ctx context.Context) (*BuildResult, error) {
	logger := logging.FromContext(ctx)
	logger.Debug("enter (Orchestrator).FullBuild")
	defer logger.Debug("exit (Orchestrator).FullBuild")
	start := time.Now()

	// 清空旧数据（全量重建语义）
	if _, err := o.RepoImpl.Exec("DELETE FROM edges"); err != nil {
		return nil, fmt.Errorf("clear edges: %w", err)
	}
	if _, err := o.RepoImpl.Exec("DELETE FROM nodes"); err != nil {
		return nil, fmt.Errorf("clear nodes: %w", err)
	}

	var (
		results []AdapterResult
		skipped int
		mu      sync.Mutex
	)
	// 适配器 → 数据通道 → 写库 goroutine（单写者）
	ch := make(chan domain.Item, 4096)
	flushed := make(chan struct{})

	// 写库协程：分批提交（1000 条/事务）
	batch := newBatch()
	go func() {
		defer close(flushed)
		for item := range ch {
			if item.Node != nil {
				batch.nodes = append(batch.nodes, item.Node)
			}
			if item.Fact != nil {
				batch.edges = append(batch.edges, item.Fact)
			}
			if len(batch.nodes) >= BatchSize || len(batch.edges) >= BatchSize {
				if err := o.flush(batch, &mu, &skipped); err != nil {
					fmt.Fprintf(os.Stderr, "write batch: %v\n", err)
				}
			}
		}
		if err := o.flush(batch, &mu, &skipped); err != nil {
			fmt.Fprintf(os.Stderr, "write final batch: %v\n", err)
		}
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
			r.Err = adapter.Index(adapterCtx, o.Repo, func(item domain.Item) error {
				select {
				case ch <- item:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			})
			r.Duration = time.Since(adapterStart)
			mu.Lock()
			results = append(results, r)
			mu.Unlock()
		}(a)
	}
	wg.Wait()
	close(ch)
	<-flushed // 等待写库协程排空

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

	// 写构建元数据
	meta := &domain.BuildMeta{
		BuildID:    newBuildID(),
		CommitSHA:  build.CommitSHA,
		ToolName:   "all",
		Status:     status,
		DurationMs: duration.Milliseconds(),
		ErrorMsg:   errorMsgs,
	}
	if err := o.RepoImpl.Save(meta); err != nil {
		return build, fmt.Errorf("save build metadata: %w", err)
	}
	return build, nil
}

type batchT struct {
	nodes []*domain.CodeEntity
	edges []*domain.Fact
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
	if len(b.nodes) == 0 && len(b.edges) == 0 {
		return nil
	}
	res, err := o.RepoImpl.SaveBatchStats(b.nodes, b.edges)
	if err != nil {
		return err
	}
	mu.Lock()
	*skipped += res.SkippedEdges
	mu.Unlock()
	b.nodes = b.nodes[:0]
	b.edges = b.edges[:0]
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
