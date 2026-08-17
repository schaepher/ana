// entrylog 为 Go 项目所有顶层函数/方法注入 enter/exit 调试日志（zap，debug 级）。
//
// 对每个函数在函数体开头注入：
//
//	无 ctx 参数：logger := zap.L()
//	有 ctx 参数：logger := logging.FromContext(ctx)   // 从 ctx 取 logger，缺失回退全局
//	logger.Debug("enter <name>")
//	defer logger.Debug("exit <name>")
//
// 用法：
//
//	go run ./scripts/entrylog -dir <项目根目录>
//
// 实现：AST 只读分析定位插入点，正文用文本插入（保留原文件格式与注释，
// 避免 format.Node 对游离注释的重排）。import 缺失时同样文本补入。
// 幂等：已注入的函数（首语句 logger 赋值 + enter Debug 调用）跳过，可重复运行。
// 安全：函数体内已有 logger 标识符（参数/局部变量）时跳过注入，避免遮蔽。
// 排除：_test.go、scripts/、internal/logging（helper 自身注入会无限递归）。
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// 注入代码用到的包
const (
	zapImport     = "go.uber.org/zap"
	loggingImport = "github.com/schaepher/codeintel/internal/logging"
)

var skipDirs = map[string]bool{
	"scripts":          true,
	"internal/logging": true, // FromContext 注入自身会无限递归
	".git":             true,
	".codeintel":       true,
	"vendor":           true,
}

func main() {
	root := flag.String("dir", ".", "要处理的 Go 模块根目录")
	flag.Parse()

	var processed, injected, skipped int
	err := filepath.WalkDir(*root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			rel, rerr := filepath.Rel(*root, path)
			if rerr == nil && skipDirs[rel] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		n, s, err := processFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  [error] %s: %v\n", path, err)
			return nil
		}
		processed++
		injected += n
		skipped += s
		return nil
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "walk: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("处理完成: 文件 %d，注入函数 %d，跳过（已有 logger 标识符/已注入）%d\n",
		processed, injected, skipped)
}

// insert 是一次文本插入（offset 为字节偏移）。
type insert struct {
	offset int
	text   string
}

// processFile 分析并注入单个 Go 文件；返回 (注入数, 跳过数, 错误)。

// applyInserts 按 offset 降序应用插入（避免偏移失效）。

// importInsertOffset 返回 import 补入位置，并报告 import 是否为括号块：
//   - 括号块：最后一个 spec 之后
//   - 单行 import：该行末尾
//   - 无 import：package 声明之后

// importText 生成 import 补入文本（配合 importInsertOffset 使用）。

// bodyText 生成注入到函数体开头的语句文本。
// singleLine 时以 \t 结尾（配合 Rbrace 前补 \n，将原单行内容拆为独立行）。

// alreadyInjected 检测函数体首语句是否已是注入模式（logger := … + enter Debug）。

// hasLoggerIdent 检查函数签名与函数体内是否已有 "logger" 标识符。

// findCtxParam 返回第一个类型为 context.Context 的参数名；无则返回空串。

// isContextType 启发式判断类型是否为 context.Context（SelectorExpr）。

// funcName 生成日志用的函数名：方法带接收者类型，如 (Service).CreatePayment。
