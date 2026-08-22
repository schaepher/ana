package cli

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
)

// TestQuerySummary：跨层摘要（Q100）——主链提取 + 步骤类型标注。
func TestQuerySummary(t *testing.T) {
	dir := seedFieldTrace(t)
	writeNode := "symbol:go:example.com/m:main#t.A.write@5"
	out := captureStdout(func() {
		if code := cmdQuery([]string{"summary", writeNode, "--repo", dir}); code != 0 {
			t.Errorf("query summary exit = %d", code)
		}
	})
	for _, want := range []string{"生命周期", "t.A", "[write]"} {
		if !strings.Contains(out, want) {
			t.Errorf("query summary 输出缺 %q:\n%s", want, out)
		}
	}

	out = captureStdout(func() {
		if code := cmdQuery([]string{"summary", writeNode, "--repo", dir, "--json"}); code != 0 {
			t.Errorf("query summary --json exit = %d", code)
		}
	})
	if !strings.Contains(out, `"steps"`) {
		t.Errorf("summary --json 应含 steps:\n%s", out)
	}

	out = captureStdout(func() {
		if code := cmdQuery([]string{"summary", writeNode, "--repo", dir, "--format", "mermaid"}); code != 0 {
			t.Errorf("query summary mermaid exit = %d", code)
		}
	})
	if !strings.Contains(out, "flowchart") {
		t.Errorf("summary mermaid 应输出 flowchart:\n%s", out)
	}
}

// TestQuerySummaryFieldPath：③ 回归——类型限定字段路径（非符号）作为
// 锚点输入可解析（此前被识别为"不存在的符号"）。
func TestQuerySummaryFieldPath(t *testing.T) {
	dir := seedFieldTrace(t)
	out := captureStdout(func() {
		if code := cmdQuery([]string{"summary", "example.com/m.T.A", "--repo", dir}); code != 0 {
			t.Errorf("query summary 字段路径 exit = %d", code)
		}
	})
	if !strings.Contains(out, "生命周期") {
		t.Errorf("字段路径摘要应输出生命周期链:\n%s", out)
	}

	if code := cmdQuery([]string{"summary", "example.com/m.Nope.X", "--repo", dir}); code == 0 {
		t.Error("未知字段路径应失败")
	}
}

// TestVersionNoOTLNoise：④ 回归——version 命令 stdout 不含 OTel JSON。
func TestVersionNoOTLNoise(t *testing.T) {
	out := captureStdout(func() {
		if code := Main(context.Background(), []string{"version"}); code != 0 {
			t.Errorf("version exit = %d", code)
		}
	})
	if strings.Contains(out, `"Name": "codeintel.main"`) || strings.Contains(out, "SpanContext") {
		t.Errorf("version stdout 不应含 OTel span JSON:\n%s", out[:min(len(out), 200)])
	}
}

// TestQueryTable：query table——表级聚合：列虚拟节点 + 写入方（summary_io 入边）。
func TestQueryTable(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "go.mod"), "module example.com/m\n\ngo 1.21\n")
	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	r := sqlite.NewRepo(db)
	funcID := "symbol:go:example.com/m:save"
	vn := func(name string, line int) *domain.CodeEntity {
		return &domain.CodeEntity{
			ID:   domain.CanonicalID(funcID + "#ext.sql." + name + ".write@" + strconv.Itoa(line)),
			Kind: domain.KindFieldAccess, Name: name, FilePath: "a.go", LineStart: line,
			Properties: map[string]any{"full_path": name, "access_kind": "write",
				"type_string": "sql", "is_external": "true", "func_id": funcID},
		}
	}
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(funcID), Kind: domain.KindFunction, Name: "save", FilePath: "a.go"},
		{ID: domain.CanonicalID(funcID + "#t0"), Kind: domain.KindSSAValue, Name: "t0"},
		vn("users.name", 5),
		vn("users.age", 6),
	}
	if _, err := r.SaveBatchStats(nodes, []*domain.Fact{
		{SourceID: domain.CanonicalID(funcID + "#t0"), TargetID: domain.CanonicalID(funcID + "#ext.sql.users.name.write@5"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1,
			Metadata: map[string]any{"line_num": 5}},
	}, nil); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(func() {
		if code := cmdQuery([]string{"table", "users", "--repo", dir}); code != 0 {
			t.Errorf("query table exit = %d", code)
		}
	})
	if !strings.Contains(out, "users.name") || !strings.Contains(out, "users.age") {
		t.Errorf("table 输出缺列: %s", out)
	}
	if !strings.Contains(out, "save") || !strings.Contains(out, ":5") {
		t.Errorf("table 输出缺写入方: %s", out)
	}

	if code := cmdQuery([]string{"table", "nope", "--repo", dir}); code != 0 {
		t.Errorf("empty table exit = %d", code)
	}
}

// TestQueryGraphOutputs：⑬ 猎 bug——impact/callees 文本输出与 JSON 输出
// 的 nodeBriefs/printNodes/printFacts 格式路径。
func TestQueryGraphOutputs(t *testing.T) {
	dir := seedRepo(t)

	out := captureStdout(func() {
		if code := cmdQuery([]string{"impact", "main", "--repo", dir}); code != 0 {
			t.Errorf("impact exit = %d", code)
		}
	})
	for _, want := range []string{"影响范围", "function", "main"} {
		if !strings.Contains(out, want) {
			t.Errorf("impact 输出缺 %q:\n%s", want, out)
		}
	}

	out = captureStdout(func() {
		if code := cmdQuery([]string{"impact", "main", "--repo", dir, "--json"}); code != 0 {
			t.Errorf("impact --json exit = %d", code)
		}
	})
	if !strings.Contains(out, `"nodes"`) {
		t.Errorf("impact --json 缺 nodes:\n%s", out)
	}

	out = captureStdout(func() {
		if code := cmdQuery([]string{"callees", "main", "--repo", dir, "--json"}); code != 0 {
			t.Errorf("callees --json exit = %d", code)
		}
	})
	if !strings.Contains(out, `"rows"`) || !strings.Contains(out, `symbol:go:example.com/m/svc:(Svc).Run`) {
		t.Errorf("callees --json 缺 rows:\n%s", out)
	}
}

// TestExportGraphValueTraceDot：⑬ 猎 bug——export graph value-trace 的
// DOT 渲染路径（renderValueTraceDot，此前 0% 覆盖）。
func TestExportGraphValueTraceDot(t *testing.T) {
	dir := seedFieldTrace(t)
	writeNode := "symbol:go:example.com/m:main#t.A.write@5"
	out := captureStdout(func() {
		if code := cmdExportGraph([]string{"--type", "value-trace", "--target", writeNode,
			"--format", "dot", "--repo", dir}); code != 0 {
			t.Errorf("export graph dot exit = %d", code)
		}
	})
	for _, want := range []string{"digraph", "->", "t.A"} {
		if !strings.Contains(out, want) {
			t.Errorf("dot 输出缺 %q:\n%s", want, out)
		}
	}
}

// TestUpdateNoGitRepo：⑬ 猎 bug——update 在非 git 仓库（无 .git）应
// 报错而非 panic/静默成功（变更检测依赖 git）。
func TestUpdateNoGitRepo(t *testing.T) {
	dir := t.TempDir()

	if code := cmdUpdate(context.Background(), []string{"--repo", dir}); code == 0 {
		t.Error("update 非 git 仓库应失败")
	}
}

// TestInitNoGoMod：⑬ 猎 bug——init 在无 go.mod 目录（ensureGoEnv 路径）
// 应报错而非 panic。
func TestInitNoGoMod(t *testing.T) {
	dir := t.TempDir()
	if code := cmdInit(context.Background(), []string{"--repo", dir}); code == 0 {
		t.Error("init 无 go.mod 应失败")
	}
}

// TestCmdInitNoRepoDefaultsToCwd：Q237——init 缺 --repo 默认当前工作目录
// （chdir 到有 go.mod 的 fixture 后缺省跑，应命中 fixture 而非报错）。
func TestCmdInitNoRepoDefaultsToCwd(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "go.mod"), "module example.com/m\n\ngo 1.21\n")
	writeTestFile(t, filepath.Join(dir, "main.go"), "package main\n\nfunc main() {}\n")
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(func() {
		if code := cmdInit(context.Background(), []string{}); code == 2 {
			t.Errorf("init 缺 --repo 不应再报 --repo is required（exit 2）")
		}
	})
	if !strings.Contains(out, "构建索引: "+dir) {
		t.Errorf("init 缺 --repo 应默认当前目录（输出 %q，期望含 %q）", out, "构建索引: "+dir)
	}
}

// TestDefaultBuildWorkers Q221：默认并发 = min(NumCPU, 8)（1..8 区间）。
func TestDefaultBuildWorkers(t *testing.T) {
	n := defaultBuildWorkers()
	if n < 1 || n > 8 {
		t.Fatalf("defaultBuildWorkers = %d, want 1..8", n)
	}
}
