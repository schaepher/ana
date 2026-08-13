package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/schaepher/codeintel/internal/domain"
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
	srv := New(context.Background(), r, web, repoDir)
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
