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
	// 建索引目录（db + 包级分析缓存）后 force 删除：db 删、cache 保留
	target := filepath.Join(dir, ".codeintel")
	if err := os.MkdirAll(filepath.Join(target, "cache"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "codeintel.db"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cacheFile := filepath.Join(target, "cache", "abc.json")
	if err := os.WriteFile(cacheFile, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := cmdClean([]string{"--repo", dir, "--force"}); code != 0 {
		t.Errorf("clean = %d, want 0", code)
	}
	if _, err := os.Stat(filepath.Join(target, "codeintel.db")); !os.IsNotExist(err) {
		t.Error("codeintel.db should be removed")
	}
	if _, err := os.Stat(cacheFile); err != nil {
		t.Error("包级分析缓存应保留（pkg hash 自校验，删除纯浪费）")
	}
}

// TestCleanPurgeCache：clean --purge-cache 连缓存一起删除（磁盘清理场景）。
func TestCleanPurgeCache(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, ".codeintel")
	if err := os.MkdirAll(filepath.Join(target, "cache"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "codeintel.db"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cacheFile := filepath.Join(target, "cache", "abc.json")
	if err := os.WriteFile(cacheFile, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := cmdClean([]string{"--repo", dir, "--force", "--purge-cache"}); code != 0 {
		t.Errorf("clean --purge-cache = %d, want 0", code)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Error(".codeintel should be fully removed with --purge-cache")
	}
}

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
