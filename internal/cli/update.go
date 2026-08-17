package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/schaepher/codeintel/internal/domain"
	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
	"github.com/schaepher/codeintel/internal/infrastructure/ssa"
	"github.com/schaepher/codeintel/internal/logging"
	"github.com/schaepher/codeintel/internal/orchestrator"
	"go.uber.org/zap"
)

// cmdUpdate 实现 `codeintel update --repo <path>`（增量更新，TD.md 5.2 增量语义）：
// git 检测变更的 .go 文件 → 删除其旧数据 → 全量分析 + 只写变更文件相关数据。
// go.mod / go.work 变更影响 module 范围，须全量 init。
func cmdUpdate(ctx context.Context, args []string) int {
	logger := zap.L()
	logger.Debug("enter cmdUpdate")
	defer logger.Debug("exit cmdUpdate")
	fs := flag.NewFlagSet("update", flag.ExitOnError)
	repoPath := fs.String("repo", "", "仓库根目录（须已运行 codeintel init 且为 git 仓库）")
	workers := fs.Int("workers", 1, "SSA 分析按包并发数（默认 1=串行）")
	fs.Parse(args)

	if *repoPath == "" {
		fmt.Fprintln(os.Stderr, "error: --repo is required")
		return 2
	}
	abs, err := filepath.Abs(*repoPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: resolve repo path: %v\n", err)
		return 1
	}
	if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
		fmt.Fprintf(os.Stderr, "error: %s is not a directory\n", abs)
		return 1
	}
	repo, err := buildRepo(abs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	// 变更检测：git diff（已跟踪修改/删除/新增）+ 未跟踪文件
	changed, err := detectChangedGoFiles(abs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if len(changed) == 0 {
		fmt.Println("无变更的 .go 文件（索引已是最新）")
		return 0
	}
	// module 级文件变更：影响模块范围，提示全量重建
	for _, f := range changed {
		if f == "go.mod" || f == "go.work" {
			fmt.Fprintf(os.Stderr, "error: %s 已变更，影响模块范围，请运行: codeintel init --repo %s\n", f, abs)
			return 1
		}
	}

	// Q182：分析器版本变化（codeintel 新特性/逻辑变更）→ 自动降级全量
	// 重建（增量写库范围无法覆盖未变更包，新特性须全量生效）
	degraded := ssa.LoadAnalyzerMarker(abs) != ssa.AnalyzerVersionHash()
	if degraded {
		fmt.Printf("分析器版本变化（codeintel 新特性/逻辑变更），本次执行全量重建——未变更包也将以新逻辑重建\n")
	}
	fmt.Printf("增量更新: %s (%d 个文件变更)\n", abs, len(changed))
	for _, f := range changed {
		fmt.Printf("  - %s\n", f)
	}
	_ = degraded
	// 日志切换到 .codeintel/codeintel.log（stdout 只留查询结果，Q88）
	if err := logging.ToFile(abs); err != nil {
		fmt.Fprintf(os.Stderr, "warning: 日志切换失败: %v\n", err)
	}
	db, err := sqlite.Open(abs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()

	orch := orchestrator.New(repo, db)
	orch.SetWorkers(*workers)
	result, err := orch.IncrementalBuild(ctx, changed)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	// 构建报告（TD.md 6.1，tool_name=incremental）
	fmt.Println()
	fmt.Println("===== 增量更新报告 =====")
	for _, a := range result.Adapter {
		mark := "ok"
		if a.Err != nil {
			mark = "FAILED: " + a.Err.Error()
		}
		fmt.Printf("  %-10s %s (%s)\n", a.Name, mark, a.Duration.Round(time.Millisecond))
	}
	fmt.Printf("  变更文件: %d\n", len(changed))
	fmt.Printf("  符号数:   %d\n", result.Nodes)
	fmt.Printf("  边数:     %d\n", result.Edges)
	if result.SkippedEdges > 0 {
		fmt.Printf("  跳过边:   %d\n", result.SkippedEdges)
	}
	fmt.Printf("  状态:     %s\n", result.Status)
	fmt.Printf("  耗时:     %s\n", result.Duration.Round(time.Millisecond))
	fmt.Println("=========================")
	if result.Status == domain.BuildFailed {
		fmt.Fprintln(os.Stderr, "增量更新失败：SCIP 符号索引不可用。请检查 scip-go 是否安装。")
		return 1
	}
	if result.Status == domain.BuildDegraded {
		fmt.Fprintln(os.Stderr, "警告：增量更新降级完成（部分工具失败，已保留可用数据）。")
	}
	return 0
}

// detectChangedGoFiles 检测仓库中变更的 Go 源文件（相对路径）：
//   - git diff --name-only HEAD：已跟踪文件的修改/删除/新增
//   - git ls-files --others --exclude-standard：未跟踪文件（含新文件）
//
// 返回 .go 文件与 go.mod/go.work（module 级变更由调用方处理）。
func detectChangedGoFiles(repoPath string) ([]string, error) {
	logger := zap.L()
	logger.Debug("enter detectChangedGoFiles")
	defer logger.Debug("exit detectChangedGoFiles")
	// 非 git 仓库：无法增量，提示 init
	if _, err := os.Stat(filepath.Join(repoPath, ".git")); err != nil {
		return nil, fmt.Errorf("%s 不是 git 仓库（增量更新需要 git；首次构建请用 init）", repoPath)
	}
	seen := map[string]bool{}
	var out []string
	add := func(list string) {
		for _, f := range strings.Split(strings.TrimSpace(list), "\n") {
			f = strings.TrimSpace(f)
			if f == "" || seen[f] {
				continue
			}
			if strings.HasSuffix(f, ".go") || f == "go.mod" || f == "go.work" {
				seen[f] = true
				out = append(out, f)
			}
		}
	}
	if b, err := exec.Command("git", "-C", repoPath, "diff", "--name-only", "HEAD").Output(); err == nil {
		add(string(b))
	}
	if b, err := exec.Command("git", "-C", repoPath, "ls-files", "--others", "--exclude-standard").Output(); err == nil {
		add(string(b))
	}
	sort.Strings(out)
	return out, nil
}

// staleInfo 索引过期检测（field_trace.md §20.3）：build_metadata 最新
// timestamp 早于 git HEAD commit 时间 → 返回提示文本；非 git 仓库 /
// 无构建记录 / 不过期 → 返回空。
func staleInfo(repoAbs string, r *sqlite.Repo) string {
	head, err := exec.Command("git", "-C", repoAbs, "log", "-1", "--format=%ct").Output()
	if err != nil {
		return "" // 非 git 仓库：无法比较
	}
	headTs, err := strconv.ParseInt(strings.TrimSpace(string(head)), 10, 64)
	if err != nil || headTs <= 0 {
		return ""
	}
	var buildTs int64
	if err := r.QueryRow(`SELECT timestamp FROM build_metadata
		ORDER BY timestamp DESC, rowid DESC LIMIT 1`).Scan(&buildTs); err != nil {
		return "" // 无构建记录
	}
	if buildTs < headTs {
		return fmt.Sprintf("索引可能过期（构建于 %s，HEAD 更新于 %s）；运行 codeintel update",
			time.Unix(buildTs, 0).Format("01-02 15:04"), time.Unix(headTs, 0).Format("01-02 15:04"))
	}
	return ""
}
