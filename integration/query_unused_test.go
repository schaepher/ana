//go:build integration

package integration

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
)

// TestQueryUnusedSelfContained：query unused 全量报告（field_trace.md §16）——
// 死代码/孤立链/var 初始化调用/回调注册/main 的判定。
func TestQueryUnusedSelfContained(t *testing.T) {
	if !scipGoAvailable() {
		t.Skip("scip-go not found")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/unused\n\ngo 1.21\n")
	writeFile(t, filepath.Join(dir, "main.go"), `package unused

type T struct{ A int }

// dead：无调用无引用 → 报告
func dead() {}

// chainA → chainB：孤立链
func chainA() { chainB() }
func chainB() {}

// ctor：var 初始化调用（Q108）→ 不算未调用
func ctor() *T { return &T{} }

var G = ctor()

// hook：作为回调注册（passes_to）→ 无调用但有引用
func hook() {}

func use(f func()) { f() }

func main() {
	_ = G
	use(hook)
}
`)
	if code := runCLI(t, "init", "--repo", dir); code != 0 {
		t.Fatalf("init exit = %d", code)
	}
	code, out := runCLIOut(t, "query", "unused", "--repo", dir)
	if code != 0 {
		t.Fatalf("query unused exit = %d", code)
	}

	if !strings.Contains(out, "dead") {
		t.Errorf("dead 应报告为未调用，output=%q", out)
	}

	if !strings.Contains(out, "chainA → chainB") {
		t.Errorf("孤立链 chainA → chainB 缺失，output=%q", out)
	}

	if strings.Contains(out, "ctor") {
		t.Errorf("ctor 不应报告（var G = ctor() 是初始化调用），output=%q", out)
	}

	if strings.Contains(out, "  main ") {
		t.Errorf("main 不应报告，output=%q", out)
	}

	if !strings.Contains(out, "hook") {
		t.Errorf("hook 应报告为无调用（有引用），output=%q", out)
	}
	if strings.Contains(out, "hook →") {
		t.Errorf("hook 有引用不应为孤立链，output=%q", out)
	}

	code, _ = runCLIOut(t, "query", "unused", "--fail-on", "unused", "--repo", dir)
	if code != 1 {
		t.Errorf("--fail-on unused 应退出码 1（存在未调用函数），got %d", code)
	}
}

// TestQueryUnusedSinceSelfContained：--since <ref>——git diff 区间内新增
// 函数标 [new] 并只报告本次改动（field_trace.md §16.5）。
func TestQueryUnusedSinceSelfContained(t *testing.T) {
	if !scipGoAvailable() {
		t.Skip("scip-go not found")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/since\n\ngo 1.21\n")
	writeFile(t, filepath.Join(dir, "main.go"), `package since

func oldUnused() {}

func main() {}
`)

	gitArgs := [][]string{
		{"init", "-q"},
		{"add", "-A"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "base"},
	}
	for _, args := range gitArgs {
		full := append([]string{"-C", dir}, args...)
		if out, err := exec.Command("git", full...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}

	writeFile(t, filepath.Join(dir, "main.go"), `package since

func oldUnused() {}

// 本次需求新增：流程未衔接（main 未调用）
func newFunc() {}

func main() {}
`)
	if code := runCLI(t, "init", "--repo", dir); code != 0 {
		t.Fatalf("init exit = %d", code)
	}
	code, out := runCLIOut(t, "query", "unused", "--since", "HEAD", "--repo", dir)
	if code != 0 {
		t.Fatalf("query unused --since exit = %d", code)
	}

	if !strings.Contains(out, "newFunc") || !strings.Contains(out, "new") {
		t.Errorf("newFunc 应报告且标 [new]，output=%q", out)
	}

	if strings.Contains(out, "oldUnused") {
		t.Errorf("--since 模式不应报告本次未改动的 oldUnused，output=%q", out)
	}

	if strings.Contains(out, "  main ") {
		t.Errorf("main 不应报告，output=%q", out)
	}
}

// TestQueryPathSelfContained：query path 数据流路径断言（field_trace.md
// §17.3）——v0 → 字段写 → 参数 → 字段读 → v1 全链可达；不可达返回无路径。
func TestQueryPathSelfContained(t *testing.T) {
	if !scipGoAvailable() {
		t.Skip("scip-go not found")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/pp\n\ngo 1.21\n")
	writeFile(t, filepath.Join(dir, "main.go"), `package pp

type T struct {
	Key string
}

// 写入方
func fill(t *T) {
	t.Key = "x"
}

// 消费方
func use(t *T) {
	_ = t.Key
}

func main() {
	t := &T{}
	fill(t)
	use(t)
}
`)
	if code := runCLI(t, "init", "--repo", dir); code != 0 {
		t.Fatalf("init exit = %d", code)
	}
	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := sqlite.NewRepo(db)

	readID := fieldAccessID(t, repo, "symbol:go:example.com/pp:use", "t.Key", "read")
	allocID := "symbol:go:example.com/pp:main#t0"
	if readID == "" {
		t.Fatalf("read 锚点缺失")
	}
	code, out := runCLIOut(t, "query", "path", allocID, readID, "--repo", dir)
	if code != 0 {
		t.Fatalf("query path exit = %d", code)
	}
	if !strings.Contains(out, "路径") || !strings.Contains(out, "argument") {
		t.Errorf("path 应输出可达路径（跨函数 argument 链），output=%q", out[:min(len(out), 300)])
	}

	code, out = runCLIOut(t, "query", "path", readID, allocID, "--repo", dir)
	if code != 0 {
		t.Fatalf("query path reverse exit = %d", code)
	}
	if !strings.Contains(out, "无路径") {
		t.Errorf("反向应不可达，output=%q", out[:min(len(out), 300)])
	}

	code, out = runCLIOut(t, "query", "path", allocID, readID, "--repo", dir, "--json")
	if code != 0 || !strings.Contains(out, `"reachable": true`) {
		t.Errorf("--json reachable 缺失，code=%d output=%q", code, out[:min(len(out), 200)])
	}
}

// TestQuerySymbolSinceSelfContained：--since 标注推广（§17.2）——
// symbol/callers 输出对本次新增函数标注 [new]。
func TestQuerySymbolSinceSelfContained(t *testing.T) {
	if !scipGoAvailable() {
		t.Skip("scip-go not found")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/sym\n\ngo 1.21\n")
	writeFile(t, filepath.Join(dir, "main.go"), `package sym

func helper() {}

func main() {
	helper()
}
`)
	for _, args := range [][]string{
		{"init", "-q"},
		{"add", "-A"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "base"},
	} {
		full := append([]string{"-C", dir}, args...)
		if out, err := exec.Command("git", full...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}

	writeFile(t, filepath.Join(dir, "main.go"), `package sym

func helper() {}

// 本次新增
func newHelper() {}

func main() {
	helper()
}
`)
	if code := runCLI(t, "init", "--repo", dir); code != 0 {
		t.Fatalf("init exit = %d", code)
	}

	code, out := runCLIOut(t, "query", "symbol", "symbol:go:example.com/sym:newHelper",
		"--since", "HEAD", "--repo", dir)
	if code != 0 {
		t.Fatalf("symbol exit = %d", code)
	}
	if !strings.Contains(out, "[new]") {
		t.Errorf("symbol --since 应标注 [new]，output=%q", out[:min(len(out), 300)])
	}

	code, out = runCLIOut(t, "query", "fields", "symbol:go:example.com/sym:helper",
		"--since", "HEAD", "--repo", dir)
	if code != 0 {
		t.Fatalf("fields exit = %d", code)
	}
	if strings.Contains(out, "[new]") || strings.Contains(out, "[mod]") {
		t.Errorf("helper 未改动不应标注，output=%q", out[:min(len(out), 200)])
	}
}
