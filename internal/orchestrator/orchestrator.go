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

// New 创建 Orchestrator，默认挂载 MVP 适配器（SCIP/AST/Git）。

// SetWorkers 设置 ssa 适配器按包并发数（Q170：CLI --workers 注入）。

// FullBuild 执行全量构建并返回报告（TD.md 5.2 并行流程）。

// IncrementalBuild 增量构建（TD.md 5.2 增量语义，MVP：全量分析 + 增量写入）：
//  1. 删除变更文件旧数据（节点级联删边与摘要）
//  2. 适配器全量运行，写库时只保留与变更文件相关的产出
//     （节点 file_path ∈ 变更文件；边/摘要的端点属于变更文件）
//  3. build_metadata 记录 tool_name=incremental
//
// 语义正确性：全量分析保证跨包间接写闭包等结果完整（分析成本与 init 相同），
// 增量只裁剪写入范围——未变更数据原样保留，不产生全量 DELETE 的碎片。

// loadPackages 统一加载仓库 go/packages（内存优化：AST/SSA 适配器共享
// 一次类型检查，避免各自 Load 翻倍）。返回共享结果供适配器复用。
// loadPackages 统一加载仓库 go/packages（内存优化：AST/SSA 适配器共享
// 一次类型检查，避免各自 Load 翻倍）。返回共享结果供适配器复用。
// P2-3 多 go.mod：每个 module 单独 Load（go/packages 不能跨 module），
// 按 PkgPath 去重合并（同一包路径只属于一个 module，Go 语义保证）。

// DiscoverModules 递归扫描仓库根下的 go.mod（跳过 .git/.codeintel/vendor/
// node_modules），返回 module 路径与相对仓库根的目录（根 go.mod 在前）。
// P2-3 多 go.mod monorepo。

// readGoModModule 解析 go.mod 的 module 指令。

// deleteFiles 删除指定文件的节点（级联删边与摘要行）；分批避免 SQLite
// 参数上限（999）。

// runAdapters 并行执行适配器并写库（keep 为 nil 时全部写入；否则只保留
// keep(item) 为 true 的条目）。pkgs 为共享加载的 go/packages 结果
// （AST/SSA 复用，避免重复类型检查）。返回各适配器结果与跳过的 FK 冲突边数。

// finishBuild 汇总构建状态并写 build_metadata（TD.md 9.2 降级矩阵）。

type batchT struct {
	nodes     []*domain.CodeEntity
	edges     []*domain.Fact
	summaries []*domain.FunctionFieldSummary
	origins   []*domain.SummaryOrigin // Q161 摘要来源
}

// flush 将当前批次写入数据库。

// retryFailedFK 构建尾部重试 FK 失败项（P2）：全部节点落库后，跨批
// 依赖已满足 → 绝大多数重试成功；仍失败的为真缺节点（如 Git 追踪到
// 未索引文件），计入跳过数（SkippedEdges 语义：最终跳过）。

// GetRepo 返回仓储（查询命令共用）。

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
