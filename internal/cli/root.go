// Package cli 实现 codeintel 命令行：init（全量构建）/ query（查询）/ clean（清理）。
package cli

import (
	"fmt"
	"go.uber.org/zap"
	"os"
)

// Main 是 CLI 入口（cmd/codeintel 调用）。
func Main(args []string) int {
	logger := zap.L()
	logger.Debug("enter Main")
	defer logger.Debug("exit Main")
	if len(args) < 1 {
		usage()
		return 2
	}
	switch args[0] {
	case "init":
		return cmdInit(args[1:])
	case "query":
		return cmdQuery(args[1:])
	case "serve":
		return cmdServe(args[1:])
	case "clean":
		return cmdClean(args[1:])
	case "help", "-h", "--help":
		usage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", args[0])
		usage()
		return 2
	}
}

func usage() {
	logger := zap.L()
	logger.Debug("enter usage")
	defer logger.Debug("exit usage")
	fmt.Fprint(os.Stderr, `codeintel - Go 代码库智能索引与查询（MVP）

用法:
  codeintel init --repo <path>     全量构建索引（生成 .codeintel/codeintel.db）
  codeintel serve --repo <path>    启动图探索 Web 服务（AntV G6 前端，--addr 默认 :8090）
  codeintel query <symbol|name>    查询符号详情（含调用者/被调用者）
  codeintel query callers <sym>    查询调用者（--depth N，默认 1，置信度阈值 0.8）
  codeintel query callees <sym>    查询被调用者（--depth N，默认 1）
  codeintel query impact <sym>     影响分析（--depth N，默认 3）
  codeintel clean --repo <path>    删除仓库的索引数据库

符号可用 canonical ID（symbol:go:<pkg>:<name>）或名称精确/模糊查找。
`)
}
