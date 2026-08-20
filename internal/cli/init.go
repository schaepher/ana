package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/schaepher/codeintel/internal/domain"
	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
	"github.com/schaepher/codeintel/internal/logging"
	"github.com/schaepher/codeintel/internal/orchestrator"
	"go.uber.org/zap"
)

// cmdInit 实现 `codeintel init --repo <path>`（TD.md 6.1）。
func cmdInit(ctx context.Context, args []string) int {
	logger := zap.L()
	logger.Debug("enter cmdInit")
	defer logger.Debug("exit cmdInit")
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	repoPath := fs.String("repo", "", "仓库根目录（含 go.mod）")
	workers := fs.Int("workers", defaultBuildWorkers(), "SSA 分析按包并发数（Q221：默认 min(NumCPU, 8)——8 核冷启动 5m16s→40s，峰值 RSS ~2.9G；小内存机器可调小，如 1）")
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
	// go.work 检测（field_trace.md §1.3）：workspace 根或上层目录存在 go.work
	// 且当前目录非 module 根时，提示进入具体模块目录
	if w := findGoWork(abs); w != "" {
		if _, err := os.Stat(filepath.Join(abs, "go.mod")); err != nil {
			fmt.Fprintf(os.Stderr, "error: 检测到 go.work（%s）。请进入具体模块目录后运行：codeintel init --repo <模块目录>\n", w)
			return 1
		}
	}
	repo, err := buildRepo(abs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	fmt.Printf("构建索引: %s (module=%s, %d 个 go.mod)\n", abs, repo.Module, len(repo.Modules))
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
	result, err := orch.FullBuild(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	// 全量重建后 VACUUM 整理碎片（field_trace.md §9：定期执行 VACUUM）
	if _, err := db.Exec("VACUUM"); err != nil {
		fmt.Fprintf(os.Stderr, "warning: VACUUM: %v\n", err)
	}

	// 构建报告（TD.md 6.1）
	fmt.Println()
	fmt.Println("===== 构建报告 =====")
	for _, a := range result.Adapter {
		mark := "ok"
		if a.Err != nil {
			mark = "FAILED: " + a.Err.Error()
		}
		fmt.Printf("  %-10s %s (%s)\n", a.Name, mark, a.Duration.Round(time.Millisecond))
	}
	fmt.Printf("  符号数: %d\n", result.Nodes)
	fmt.Printf("  边数:   %d\n", result.Edges)
	if result.SkippedEdges > 0 {
		fmt.Printf("  跳过边: %d (端点非索引对象)\n", result.SkippedEdges)
	}
	fmt.Printf("  状态:   %s\n", result.Status)
	fmt.Printf("  耗时:   %s\n", result.Duration.Round(time.Millisecond))
	if result.CommitSHA != "" {
		fmt.Printf("  HEAD:   %s\n", result.CommitSHA[:12])
	}
	fmt.Println("=========================")

	if result.Status == domain.BuildFailed {
		fmt.Fprintln(os.Stderr, "构建失败：SCIP 符号索引不可用。请检查 scip-go 是否安装。")
		return 1
	}
	if result.Status == domain.BuildDegraded {
		fmt.Fprintln(os.Stderr, "警告：构建降级完成（部分工具失败，已保留可用数据）。")
	}
	fmt.Printf("数据库: %s/.codeintel/codeintel.db\n", abs)
	return 0
}

// buildRepo 解析仓库的 module 信息（P2-3 多 go.mod monorepo）：
// 递归扫描根下所有 go.mod，构造 Repository（根 module 在前）。
func buildRepo(repoPath string) (*domain.Repository, error) {
	modules, dirs, err := orchestrator.DiscoverModules(repoPath)
	if err != nil {
		return nil, err
	}
	return &domain.Repository{
		Path:       repoPath,
		Module:     modules[0],
		Modules:    modules,
		ModuleDirs: dirs,
	}, nil
}

// readGoModule 读取 go.mod 的 module 行。
func readGoModule(repoPath string) (string, error) {
	logger := zap.L()
	logger.Debug("enter readGoModule")
	defer logger.Debug("exit readGoModule")
	data, err := os.ReadFile(filepath.Join(repoPath, "go.mod"))
	if err != nil {
		return "", fmt.Errorf("read go.mod (repo must be a Go module): %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			m := strings.TrimSpace(strings.TrimPrefix(line, "module "))
			if i := strings.Index(m, " "); i >= 0 {
				m = m[:i]
			}
			return m, nil
		}
	}
	return "", fmt.Errorf("no module directive found in go.mod")
}

// resolveRepo 从参数解析仓库路径（默认当前目录），并验证存在 go.mod。
func resolveRepo(repoPath string) (string, string, error) {
	logger := zap.L()
	logger.Debug("enter resolveRepo")
	defer logger.Debug("exit resolveRepo")
	if repoPath == "" {
		repoPath = "."
	}
	abs, err := filepath.Abs(repoPath)
	if err != nil {
		return "", "", err
	}
	module, err := readGoModule(abs)
	if err != nil {
		return "", "", err
	}
	return abs, module, nil
}

// ensureGoEnv 检查 go 与 scip-go 可用（供诊断信息使用）。
func ensureGoEnv() error {
	logger := zap.L()
	logger.Debug("enter ensureGoEnv")
	defer logger.Debug("exit ensureGoEnv")
	if _, err := exec.LookPath("go"); err != nil {
		return fmt.Errorf("go not found in PATH")
	}
	return nil
}

// findGoWork 从目录向上（最多 4 层）查找 go.work；未找到返回空串。
func findGoWork(dir string) string {
	for i := 0; i < 4 && dir != "" && dir != "/"; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.work")); err == nil {
			return filepath.Join(dir, "go.work")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

// defaultBuildWorkers Q221：构建默认并发数 = min(NumCPU, 8)（1..8）——
// 8 核实测冷启动 5m16s → 40s（峰值 RSS ~2.9G）；上限 8 防大机器内存
// 翻倍。小内存机器可 --workers 1 调小。
func defaultBuildWorkers() int {
	n := runtime.NumCPU()
	if n > 8 {
		n = 8
	}
	if n < 1 {
		n = 1
	}
	return n
}
