package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/schaepher/codeintel/internal/domain"
	"github.com/schaepher/codeintel/internal/action"
	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
)

// newTestServer 建临时 DB + 预填数据 + 临时源码文件，返回 httptest server。
func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	repoDir := t.TempDir()
	// 临时源码（/api/source 读取）
	if err := os.MkdirAll(filepath.Join(repoDir, "internal", "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	src := "package app\n\n// Greet 打招呼。\nfunc Greet(name string) string {\n\treturn \"hi \" + name\n}\n"
	if err := os.WriteFile(filepath.Join(repoDir, "internal", "app", "app.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	db, err := sqlite.Open(repoDir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	r := sqlite.NewRepo(db)

	greet := &domain.CodeEntity{
		ID:        "symbol:go:example.com/app:Greet",
		Kind:      domain.KindFunction,
		Name:      "Greet",
		FilePath:  "internal/app/app.go",
		LineStart: 3,
		LineEnd:   5,
		Properties: map[string]any{
			"signature":   "func Greet(name string) string",
			"doc_comment": "Greet 打招呼。",
		},
	}
	main := &domain.CodeEntity{
		ID:        "symbol:go:example.com/app:main",
		Kind:      domain.KindFunction,
		Name:      "main",
		FilePath:  "cmd/main.go",
		LineStart: 1,
		LineEnd:   3,
		Properties: map[string]any{
			"serves_http": "true",
		},
	}
	svc := &domain.CodeEntity{
		ID:       "symbol:go:example.com/app:Svc",
		Kind:     domain.KindStruct,
		Name:     "Svc",
		FilePath: "internal/app/svc.go",
		Properties: map[string]any{
			"fields": []any{map[string]any{"name": "Port", "type": "int"}},
		},
	}
	if _, err := r.SaveBatchStats([]*domain.CodeEntity{greet, main, svc}, nil, nil); err != nil {
		t.Fatalf("save nodes: %v", err)
	}
	if _, err := r.SaveBatchStats(nil, []*domain.Fact{{
		SourceID: main.ID, TargetID: greet.ID, Kind: domain.FactCalls,
		Confidence: 0.8, Metadata: map[string]any{"line_num": 2},
	}}, nil); err != nil {
		t.Fatalf("save edge: %v", err)
	}

	web := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<html>codeintel</html>")},
	}
	srv := New(context.Background(), action.New(r), web, repoDir)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func get(t *testing.T, ts *httptest.Server, path string) (*http.Response, map[string]any) {
	t.Helper()
	resp, err := http.Get(ts.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return resp, m
}

func TestAPIEndpoints(t *testing.T) {
	ts := newTestServer(t)

	// roots：main + serves_http 标记 → http flag
	resp, m := get(t, ts, "/api/roots")
	if resp.StatusCode != 200 {
		t.Fatalf("roots status = %d", resp.StatusCode)
	}
	nodes, _ := m["nodes"].([]any)
	if len(nodes) != 1 {
		t.Fatalf("roots = %v", nodes)
	}
	first := nodes[0].(map[string]any)
	flags, _ := first["flags"].([]any)
	if len(flags) != 2 || flags[0] != "main" || flags[1] != "http" {
		t.Errorf("main flags = %v, want [main http]", flags)
	}

	// search：缺 q → 400
	if resp, _ := get(t, ts, "/api/search"); resp.StatusCode != 400 {
		t.Errorf("search without q status = %d, want 400", resp.StatusCode)
	}
	// search 正常
	_, m = get(t, ts, "/api/search?q=Greet")
	if nodes, _ := m["nodes"].([]any); len(nodes) != 1 {
		t.Errorf("search Greet = %v", nodes)
	}

	// expand：缺 id → 400；不存在 → 404
	if resp, _ := get(t, ts, "/api/expand"); resp.StatusCode != 400 {
		t.Errorf("expand without id status = %d", resp.StatusCode)
	}
	if resp, _ := get(t, ts, "/api/expand?id=nope"); resp.StatusCode != 404 {
		t.Errorf("expand missing symbol status = %d", resp.StatusCode)
	}
	// expand 正常：main → Greet（calls 出边）
	resp, m = get(t, ts, "/api/expand?id=symbol:go:example.com/app:main")
	if resp.StatusCode != 200 {
		t.Fatalf("expand status = %d", resp.StatusCode)
	}
	edges, _ := m["edges"].([]any)
	if len(edges) != 1 {
		t.Fatalf("expand edges = %v", edges)
	}
	e := edges[0].(map[string]any)
	if e["direction"] != "out" || e["kind"] != "calls" {
		t.Errorf("edge = %v", e)
	}
	if e["line"] != float64(2) {
		t.Errorf("edge line = %v, want 2", e["line"])
	}
	neighbors, _ := m["neighbors"].([]any)
	if len(neighbors) != 1 {
		t.Errorf("neighbors = %v", neighbors)
	}
}

func TestSourceEndpoint(t *testing.T) {
	ts := newTestServer(t)
	// 缺 id → 400
	if resp, _ := get(t, ts, "/api/source"); resp.StatusCode != 400 {
		t.Errorf("source without id = %d", resp.StatusCode)
	}
	// struct → 400（仅 function/method）
	if resp, _ := get(t, ts, "/api/source?id=symbol:go:example.com/app:Svc"); resp.StatusCode != 400 {
		t.Errorf("source struct = %d, want 400", resp.StatusCode)
	}
	// 不存在 → 404
	if resp, _ := get(t, ts, "/api/source?id=nope"); resp.StatusCode != 404 {
		t.Errorf("source missing = %d, want 404", resp.StatusCode)
	}
	// 正常：函数源码 + 行号。节点 LineStart=3（doc 注释行）而声明在第 4
	// 行——精确匹配失败后走名称匹配兜底，返回声明实际行号
	resp, m := get(t, ts, "/api/source?id=symbol:go:example.com/app:Greet")
	if resp.StatusCode != 200 {
		t.Fatalf("source status = %d", resp.StatusCode)
	}
	if m["line"] != float64(4) {
		t.Errorf("source line = %v, want 4 (name-match fallback)", m["line"])
	}
	code, _ := m["code"].(string)
	if !strings.Contains(code, "func Greet") || !strings.Contains(code, "hi ") {
		t.Errorf("source code = %q", code)
	}
	if m["file"] != "internal/app/app.go" {
		t.Errorf("source file = %v", m["file"])
	}
}

func TestStaticFile(t *testing.T) {
	ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("static status = %d", resp.StatusCode)
	}
	buf := make([]byte, 64)
	n, _ := resp.Body.Read(buf)
	if !strings.Contains(string(buf[:n]), "codeintel") {
		t.Errorf("static content = %q", string(buf[:n]))
	}
}

func TestNodeToJSON(t *testing.T) {
	// main 入口标记 + fields 转换
	n := &domain.CodeEntity{
		ID:   "symbol:go:example.com/app:main",
		Kind: domain.KindFunction,
		Name: "main",
		Properties: map[string]any{
			"serves_http": "true",
			"framework":   "true",
		},
	}
	j := nodeToJSON(n)
	if len(j.Flags) != 3 || j.Flags[0] != "main" || j.Flags[1] != "framework" || j.Flags[2] != "http" {
		t.Errorf("flags = %v", j.Flags)
	}
	// framework 标记
	n2 := &domain.CodeEntity{ID: "x", Kind: domain.KindStruct, Name: "S", Properties: map[string]any{"framework": "true"}}
	if j2 := nodeToJSON(n2); len(j2.Flags) != 1 || j2.Flags[0] != "framework" {
		t.Errorf("framework flags = %v", j2.Flags)
	}
	// fields
	n3 := &domain.CodeEntity{ID: "y", Kind: domain.KindStruct, Name: "S", Properties: map[string]any{
		"fields": []any{map[string]any{"name": "Port", "type": "int"}},
	}}
	if j3 := nodeToJSON(n3); len(j3.Fields) != 1 || j3.Fields[0].Name != "Port" || j3.Fields[0].Type != "int" {
		t.Errorf("fields = %+v", j3.Fields)
	}
	// 非法 fields 条目跳过
	n4 := &domain.CodeEntity{ID: "z", Kind: domain.KindStruct, Name: "S", Properties: map[string]any{
		"fields": []any{"bad"},
	}}
	if j4 := nodeToJSON(n4); len(j4.Fields) != 0 {
		t.Errorf("bad fields = %+v", j4.Fields)
	}
}

func TestFlowsEndpoint(t *testing.T) {
	repoDir := t.TempDir()
	db, err := sqlite.Open(repoDir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	r := sqlite.NewRepo(db)
	funcID := "symbol:go:example.com/app:Greet"
	greet := &domain.CodeEntity{
		ID: domain.CanonicalID(funcID), Kind: domain.KindFunction, Name: "Greet",
		FilePath: "app.go", LineStart: 1,
		Properties: map[string]any{"signature": "func Greet(s string) string"},
	}
	write := &domain.CodeEntity{
		ID: domain.CanonicalID(funcID + "#s.Name.write@2"), Kind: domain.KindFieldAccess,
		Name: "s.Name", FilePath: "app.go", LineStart: 2,
		Properties: map[string]any{"full_path": "example.com/app.T.Name",
			"instance_path": "s.Name", "access_kind": "write", "func_id": funcID},
	}
	result := &domain.CodeEntity{
		ID: domain.CanonicalID(funcID + "#t0"), Kind: domain.KindSSAValue, Name: "t0",
		Properties: map[string]any{"func_id": funcID},
	}
	if _, err := r.SaveBatchStats([]*domain.CodeEntity{greet, write, result}, []*domain.Fact{{
		SourceID: write.ID, TargetID: result.ID, Kind: domain.FactDataFlowsTo,
		ToolSource: domain.ToolSSA, Confidence: 1,
	}}, nil); err != nil {
		t.Fatalf("save: %v", err)
	}
	web := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<html>x</html>")}}
	ts := httptest.NewServer(New(context.Background(), action.New(r), web, repoDir).Handler())
	t.Cleanup(ts.Close)

	resp, body := get(t, ts, "/api/flows?id="+url.QueryEscape(funcID))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %v", resp.StatusCode, body)
	}
	flows, ok := body["flows"].([]any)
	if !ok {
		t.Fatalf("flows missing: %v", body)
	}
	if len(flows) != 2 {
		t.Fatalf("flows = %d, want 2 (anchor + result)", len(flows))
	}
	first := flows[0].(map[string]any)
	if first["kind"] != "field_access" || first["access"] != "write" {
		t.Errorf("anchor row = %v", first)
	}
	second := flows[1].(map[string]any)
	if second["dir"].(float64) != 1 || second["edgeKind"] != "data_flows_to" {
		t.Errorf("use-chain row = %v", second)
	}
}

// TestValueTraceEndpoint：⑬ 猎 bug——/api/value-trace 端点（此前 0%
// 覆盖）：数据值全链 JSON 输出 + 不存在的节点返回空。
func TestValueTraceEndpoint(t *testing.T) {
	repoDir := t.TempDir()
	db, err := sqlite.Open(repoDir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	r := sqlite.NewRepo(db)
	funcID := "symbol:go:example.com/app:Greet"
	write := &domain.CodeEntity{
		ID: domain.CanonicalID(funcID + "#s.Name.write@2"), Kind: domain.KindFieldAccess,
		Name: "s.Name", FilePath: "app.go", LineStart: 2,
		Properties: map[string]any{"full_path": "example.com/app.T.Name",
			"instance_path": "s.Name", "access_kind": "write", "func_id": funcID},
	}
	val := &domain.CodeEntity{
		ID: domain.CanonicalID(funcID + "#t0"), Kind: domain.KindSSAValue, Name: "t0",
		Properties: map[string]any{"func_id": funcID},
	}
	if _, err := r.SaveBatchStats([]*domain.CodeEntity{write, val}, []*domain.Fact{{
		SourceID: val.ID, TargetID: write.ID, Kind: domain.FactDataFlowsTo,
		ToolSource: domain.ToolSSA, Confidence: 1,
	}}, nil); err != nil {
		t.Fatalf("save: %v", err)
	}
	web := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<html>x</html>")}}
	ts := httptest.NewServer(New(context.Background(), action.New(r), web, repoDir).Handler())
	t.Cleanup(ts.Close)

	resp, body := get(t, ts, "/api/value-trace?id="+url.QueryEscape(string(write.ID)))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %v", resp.StatusCode, body)
	}
	flows, ok := body["flows"].([]any)
	if !ok {
		t.Fatalf("flows missing: %v", body)
	}
	if len(flows) != 2 {
		t.Fatalf("flows = %d, want 2", len(flows))
	}
	// 不存在的节点 → 空 flows 且 200（不报错）
	resp, body = get(t, ts, "/api/value-trace?id=nope")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("missing node status = %d", resp.StatusCode)
	}
	if flows, _ := body["flows"].([]any); len(flows) != 0 {
		t.Fatalf("missing node flows = %v, want empty", flows)
	}
}

// TestValueTraceConditionsEndpoint：⑬ 猎 bug 回归——/api/value-trace
// 返回 conditions 字段（Q92 条件标注此前前端缺失，CLI 有 HTTP 无）。
func TestValueTraceConditionsEndpoint(t *testing.T) {
	repoDir := t.TempDir()
	db, err := sqlite.Open(repoDir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	r := sqlite.NewRepo(db)
	funcID := "symbol:go:example.com/app:Greet"
	// 真实源码文件（TraceConditions 解析 AST 标注条件）
	writeFileAt := func() {}
	_ = writeFileAt
	srcPath := filepath.Join(repoDir, "app.go")
	if err := os.WriteFile(srcPath, []byte(`package app

func Greet(s *T) {
	if s.Name == "" {
		s.Name = "x"
	}
}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	write := &domain.CodeEntity{
		ID: domain.CanonicalID(funcID + "#s.Name.write@4"), Kind: domain.KindFieldAccess,
		Name: "s.Name", FilePath: "app.go", LineStart: 4,
		Properties: map[string]any{"full_path": "example.com/app.T.Name",
			"instance_path": "s.Name", "access_kind": "write", "func_id": funcID},
	}
	val := &domain.CodeEntity{
		ID: domain.CanonicalID(funcID + "#t0"), Kind: domain.KindSSAValue, Name: "t0",
		Properties: map[string]any{"func_id": funcID},
	}
	if _, err := r.SaveBatchStats([]*domain.CodeEntity{write, val}, []*domain.Fact{{
		SourceID: val.ID, TargetID: write.ID, Kind: domain.FactDataFlowsTo,
		ToolSource: domain.ToolSSA, Confidence: 1,
	}}, nil); err != nil {
		t.Fatalf("save: %v", err)
	}
	web := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<html>x</html>")}}
	ts := httptest.NewServer(New(context.Background(), action.New(r), web, repoDir).Handler())
	t.Cleanup(ts.Close)

	resp, body := get(t, ts, "/api/value-trace?id="+url.QueryEscape(string(write.ID)))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %v", resp.StatusCode, body)
	}
	flows, _ := body["flows"].([]any)
	for _, raw := range flows {
		f := raw.(map[string]any)
		if f["name"] == "s.Name" && f["access"] == "write" && f["line"] == float64(4) {
			conds, ok := f["conditions"].([]any)
			if !ok || len(conds) == 0 {
				t.Errorf("写节点缺 conditions（应在 if 分支内标注）: %v", f)
			}
			return
		}
	}
	t.Errorf("value-trace 输出缺写节点: %v", body)
}

// TestHandleIncremental：POST /incremental（field_trace.md §20.1）——
// 未配置 buildFn → 404；配置后 → 202 + 异步执行；执行中 → 409。
func TestHandleIncremental(t *testing.T) {
	dir := t.TempDir()
	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	r := sqlite.NewRepo(db)
	web := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("x")}}
	srv := New(context.Background(), action.New(r), web, dir)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	// 1. 未配置 buildFn → 404
	resp, err := http.Post(ts.URL+"/incremental", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("未配置 buildFn 应 404，got %d", resp.StatusCode)
	}

	// 2. 配置后 → 202 + buildFn 异步执行
	executed := make(chan string, 1)
	srv.SetBuildFunc(func() (string, error) {
		executed <- "ok"
		return "build-1", nil
	})
	resp, err = http.Post(ts.URL+"/incremental", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("应 202，got %d", resp.StatusCode)
	}
	select {
	case <-executed:
	case <-time.After(2 * time.Second):
		t.Fatal("buildFn 未异步执行")
	}

	// 3. 执行中 → 409
	block := make(chan struct{})
	srv.SetBuildFunc(func() (string, error) { <-block; return "", nil })
	// 触发第一个请求（进入 building 状态）
	go func() {
		http.Post(ts.URL+"/incremental", "application/json", nil)
	}()
	// 等待 building 置位
	deadline := time.Now().Add(2 * time.Second)
	for {
		srv.buildMu.Lock()
		b := srv.building
		srv.buildMu.Unlock()
		if b {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("building 未置位")
		}
		time.Sleep(10 * time.Millisecond)
	}
	resp, err = http.Post(ts.URL+"/incremental", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("执行中应 409，got %d", resp.StatusCode)
	}
	close(block)
}

// TestModuleCallsEndpoint：/api/module-calls（field_trace.md §21.3）——
// 空数据返回空数组；module-calls JSON 结构。
func TestModuleCallsEndpoint(t *testing.T) {
	ts := newTestServer(t)
	resp, m := get(t, ts, "/api/module-calls")
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	calls, ok := m["calls"].([]any)
	if !ok {
		t.Fatalf("calls 字段缺失: %v", m)
	}
	if len(calls) != 0 {
		t.Errorf("无调用数据应为空数组: %v", calls)
	}
}

// TestModulesPage：/modules.html 前端模块视图可达。
func TestModulesPage(t *testing.T) {
	dir := t.TempDir()
	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	web := fstest.MapFS{
		"index.html":   &fstest.MapFile{Data: []byte("x")},
		"modules.html": &fstest.MapFile{Data: []byte("<html>modules</html>")},
	}
	srv := New(context.Background(), action.New(sqlite.NewRepo(db)), web, dir)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	resp, err := http.Get(ts.URL + "/modules.html")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("modules.html status = %d", resp.StatusCode)
	}
}
