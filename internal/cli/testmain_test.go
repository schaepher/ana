package cli

import (
	"os"
	"testing"
)

// Q238：cli 测试统一把全局注册表目录指向临时目录——cmdInit/cmdUpdate/
// cmdClean 的注册钩子不会污染真实 ~/.codeintel。
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "codeintel-registry-test-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	old := registryDirFn
	registryDirFn = func() string { return dir }
	code := m.Run()
	registryDirFn = old
	os.Exit(code)
}
