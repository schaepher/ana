// Package cli 实现 codeintel 命令行：init（全量构建）/ query（查询）/ clean（清理）。
package cli

import (
	"context"
	"fmt"
	"go.uber.org/zap"
	"os"
)

// Main 是 CLI 入口（cmd/codeintel 调用）。ctx 携带 root span（链路追踪）。
func Main(ctx context.Context, args []string) int {
	logger := zap.L()
	logger.Debug("enter Main")
	defer logger.Debug("exit Main")
	if len(args) < 1 {
		usage()
		return 2
	}
	switch args[0] {
	case "init":
		return cmdInit(ctx, args[1:])
	case "update":
		return cmdUpdate(ctx, args[1:])
	case "reindex":
		return cmdReindex(ctx, args[1:])
	case "query":
		return cmdQuery(args[1:])
	case "export":
		return cmdExport(args[1:])
	case "serve":
		return cmdServe(ctx, args[1:])
	case "clean":
		return cmdClean(args[1:])
	case "version", "--version", "-v":
		return cmdVersion(args[1:])
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
  codeintel update --repo <path>   增量更新（git 检测变更文件，全量分析+增量写入）
  codeintel serve --repo <path>    启动图探索 Web 服务（AntV G6 前端，--addr 默认 :8090）
  codeintel query <symbol|name>    查询符号详情（含调用者/被调用者）
  codeintel query callers <sym>    查询调用者（--depth N，默认 1，置信度阈值 0.8）
  codeintel query callees <sym>    查询被调用者（--depth N，默认 1）
  codeintel query impact <sym>     影响分析（--depth N，默认 3）
  codeintel query fields <func>    字段读写摘要（direct_read/write + indirect_write）
  codeintel query trace-backward <field> --func <func>
                                  字段产生点反向追溯（--max-depth N，默认 8）
  codeintel query trace-forward <field> --func <func>
                                  字段后续使用正向追踪
  codeintel export [--out json]   导出双层索引 JSON（字段 → 产生者/消费者）
  codeintel clean --repo <path>    删除仓库的索引数据库
  codeintel version                输出编译时的 commit hash

符号可用 canonical ID（symbol:go:<pkg>:<name>）或名称精确/模糊查找。

任意位置加 --verbose（或 --debug）输出 debug 级日志（默认 info 级）。
`)
}
