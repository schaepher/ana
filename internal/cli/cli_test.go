package cli

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
)

// captureStdout 捕获 stdout 输出（CLI 结果断言用）。

func TestMainDispatch(t *testing.T) {
	ctx := context.Background()
	if code := Main(ctx, []string{}); code != 2 {
		t.Errorf("no args = %d, want 2", code)
	}
	if code := Main(ctx, []string{"bogus"}); code != 2 {
		t.Errorf("unknown cmd = %d, want 2", code)
	}
	if code := Main(ctx, []string{"help"}); code != 0 {
		t.Errorf("help = %d, want 0", code)
	}
	if code := Main(ctx, []string{"version"}); code != 0 {
		t.Errorf("version = %d, want 0", code)
	}
}

func TestClean(t *testing.T) {
	dir := t.TempDir()
	// 无索引目录：直接返回 0
	if code := cmdClean([]string{"--repo", dir, "--force"}); code != 0 {
		t.Errorf("clean without index = %d, want 0", code)
	}
	// 建索引目录后 force 删除
	target := filepath.Join(dir, ".codeintel")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "codeintel.db"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := cmdClean([]string{"--repo", dir, "--force"}); code != 0 {
		t.Errorf("clean = %d, want 0", code)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Error(".codeintel should be removed")
	}
}

// seedRepo 建临时仓库 + 预填一个小图（query 的 resolveRepo 要求 go.mod）。

// seedFieldTrace 预填字段追溯数据：函数节点 + 摘要行 + field_access/ssa_value 图。

// TestValueTracePersist：value-trace 经过 SQL 持久化虚拟节点
// （Q97：字段 → 表.列 映射可见）。
func TestValueTracePersist(t *testing.T) {
	dir := seedFieldTrace(t)
	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	r := sqlite.NewRepo(db)
	funcID := "symbol:go:example.com/m:main"
	val := &domain.CodeEntity{ID: domain.CanonicalID(funcID + "#u"), Kind: domain.KindSSAValue,
		Name: "u", Properties: map[string]any{"func_id": funcID}}
	vnode := &domain.CodeEntity{ID: domain.CanonicalID(funcID + "#ext.sql.users.name.write@9"),
		Kind: domain.KindFieldAccess, Name: "users.name", FilePath: "main.go", LineStart: 9,
		Properties: map[string]any{"func_id": funcID, "instance_path": "users.name",
			"access_kind": "write", "full_path": "example.com/m.User.Name"}}
	r.SaveBatchStats([]*domain.CodeEntity{val, vnode}, []*domain.Fact{
		{SourceID: val.ID, TargetID: vnode.ID, Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
	}, nil)
	out := captureStdout(func() {
		if code := cmdQuery([]string{"value-trace", string(val.ID), "--repo", dir}); code != 0 {
			t.Errorf("value-trace exit = %d", code)
		}
	})
	if !strings.Contains(out, "users.name") {
		t.Errorf("value-trace 应显示持久化节点 users.name:\n%s", out)
	}
}

// TestQuerySymbolCandidates：接口类型 symbol 详情展示候选实现
// （Q95：candidates + 置信度 + 注册点）。

// TestQueryFieldsCallSite：indirect_write 摘要展示调用点（Q90 调用点级回连）：
// INDIRECT_WRITE 边 metadata 的调用点行号与实参变量名出现在 fields 输出。

func TestQueryTraceBackward(t *testing.T) {
	dir := seedFieldTrace(t)
	out := captureStdout(func() {
		if code := cmdQuery([]string{"trace-backward", "example.com/m.T.A",
			"--func", "main", "--repo", dir}); code != 0 {
			t.Errorf("trace-backward exit = %d", code)
		}
	})
	for _, want := range []string{"产生点", "data_flows_to", "t.A"} {
		if !strings.Contains(out, want) {
			t.Errorf("trace-backward output missing %q:\n%s", want, out)
		}
	}
}

func TestQueryTraceForward(t *testing.T) {
	dir := seedFieldTrace(t)
	out := captureStdout(func() {
		if code := cmdQuery([]string{"trace-forward", "example.com/m.T.A",
			"--func", "main", "--repo", dir}); code != 0 {
			t.Errorf("trace-forward exit = %d", code)
		}
	})
	// 读节点 → result：正向能走到 t1
	if !strings.Contains(out, "t1") {
		t.Errorf("trace-forward output missing result value:\n%s", out)
	}
}

func TestExport(t *testing.T) {
	dir := seedFieldTrace(t)
	outPath := filepath.Join(t.TempDir(), "analysis.json")
	if code := cmdExport([]string{"--repo", dir, "--out", outPath}); code != 0 {
		t.Fatalf("export exit = %d", code)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("export JSON: %v", err)
	}
	fields, ok := got["fields"].(map[string]any)
	if !ok {
		t.Fatalf("export missing fields: %s", data)
	}
	if _, ok := fields["example.com/m.T.A"]; !ok {
		t.Errorf("export missing field path: %s", data)
	}
}

func TestInitGoWorkReject(t *testing.T) {
	// go.work 在目标目录且无 go.mod → init 报错提示进入模块目录
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.work"), []byte("go 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	code := cmdInit(context.Background(), []string{"--repo", dir})
	w.Close()
	os.Stderr = old
	var buf strings.Builder
	io.Copy(&buf, r)
	if code != 1 {
		t.Errorf("init with go.work = %d, want 1", code)
	}
	if !strings.Contains(buf.String(), "go.work") {
		t.Errorf("stderr = %q, want go.work 提示", buf.String())
	}
	// 有 go.mod 的模块目录（即使上层有 go.work）不报错
	sub := filepath.Join(dir, "m")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "go.mod"), []byte("module example.com/m\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := cmdInit(context.Background(), []string{"--repo", sub}); code != 0 {
		t.Errorf("init module dir under go.work = %d, want 0", code)
	}
}

// TestQuerySummary：跨层摘要（Q100）——主链提取 + 步骤类型标注。

// TestQuerySummaryFieldPath：③ 回归——类型限定字段路径（非符号）作为
// 锚点输入可解析（此前被识别为"不存在的符号"）。

// TestVersionNoOTLNoise：④ 回归——version 命令 stdout 不含 OTel JSON。

// TestQueryTable：query table——表级聚合：列虚拟节点 + 写入方（summary_io 入边）。

// TestQueryGraphOutputs：⑬ 猎 bug——impact/callees 文本输出与 JSON 输出
// 的 nodeBriefs/printNodes/printFacts 格式路径。

// TestExportGraphValueTraceDot：⑬ 猎 bug——export graph value-trace 的
// DOT 渲染路径（renderValueTraceDot，此前 0% 覆盖）。

// TestUpdateNoGitRepo：⑬ 猎 bug——update 在非 git 仓库（无 .git）应
// 报错而非 panic/静默成功（变更检测依赖 git）。

// TestInitNoGoMod：⑬ 猎 bug——init 在无 go.mod 目录（ensureGoEnv 路径）
// 应报错而非 panic。
