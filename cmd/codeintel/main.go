// codeintel 是 Codebase Intelligence 系统的 CLI 入口（TD.md 第 6 章）。
// 初始化日志与 OpenTelemetry 追踪，创建 root span，将带链路的 ctx 贯穿 CLI。
package main

import (
	"context"
	"os"
	"strings"

	"go.opentelemetry.io/otel"
	"go.uber.org/zap"

	"github.com/schaepher/codeintel/internal/cli"
	"github.com/schaepher/codeintel/internal/logging"
)

func main() {
	// 全局标志：任意位置出现 --verbose / --debug 时输出 Debug 级日志
	// （默认 Info 级）——识别后从参数中移除，避免子命令 flag 解析报错
	verbose := false
	filtered := make([]string, 0, len(os.Args)-1)
	for _, a := range os.Args[1:] {
		if a == "--verbose" || a == "--debug" {
			verbose = true
			continue
		}
		filtered = append(filtered, a)
	}
	// 日志目录：粗解析 --repo（Q88 日志与 db 同目录）——root span 与
	// 早期日志从创建起即写 .codeintel/codeintel.log，stdout 只留查询结果
	repoDir := extractRepoDir(filtered)
	tp, err := logging.Setup("codeintel", verbose, repoDir)
	if err != nil {
		// 追踪初始化失败不阻塞主流程：全局 provider 保持 noop，日志照常
		zap.L().Warn("tracing setup failed, tracing disabled", zap.Error(err))
	}

	logger := zap.L()
	logger.Debug("enter main")
	defer logger.Debug("exit main")

	ctx, span := otel.Tracer("codeintel").Start(context.Background(), "codeintel.main")
	defer span.End()

	// 注意：os.Exit 不执行 defer，span 与 tp 必须在退出前显式结束/冲刷
	code := cli.Main(ctx, filtered)
	span.End()
	if tp != nil {
		_ = tp.Shutdown(context.Background())
	}
	os.Exit(code)
}

// extractRepoDir 从命令行粗解析 --repo 目录（`--repo X` / `--repo=X`），
// 未指定返回空串（日志保持 stdout）。
func extractRepoDir(args []string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--repo" && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(a, "--repo=") {
			return strings.TrimPrefix(a, "--repo=")
		}
	}
	return ""
}
