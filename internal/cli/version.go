package cli

import (
	"fmt"
	"runtime/debug"
)

// gitCommit 由构建参数注入（见 Makefile）：
//
//	go build -ldflags "-X github.com/schaepher/codeintel/internal/cli.gitCommit=<hash>"
//
// 未注入时回退到 debug.ReadBuildInfo 的 vcs.revision（go build 默认嵌入）。
var gitCommit = "unknown"

// cmdVersion 实现 `codeintel version`：输出编译时的 commit hash。
func cmdVersion(args []string) int {
	fmt.Printf("codeintel %s\n", version())
	return 0
}

func version() string {
	if gitCommit != "unknown" && gitCommit != "" {
		return gitCommit
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	for _, s := range bi.Settings {
		if s.Key == "vcs.revision" {
			return s.Value
		}
	}
	return "unknown"
}
