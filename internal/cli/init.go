package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/schaepher/codeintel/internal/domain"
	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
	"github.com/schaepher/codeintel/internal/orchestrator"
	"go.uber.org/zap"
)

// cmdInit 实现 `codeintel init --repo <path>`（TD.md 6.1）。
func cmdInit(args []string) int {
	logger := zap.L()
	logger.Debug("enter cmdInit")
	defer logger.Debug("exit cmdInit")
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	repoPath := fs.String("repo", "", "仓库根目录（含 go.mod）")
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
	module, err := readGoModule(abs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	fmt.Printf("构建索引: %s (module=%s)\n", abs, module)
	db, err := sqlite.Open(abs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()

	orch := orchestrator.New(&domain.Repository{Path: abs, Module: module}, db)
	result, err := orch.FullBuild(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
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
