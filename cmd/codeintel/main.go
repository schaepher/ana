// codeintel 是 Codebase Intelligence 系统的 CLI 入口（TD.md 第 6 章）。
package main

import (
	"os"

	"github.com/schaepher/codeintel/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:]))
}
