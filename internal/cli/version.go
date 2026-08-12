package cli

import (
	"fmt"
	"runtime/debug"
)

// cmdVersion 实现 `codeintel version`：输出编译时的 commit hash。
// go build 默认（-buildvcs=true）把 VCS 信息嵌入二进制，无需 ldflags。
func cmdVersion(args []string) int {
	fmt.Printf("codeintel %s\n", gitCommit())
	return 0
}

// gitCommit 从构建信息读取 vcs.revision。
func gitCommit() string {
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
