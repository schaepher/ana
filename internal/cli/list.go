package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
)

// Q238 codeintel list：全局注册台账（design-q238.md §3.4）。
// 输出：短名(=目录名) 路径 module 状态 worktree归属(⊢主仓库) workspace。
// 状态机（Q15）：[missing]（目录不存在）> 未构建（build_id 空）>
// 过期（已构建但 HEAD 变）> 已构建（head 一致）。--stale 只筛过期，
// --unbuilt 筛未构建。注册表缺失/空 → 提示（不报错，Q12 非必需前置）。

// listStatus 条目状态（Q15 三态 + missing）。
type listStatus string

const (
	statusBuilt   listStatus = "已构建"
	statusStale   listStatus = "过期"
	statusUnbuilt listStatus = "未构建"
	statusMissing listStatus = "[missing]"
)

// listEntry 渲染条目。
type listEntry struct {
	short      string
	path       string
	module     string
	status     listStatus
	worktreeOf string // 主仓库短名（空=非 worktree）
	workspace  string
}

func statusOf(rp sqlite.RegistryRepo) listStatus {
	if _, err := os.Stat(rp.Path); err != nil {
		return statusMissing
	}
	if rp.BuildID == "" {
		return statusUnbuilt
	}
	if head := gitHead(rp.Path); head != "" && head != rp.HeadCommit {
		return statusStale
	}
	return statusBuilt
}

// cmdList 实现 `codeintel list [--worktree-of <p>] [--workspace <p>]
// [--module <片段>] [--stale] [--unbuilt] [--json]`。
func cmdList(args []string) int {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	worktreeOf := fs.String("worktree-of", "", "只看该主仓库的 worktree 条目（短名/路径）")
	workspace := fs.String("workspace", "", "只看该 workspace 的条目（目录短名/路径）")
	module := fs.String("module", "", "module 名片段过滤")
	stale := fs.Bool("stale", false, "只看过期（已构建但 HEAD 变更）")
	unbuilt := fs.Bool("unbuilt", false, "只看未构建")
	asJSON := fs.Bool("json", false, "JSON 数组输出")
	fs.Parse(args)

	r := openGlobalRegistry()
	if r == nil {
		fmt.Println("全局注册表不可用（~/.codeintel）")
		return 1
	}
	defer r.Close()
	repos, err := r.ListRepos()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if len(repos) == 0 {
		fmt.Println("没有已注册的仓库（先运行 codeintel init）")
		return 0
	}

	// 过滤值解析：--worktree-of 支持注册表短名（Q6）
	wtTarget := *worktreeOf
	if wtTarget != "" {
		wtTarget = ResolveRepoRef(wtTarget)
	}

	var entries []listEntry
	for _, rp := range repos {
		if wtTarget != "" && rp.WorktreeOf != wtTarget {
			continue
		}
		if *workspace != "" && rp.Workspace != *workspace &&
			filepath.Base(rp.Workspace) != *workspace {
			continue
		}
		if *module != "" && !strings.Contains(rp.Module, *module) {
			continue
		}
		st := statusOf(rp)
		if *stale && st != statusStale {
			continue
		}
		if *unbuilt && st != statusUnbuilt {
			continue
		}
		wtShort := ""
		if rp.WorktreeOf != "" {
			wtShort = filepath.Base(rp.WorktreeOf)
		}
		entries = append(entries, listEntry{
			short:      filepath.Base(rp.Path),
			path:       rp.Path,
			module:     rp.Module,
			status:     st,
			worktreeOf: wtShort,
			workspace:  rp.Workspace,
		})
	}

	if *asJSON {
		type entryJSON struct {
			Short      string `json:"short"`
			Path       string `json:"path"`
			Module     string `json:"module"`
			Status     string `json:"status"`
			WorktreeOf string `json:"worktree_of,omitempty"`
			Workspace  string `json:"workspace,omitempty"`
		}
		arr := make([]entryJSON, 0, len(entries))
		for _, e := range entries {
			arr = append(arr, entryJSON{e.short, e.path, e.module,
				string(e.status), e.worktreeOf, e.workspace})
		}
		b, _ := json.Marshal(arr)
		fmt.Println(string(b))
		return 0
	}

	for _, e := range entries {
		line := fmt.Sprintf("%-24s %-52s %-28s %s", e.short, e.path, e.module, e.status)
		if e.worktreeOf != "" {
			line += fmt.Sprintf("  ⊢ %s", e.worktreeOf)
		}
		if e.workspace != "" {
			line += fmt.Sprintf("  [%s]", filepath.Base(e.workspace))
		}
		fmt.Println(line)
	}
	return 0
}
