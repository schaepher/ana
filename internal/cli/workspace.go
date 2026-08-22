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

	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
)

// Q238 workspace（design-q238.md §3.4）：把已注册仓库按需求创建 git
// worktree 到 workspace 目录。
//
//	codeintel workspace init --dir <目录> [--repo <子集>...] [--build] [--branch <b>]
//	  目录不存在自动创建；已存在幂等（该仓库已有 worktree 跳过）；
//	  默认只创建 worktree + 注册（未构建），--build 时逐个 init 构建；
//	  分支缺省 detached（git 不允许同分支双 worktree），--branch 显式
//	  创建命名分支（-b）；单仓库失败不中断，汇总报告（有失败 exit 非零）。
//	codeintel workspace prune
//	  扫描注册条目，目录不存在的清理（worktree 与主仓库条目）。

func cmdWorkspace(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "用法: codeintel workspace init|prune …")
		return 2
	}
	switch args[0] {
	case "init":
		return cmdWorkspaceInit(args[1:])
	case "prune":
		return cmdWorkspacePrune(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown workspace subcommand: %s\n", args[0])
		return 2
	}
}

// addWorktree git worktree add（目录已存在即视为幂等命中——由调用方判断）。
// 无 --branch 时 --detach（当前 commit）——默认分支（如 main）已被主仓库
// checkout，git 不允许两个 worktree 同分支；--branch 指定时 -b 创建新分支
// （worktree add 不会自动建分支；新分支不与主仓库冲突）。
func addWorktree(main, wtDir, branch string) error {
	args := []string{"-C", main, "worktree", "add"}
	if branch != "" {
		args = append(args, "-b", branch, wtDir)
	} else {
		args = append(args, "--detach", wtDir)
	}
	out, err := exec.Command("git", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("git worktree add: %v\n%s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// cmdWorkspaceInit 实现 workspace init。
func cmdWorkspaceInit(args []string) int {
	// --repo 子集先手动提取（可重复，flag 包不支持）再解析其余
	var subset []string
	var restArgs []string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--repo" && i+1 < len(args):
			subset = append(subset, args[i+1])
			i++
		case strings.HasPrefix(args[i], "--repo="):
			subset = append(subset, strings.TrimPrefix(args[i], "--repo="))
		default:
			restArgs = append(restArgs, args[i])
		}
	}
	fs := flag.NewFlagSet("workspace init", flag.ExitOnError)
	dir := fs.String("dir", ".", "workspace 目录（默认当前目录；不存在自动创建）")
	build := fs.Bool("build", false, "创建后逐个 init 构建索引（默认只建 worktree 注册为未构建）")
	branch := fs.String("branch", "", "worktree 分支（默认各仓库当前分支）")
	fs.Parse(restArgs)

	wsDir, err := filepath.Abs(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "error: 创建 workspace 目录: %v\n", err)
		return 1
	}

	r := openGlobalRegistry()
	if r == nil {
		fmt.Fprintln(os.Stderr, "error: 全局注册表不可用")
		return 1
	}
	defer r.Close()
	repos, err := r.ListRepos()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	// 子集解析（Q6 短名/后缀/module）→ 目标 = 主仓库条目（is_worktree=0）
	var targets []sqlite.RegistryRepo
	if len(subset) > 0 {
		for _, s := range subset {
			ref := ResolveRepoRef(s)
			for _, rp := range repos {
				if rp.Path == ref && !rp.IsWorktree {
					targets = append(targets, rp)
					break
				}
			}
		}
	} else {
		for _, rp := range repos {
			if !rp.IsWorktree {
				targets = append(targets, rp)
			}
		}
	}
	if len(targets) == 0 {
		fmt.Println("没有可创建 worktree 的主仓库（先 codeintel init 注册，或用 --repo 指定）")
		return 0
	}

	fmt.Printf("workspace: %s（%d 个仓库）\n", wsDir, len(targets))
	var created, skipped, failed int
	for _, rp := range targets {
		short := filepath.Base(rp.Path)
		wtDir := filepath.Join(wsDir, short)
		if _, err := os.Stat(wtDir); err == nil {
			fmt.Printf("  跳过 %s（%s 已存在）\n", short, wtDir)
			skipped++
			continue
		}
		// 分支：--branch 显式命名（git 自动创建，不与主仓库冲突）；
		// 缺省 detached（Q238 实施修订：git 不允许同一分支双 worktree
		// checkout——默认当前分支如 main 已被主仓库占用必失败）
		br := *branch
		if err := addWorktree(rp.Path, wtDir, br); err != nil {
			fmt.Printf("  失败 %s: %v\n", short, err)
			failed++
			continue
		}
		stamp := time.Now().UTC().Format(time.RFC3339)
		if err := r.RegisterRepo(sqlite.RegistryRepo{
			Path: wtDir, Module: rp.Module, GoModCount: rp.GoModCount,
			IsWorktree: true, WorktreeOf: rp.Path, Workspace: wsDir,
			RegisteredAt: stamp,
		}); err != nil {
			fmt.Printf("  失败 %s: 注册 worktree: %v\n", short, err)
			failed++
			continue
		}
		fmt.Printf("  已创建 %s → %s（分支 %s）\n", short, wtDir, br)
		created++
		if *build {
			// Q9c：--build 时逐个构建（cmdInit 会注册/刷新该 worktree 条目，
			// registerRepoAfterBuild 保留 workspace 归属）
			if code := cmdInit(context.Background(), []string{"--repo", wtDir}); code != 0 {
				fmt.Printf("  失败 %s: 构建索引 exit=%d\n", short, code)
				failed++
			}
		}
	}
	fmt.Printf("汇总: 创建 %d，跳过 %d，失败 %d\n", created, skipped, failed)
	if failed > 0 {
		return 1
	}
	return 0
}

// cmdWorkspacePrune 清理目录不存在的注册条目（Q11：list 标 [missing]，
// prune 显式清理）。
func cmdWorkspacePrune(args []string) int {
	fs := flag.NewFlagSet("workspace prune", flag.ExitOnError)
	fs.Parse(args)

	r := openGlobalRegistry()
	if r == nil {
		fmt.Fprintln(os.Stderr, "error: 全局注册表不可用")
		return 1
	}
	defer r.Close()
	repos, err := r.ListRepos()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	var removed int
	for _, rp := range repos {
		if _, err := os.Stat(rp.Path); err == nil {
			continue
		}
		if err := r.UnregisterRepo(rp.Path); err != nil {
			fmt.Printf("  失败 %s: %v\n", rp.Path, err)
			continue
		}
		// UnregisterRepo 级联删 worktree_of 指向它的条目——若删的是
		// worktree 条目自身，级联不影响主仓库
		fmt.Printf("  已清理 %s\n", rp.Path)
		removed++
	}
	fmt.Printf("清理完成: %d 条\n", removed)
	return 0
}
