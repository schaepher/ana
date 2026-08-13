// codeintel 是 Codebase Intelligence 系统的 CLI 入口（TD.md 第 6 章）。
// 初始化日志与 OpenTelemetry 追踪，创建 root span，将带链路的 ctx 贯穿 CLI。
package main

import (
	"context"
	"os"

	"go.opentelemetry.io/otel"
	"go.uber.org/zap"

	"github.com/schaepher/codeintel/internal/cli"
	"github.com/schaepher/codeintel/internal/logging"
)

func main() {
	// 全局标志：任意位置出现 --verbose / --debug 时输出 Debug 级日志（默认 Info 级）
	verbose := false
	for _, a := range os.Args[1:] {
		if a == "--verbose" || a == "--debug" {
			verbose = true
			break
		}
	}
	tp, err := logging.Setup("codeintel", verbose)
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
	code := cli.Main(ctx, os.Args[1:])
	span.End()
	if tp != nil {
		_ = tp.Shutdown(context.Background())
	}
	os.Exit(code)
}
