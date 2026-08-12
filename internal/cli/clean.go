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
	fs.Parse(args)

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
	if err := os.RemoveAll(target); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	fmt.Printf("已删除 %s\n", target)
	return 0
}
