package cli

import (
	"os"
	"path/filepath"
	"strings"
)

// Q238 worktree 检测（design-q238.md §3.2）：注册时判定目标仓库是否
// git worktree 及主仓库路径。
//
// 形态：
//   - <path>/.git 是目录 → 主仓库（非 worktree）
//   - <path>/.git 是文件且内容 "gitdir: X" → worktree；X 形如
//     <主仓库>/.git/worktrees/<name>，主仓库 = dirname(X 中 "/.git/" 前缀)
//   - 无 .git → 非 git 仓库（注册照常，head 留空）
func detectWorktree(path string) (isWorktree bool, mainPath string) {
	gitDot := filepath.Join(path, ".git")
	fi, err := os.Stat(gitDot)
	if err != nil {
		return false, ""
	}
	if fi.IsDir() {
		return false, ""
	}
	b, err := os.ReadFile(gitDot)
	if err != nil {
		return false, ""
	}
	line := strings.TrimSpace(string(b))
	if !strings.HasPrefix(line, "gitdir:") {
		return false, ""
	}
	gitdir := strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
	// gitdir 形如 <主仓库>/.git/worktrees/<name> → 主仓库 = dirname 前缀
	if i := strings.Index(gitdir, "/.git/"); i > 0 {
		main := filepath.Dir(gitdir[:i+len("/.git")])
		if fi, err := os.Stat(filepath.Join(main, ".git")); err == nil && fi.IsDir() {
			return true, main
		}
		return true, ""
	}
	return true, ""
}
