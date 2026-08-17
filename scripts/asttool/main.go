// asttool：go/ast 文件操作工具链（基础功能，供大文件拆分/测试迁移复用）。
//
// 子命令：
//
//	asttool analyze <file...>                      列出顶层声明清单
//	asttool split <src.go> <out1.go:name1,name2> ... 按声明分组拆分文件
//	asttool migrate [--pkg <name>] <out_prefix> <src...> 测试迁移（fixture/查询/断言变换）
//
// 用途示例：
//
//	# 查看 repo.go 的声明（拆分前规划分组）
//	asttool analyze internal/infrastructure/sqlite/repo.go
//
//	# 按主题拆文件：SaveBatch/SaveBatchStats 移到 repo_save.go
//	asttool split repo.go repo_save.go:SaveBatch,SaveBatchStats repo_symbol.go:GetSymbol
//
//	# 迁移 integration SelfContained 测试到单元测试（见 migrate.go 头注释）
//	asttool migrate --pkg ssa internal/infrastructure/ssa/migrated_test integration/xxx_test.go
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "analyze":
		analyzeMain(os.Args[2:])
	case "split":
		splitMain(os.Args[2:])
	case "migrate":
		migrateMain(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  asttool analyze <file...>
  asttool split <src.go> <out1.go:name1,name2> [<out2.go:...>...]
  asttool migrate [--pkg <name>] <out_prefix> <src_test_files...>
see file headers for details`)
}
