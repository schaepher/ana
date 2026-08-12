// codeintel 是 Codebase Intelligence 系统的 CLI 入口（TD.md 第 6 章）。
package main

import (
	"os"

	"github.com/schaepher/codeintel/internal/cli"
	"go.uber.org/zap"
)

func main() {
	logger := zap.L()
	logger.Debug("enter main")
	defer logger.Debug("exit main")
	os.Exit(cli.Main(os.Args[1:]))
}
