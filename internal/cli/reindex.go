package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

// cmdReindex 实现 `codeintel reindex --repo <path>`：一步重建索引——
// 删除旧库（绕过 schema 版本检查，clean 语义）后走完整 init 流程
// （全量构建 + VACUUM）。
func cmdReindex(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("reindex", flag.ExitOnError)
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
	if err := deleteIndexDB(abs); err != nil {
		fmt.Fprintf(os.Stderr, "error: 删除旧索引: %v\n", err)
		return 1
	}
	fmt.Printf("重建索引: %s\n", abs)
	return cmdInit(ctx, args)
}

// deleteIndexDB 删除索引数据库文件（.codeintel/codeintel.db*），
// 保留日志与 scip 中间文件；无库时静默成功。
func deleteIndexDB(repoPath string) error {
	dir := filepath.Join(repoPath, ".codeintel")
	for _, f := range []string{"codeintel.db", "codeintel.db-wal", "codeintel.db-shm"} {
		if err := os.Remove(filepath.Join(dir, f)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
