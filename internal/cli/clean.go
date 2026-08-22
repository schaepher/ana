package cli

import (
	"flag"
	"fmt"
	"go.uber.org/zap"
	"os"
	"path/filepath"
)

// cmdClean 实现 `codeintel clean --repo <path>`（TD.md 10.2 数据清理）。
func cmdClean(args []string) int {
	logger := zap.L()
	logger.Debug("enter cmdClean")
	defer logger.Debug("exit cmdClean")
	fs := flag.NewFlagSet("clean", flag.ExitOnError)
	repoPath := fs.String("repo", ".", "仓库根目录")
	force := fs.Bool("force", false, "不提示直接删除")
	purgeCache := fs.Bool("purge-cache", false, "连包级分析缓存（.codeintel/cache）一起删除——默认保留")
	fs.Parse(args)
	*repoPath = ResolveRepoRef(*repoPath) // Q238：注册表短名/后缀/module

	abs, err := filepath.Abs(*repoPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	target := filepath.Join(abs, ".codeintel")
	if _, err := os.Stat(target); err != nil {
		fmt.Printf("没有找到索引目录 %s\n", target)
		return 0
	}
	if !*force {
		fmt.Printf("确定删除 %s ？输入 yes 确认: ", target)
		var answer string
		if _, err := fmt.Scanln(&answer); err != nil || answer != "yes" {
			fmt.Println("已取消")
			return 0
		}
	}
	if *purgeCache {
		if err := os.RemoveAll(target); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		fmt.Printf("已删除 %s（含包级分析缓存）\n", target)
		unregisterRepoAfterClean(abs)
		return 0
	}
	// 默认保留 .codeintel/cache（Q176 包级分析缓存）：pkg 源码 hash 自校验
	// + 格式版本——任何情况下不会用错缓存，删除纯浪费（重建时未变包
	// 的 emitFunction 可直接跳过）。磁盘清理用 --purge-cache。
	entries, err := os.ReadDir(target)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	for _, e := range entries {
		if e.Name() == "cache" {
			continue
		}
		if err := os.RemoveAll(filepath.Join(target, e.Name())); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
	}
	fmt.Printf("已删除索引 %s（保留 .codeintel/cache 包级分析缓存；--purge-cache 可一并删除）\n", target)
	// Q238：clean 注销全局台账（级联 worktree 条目）
	unregisterRepoAfterClean(abs)
	return 0
}
