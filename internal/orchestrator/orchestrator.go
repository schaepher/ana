// Package orchestrator 实现 Index Orchestrator（TD.md 6.1 全量构建流程）：
//   - 并行启动所有适配器 goroutine，每个独立超时（默认 10 分钟）
//   - 适配器流式数据 → 分批（1000 条/事务）写入 SQLite
//   - 某适配器失败：已提交数据保留，构建标记降级
//   - SCIP 失败：构建失败（符号权威缺失）
package orchestrator

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/schaepher/codeintel/internal/domain"
	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
	"go.uber.org/zap"
)

// 常量
const (
	AdapterTimeout = 10 * time.Minute // 适配器级超时（TD.md 9.1）
	BatchSize      = 20000            // 分批事务大小（TD.md 5.2；Q171/Q174 双缓冲+加大摊薄事务——大仓库 36 万 item 时减少 flush 次数；Q221：10000→20000 减半 cgocall——pprof 16% 在 SQLite C 调用）
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

	// P2 跨批 FK 收集：flush 时端点节点尚未落库的边/摘要/来源，构建尾部
	// （全部节点落库后）统一重试——原实现静默跳过导致非确定性丢边。
	// 仅 flush 协程写、finish 阶段读（flushCh 关闭 + flushWg.Wait 同步）。
	failedEdges     []*domain.Fact
	failedSummaries []*domain.FunctionFieldSummary
	failedOrigins   []*domain.SummaryOrigin
}

type batchT struct {
	nodes     []*domain.CodeEntity
	edges     []*domain.Fact
	summaries []*domain.FunctionFieldSummary
	origins   []*domain.SummaryOrigin // Q161 摘要来源
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
