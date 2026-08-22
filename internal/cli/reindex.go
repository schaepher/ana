package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

// cmdReindex 实现 `codeintel reindex --repo <path>`：一步重建索引——
// 与 init 相同走全量构建（FullBuild 清空图数据表 DROP+CREATE 重建，
// 保留 build_metadata 与配置表），语义明确为"重建"。
func cmdReindex(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("reindex", flag.ExitOnError)
	// Q237：--repo 缺省当前工作目录
	repoPath := fs.String("repo", ".", "仓库根目录（含 go.mod；默认当前目录）")
	fs.Int("workers", defaultBuildWorkers(), "SSA 分析按包并发数（Q221：默认 min(NumCPU, 8)；透传给 init）")
	fs.Parse(args)
	*repoPath = resolveRepoRef(*repoPath) // Q238：注册表短名/后缀/module

	abs, err := filepath.Abs(*repoPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: resolve repo path: %v\n", err)
		return 1
	}
	fmt.Printf("重建索引: %s\n", abs)
	return cmdInit(ctx, args)
}
