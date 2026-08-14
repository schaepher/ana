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
func captureStdout(f func()) string {
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	f()
	w.Close()
	os.Stdout = old
	var buf strings.Builder
	if _, err := io.Copy(&buf, r); err != nil {
		return ""
	}
	return buf.String()
}

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
func seedRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/m\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	r := sqlite.NewRepo(db)
	nodes := []*domain.CodeEntity{
		{ID: "symbol:go:example.com/m:main", Kind: domain.KindFunction, Name: "main", FilePath: "main.go"},
		{ID: "symbol:go:example.com/m/svc:(Svc).Run", Kind: domain.KindMethod, Name: "(Svc).Run", FilePath: "svc/svc.go"},
	}
	if _, err := r.SaveBatchStats(nodes, nil, nil); err != nil {
		t.Fatalf("save nodes: %v", err)
	}
	if _, err := r.SaveBatchStats(nil, []*domain.Fact{{
		SourceID: "symbol:go:example.com/m:main", TargetID: "symbol:go:example.com/m/svc:(Svc).Run",
		Kind: domain.FactCalls, Confidence: 0.9,
	}}, nil); err != nil {
		t.Fatalf("save edge: %v", err)
	}
	return dir
}

func TestQuerySymbol(t *testing.T) {
	dir := seedRepo(t)
	if code := cmdQuery([]string{"symbol", "main", "--repo", dir}); code != 0 {
		t.Errorf("query symbol main = %d, want 0", code)
	}
	// 未知符号 → 非 0
	if code := cmdQuery([]string{"symbol", "nope_nope", "--repo", dir}); code == 0 {
		t.Error("query unknown symbol should fail")
	}
}

func TestQueryCalleesCallers(t *testing.T) {
	dir := seedRepo(t)
	if code := cmdQuery([]string{"callees", "main", "--repo", dir}); code != 0 {
		t.Errorf("query callees = %d, want 0", code)
	}
	if code := cmdQuery([]string{"callers", "symbol:go:example.com/m/svc:(Svc).Run", "--repo", dir}); code != 0 {
		t.Errorf("query callers = %d, want 0", code)
	}
	// 缺符号参数 → 2
	if code := cmdQuery([]string{"callees"}); code != 2 {
		t.Errorf("callees without symbol = %d, want 2", code)
	}
	// 无子命令 → 2
	if code := cmdQuery([]string{}); code != 2 {
		t.Errorf("query without subcommand = %d, want 2", code)
	}
}

func TestQueryNoRepo(t *testing.T) {
	// 不存在的 repo 目录 → 1
	if code := cmdQuery([]string{"symbol", "main", "--repo", filepath.Join(t.TempDir(), "nope")}); code != 1 {
		t.Errorf("query with bad repo = %d, want 1", code)
	}
}

// seedFieldTrace 预填字段追溯数据：函数节点 + 摘要行 + field_access/ssa_value 图。
func seedFieldTrace(t *testing.T) string {
	t.Helper()
	dir := seedRepo(t)
	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	r := sqlite.NewRepo(db)
	funcID := "symbol:go:example.com/m:main"
	// 摘要行
	r.SaveBatchStats(nil, nil, []*domain.FunctionFieldSummary{
		{FunctionID: domain.CanonicalID(funcID), AccessKind: domain.SummaryDirectWrite,
			FieldPath: "example.com/m.T.A", InstancePath: "t.A", LineStart: 5, CodeSnippet: "t.A = v"},
		{FunctionID: domain.CanonicalID(funcID), AccessKind: domain.SummaryDirectRead,
			FieldPath: "example.com/m.T.A", InstancePath: "t.A", LineStart: 7, CodeSnippet: "return t.A"},
	})
	// 追溯图：value → 写节点（data_flows_to）；读节点 → result
	writeNode := &domain.CodeEntity{ID: domain.CanonicalID(funcID + "#t.A.write@5"),
		Kind: domain.KindFieldAccess, Name: "t.A", FilePath: "main.go", LineStart: 5,
		Properties: map[string]any{"full_path": "example.com/m.T.A", "instance_path": "t.A",
			"access_kind": "write", "func_id": funcID}}
	readNode := &domain.CodeEntity{ID: domain.CanonicalID(funcID + "#t.A.read@7"),
		Kind: domain.KindFieldAccess, Name: "t.A", FilePath: "main.go", LineStart: 7,
		Properties: map[string]any{"full_path": "example.com/m.T.A", "instance_path": "t.A",
			"access_kind": "read", "func_id": funcID}}
	val := &domain.CodeEntity{ID: domain.CanonicalID(funcID + "#t0"), Kind: domain.KindSSAValue,
		Name: "t0", Properties: map[string]any{"func_id": funcID}}
	result := &domain.CodeEntity{ID: domain.CanonicalID(funcID + "#t1"), Kind: domain.KindSSAValue,
		Name: "t1", Properties: map[string]any{"func_id": funcID}}
	r.SaveBatchStats([]*domain.CodeEntity{writeNode, readNode, val, result}, []*domain.Fact{
		{SourceID: val.ID, TargetID: writeNode.ID, Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: readNode.ID, TargetID: result.ID, Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
	}, nil)
	return dir
}

func TestQueryFields(t *testing.T) {
	dir := seedFieldTrace(t)
	out := captureStdout(func() {
		if code := cmdQuery([]string{"fields", "main", "--repo", dir}); code != 0 {
			t.Errorf("query fields exit = %d", code)
		}
	})
	for _, want := range []string{"[direct_read]", "[direct_write]", "example.com/m.T.A", "t.A = v"} {
		if !strings.Contains(out, want) {
			t.Errorf("query fields output missing %q:\n%s", want, out)
		}
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

// TestQuerySymbolCandidates：接口类型 symbol 详情展示候选实现
// （Q95：candidates + 置信度 + 注册点）。
func TestQuerySymbolCandidates(t *testing.T) {
	dir := seedRepo(t)
	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	r := sqlite.NewRepo(db)
	// 接口类型节点 + dispatch_to 边（注册点 0.9；target 须为已存在节点）
	ifaceID := "symbol:go:example.com/m/svc:Handler"
	implID := "symbol:go:example.com/m/svc:(Svc).Run"
	r.SaveBatchStats([]*domain.CodeEntity{
		{ID: domain.CanonicalID(ifaceID), Kind: domain.KindInterface, Name: "Handler", FilePath: "svc/svc.go", LineStart: 3},
	}, []*domain.Fact{{
		SourceID: domain.CanonicalID(ifaceID), TargetID: domain.CanonicalID(implID),
		Kind: domain.FactDispatchTo, ToolSource: domain.ToolSSA, Confidence: 0.9,
		Metadata: map[string]any{"origin": "register", "interface_method": "Handle",
			"register_site": float64(5), "confidence": 0.9},
	}}, nil)

	out := captureStdout(func() {
		if code := cmdQuery([]string{"symbol", ifaceID, "--repo", dir}); code != 0 {
			t.Errorf("query symbol iface exit = %d", code)
		}
	})
	for _, want := range []string{"候选实现", "(Svc).Run", "0.9"} {
		if !strings.Contains(out, want) {
			t.Errorf("symbol 候选实现输出缺 %q:\n%s", want, out)
		}
	}
	// --json：candidates 数组
	out = captureStdout(func() {
		if code := cmdQuery([]string{"symbol", ifaceID, "--repo", dir, "--json"}); code != 0 {
			t.Errorf("query symbol iface --json exit = %d", code)
		}
	})
	if !strings.Contains(out, `"candidates"`) {
		t.Errorf("symbol --json 应含 candidates:\n%s", out)
	}
}

// TestQueryFieldsCallSite：indirect_write 摘要展示调用点（Q90 调用点级回连）：
// INDIRECT_WRITE 边 metadata 的调用点行号与实参变量名出现在 fields 输出。
func TestQueryFieldsCallSite(t *testing.T) {
	dir := seedFieldTrace(t)
	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	r := sqlite.NewRepo(db)
	// 追加间接写摘要行 + INDIRECT_WRITE 边（metadata 携带调用点；
	// target 须为已存在节点，否则 FK 跳过）
	funcID := "symbol:go:example.com/m:main"
	calleeID := "symbol:go:example.com/m/svc:(Svc).Run"
	r.SaveBatchStats(nil, nil, []*domain.FunctionFieldSummary{
		{FunctionID: domain.CanonicalID(funcID), AccessKind: domain.SummaryIndirectWrite,
			FieldPath: "example.com/m.T.A", InstancePath: "t.A", LineStart: 9, CodeSnippet: "t.A = v"},
	})
	if _, err := r.SaveBatchStats(nil, []*domain.Fact{{
		SourceID: domain.CanonicalID(funcID), TargetID: domain.CanonicalID(calleeID),
		Kind: domain.FactIndirectWrite, ToolSource: domain.ToolSSA, Confidence: 1,
		Metadata: map[string]any{"call_line": float64(16), "call_args": "t"},
	}}, nil); err != nil {
		t.Fatalf("save indirect edge: %v", err)
	}
	out := captureStdout(func() {
		if code := cmdQuery([]string{"fields", "main", "--repo", dir}); code != 0 {
			t.Errorf("query fields exit = %d", code)
		}
	})
	for _, want := range []string{"调用点", "16", "t"} {
		if !strings.Contains(out, want) {
			t.Errorf("query fields 输出应含调用点信息 %q:\n%s", want, out)
		}
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
	// --json：steps 数组
	out = captureStdout(func() {
		if code := cmdQuery([]string{"summary", writeNode, "--repo", dir, "--json"}); code != 0 {
			t.Errorf("query summary --json exit = %d", code)
		}
	})
	if !strings.Contains(out, `"steps"`) {
		t.Errorf("summary --json 应含 steps:\n%s", out)
	}
	// --format mermaid
	out = captureStdout(func() {
		if code := cmdQuery([]string{"summary", writeNode, "--repo", dir, "--format", "mermaid"}); code != 0 {
			t.Errorf("query summary mermaid exit = %d", code)
		}
	})
	if !strings.Contains(out, "flowchart") {
		t.Errorf("summary mermaid 应输出 flowchart:\n%s", out)
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
