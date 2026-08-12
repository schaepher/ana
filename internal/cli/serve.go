package cli

import (
	"errors"
	"flag"
	"fmt"
	iofs "io/fs"
	"net/http"
	"os"
	"time"

	"github.com/schaepher/codeintel/assets"
	"github.com/schaepher/codeintel/internal/domain"
	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
	"github.com/schaepher/codeintel/internal/server"
)

// cmdServe 实现 `codeintel serve --repo <path> [--addr :8090]`：
// 提供图探索 HTTP 接口与前端页面（TD.md 2.3 中 serve 守护进程的 MVP 形态）。
func cmdServe(args []string) int {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	repoPath := fs.String("repo", ".", "仓库根目录（须已运行 codeintel init）")
	addr := fs.String("addr", ":8090", "HTTP 监听地址")
	fs.Parse(args)

	abs, _, err := resolveRepo(*repoPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	db, err := sqlite.Open(abs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()

	// 校验已构建（nodes 非空），否则提示先 init
	repo := sqlite.NewRepo(db)
	if _, _, err := repo.Counts(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	_, err = repo.GetLatest()
	if errors.Is(err, domain.ErrNotFound) {
		fmt.Fprintf(os.Stderr, "error: %s 尚未构建索引，请先运行: codeintel init --repo %s\n", abs, abs)
		return 1
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	// go:embed 的 web/ 前缀剥离：embed.FS 中路径为 "web/..."
	webFS, err := iofs.Sub(assets.WebFS, "web")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: embed web assets: %v\n", err)
		return 1
	}

	srv := &http.Server{
		Addr:              *addr,
		Handler:           server.New(repo, webFS).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	fmt.Printf("codeintel serve 已启动: http://localhost%s  （仓库: %s）\n", *addr, abs)
	fmt.Println("提示: 浏览器打开后展示顶层入口（main / HTTP / gRPC 服务），点击节点展开依赖。Ctrl+C 退出。")
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintf(os.Stderr, "serve error: %v\n", err)
		return 1
	}
	return 0
}
