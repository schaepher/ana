package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
	"go.uber.org/zap"
)

// Q238 全局注册表接入（design-q238.md §3.2）：init/reindex 成功注册、
// update 成功刷新、clean 注销（级联 worktree）。注册失败仅警告——
// 注册表从不作为命令必需前置（Q12）。

// registryDirFn 全局注册表目录（可注入；默认 ~/.codeintel）。
// cli 包测试经 TestMain 注入临时目录，避免写真实 home。
var registryDirFn = func() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(h, ".codeintel")
}

// openGlobalRegistry 打开全局注册表（目录缺失自动创建/重建）。
func openGlobalRegistry() *sqlite.Registry {
	dir := registryDirFn()
	if dir == "" {
		return nil
	}
	r, err := sqlite.OpenRegistry(dir)
	if err != nil {
		zap.L().Warn("全局注册表不可用（命令继续）", zap.Error(err))
		return nil
	}
	return r
}

// gitHead 取仓库当前 HEAD（非 git 仓库/失败返回空）。
func gitHead(repoPath string) string {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = repoPath
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// registerRepoAfterBuild 构建成功注册（init/reindex 共用）。commitSHA
// 为空时回退 git rev-parse。
func registerRepoAfterBuild(abs, module string, goModCount int, commitSHA string) {
	r := openGlobalRegistry()
	if r == nil {
		return
	}
	defer r.Close()
	head := commitSHA
	if head == "" {
		head = gitHead(abs)
	}
	isWT, wtOf := detectWorktree(abs)
	stamp := time.Now().UTC().Format(time.RFC3339)
	repo := sqlite.RegistryRepo{
		Path:         abs,
		Module:       module,
		GoModCount:   goModCount,
		HeadCommit:   head,
		BuildID:      head,
		LastBuiltAt:  stamp,
		IsWorktree:   isWT,
		WorktreeOf:   wtOf,
		RegisteredAt: stamp,
	}
	if err := r.RegisterRepo(repo); err != nil {
		fmt.Fprintf(os.Stderr, "warning: 全局注册失败: %v\n", err)
	}
}

// refreshRepoAfterUpdate update 成功刷新构建状态（registered_at 不变）。
func refreshRepoAfterUpdate(abs, commitSHA string) {
	r := openGlobalRegistry()
	if r == nil {
		return
	}
	defer r.Close()
	head := commitSHA
	if head == "" {
		head = gitHead(abs)
	}
	if err := r.RefreshRepo(abs, head, head); err != nil {
		fmt.Fprintf(os.Stderr, "warning: 全局注册刷新失败: %v\n", err)
	}
}

// unregisterRepoAfterClean clean 注销（含级联 worktree 条目）。
func unregisterRepoAfterClean(abs string) {
	r := openGlobalRegistry()
	if r == nil {
		return
	}
	defer r.Close()
	if err := r.UnregisterRepo(abs); err != nil {
		fmt.Fprintf(os.Stderr, "warning: 全局注册注销失败: %v\n", err)
	}
}

// resolveRepoRef --repo 参数解析（Q238，design-q238.md §3.3）：
//  1. 文件系统存在（现行语义，路径优先）→ 原样返回
//  2. 注册表绝对路径后缀匹配（唯一）→ 返回匹配路径
//  3. 注册表目录名精确匹配（唯一）→ 返回
//  4. 注册表 module 名精确匹配（唯一）→ 返回
// 多命中 → stderr 打印候选列表、返回空（调用方报错）；
// 未命中/注册表不可用 → 原样返回（调用方报原路径错误）。
func resolveRepoRef(arg string) string {
	if arg == "" {
		return arg
	}
	if _, err := os.Stat(arg); err == nil {
		return arg
	}
	r := openGlobalRegistry()
	if r == nil {
		return arg
	}
	defer r.Close()
	repos, err := r.ListRepos()
	if err != nil {
		return arg
	}
	var hits []string
	seen := map[string]bool{}
	add := func(p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			hits = append(hits, p)
		}
	}
	for _, rp := range repos {
		if strings.HasSuffix(rp.Path, arg) && strings.HasPrefix(arg, "/") {
			add(rp.Path) // 绝对路径后缀（arg 以 / 开头才后缀匹配，避免 "o2o" 误配）
		}
		if filepath.Base(rp.Path) == arg {
			add(rp.Path) // 目录名精确
		}
		if rp.Module == arg {
			add(rp.Path) // module 名精确
		}
	}
	switch len(hits) {
	case 0:
		return arg
	case 1:
		return hits[0]
	default:
		fmt.Fprintf(os.Stderr, "error: --repo %q 命中多个已注册仓库:\n", arg)
		for _, p := range hits {
			fmt.Fprintf(os.Stderr, "  %s\n", p)
		}
		fmt.Fprintln(os.Stderr, "请使用完整路径")
		return ""
	}
}

// printRepoHint 缺省 cwd 非仓库场景的引导（Q13）：注册表非空时提示
// list 与 --repo 短名用法；空注册表静默。
func printRepoHint() {
	r := openGlobalRegistry()
	if r == nil {
		return
	}
	defer r.Close()
	n, err := r.CountRepos()
	if err != nil || n == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "已注册 %d 个仓库（codeintel list 查看；或 --repo <短名> 指定）\n", n)
}
