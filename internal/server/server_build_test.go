package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/schaepher/codeintel/internal/action"
	"github.com/schaepher/codeintel/internal/domain"
	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
)

// newTestServer 建临时 DB + 预填数据 + 临时源码文件，返回 httptest server。
func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	repoDir := t.TempDir()

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

	resp, err := http.Post(ts.URL+"/incremental", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("未配置 buildFn 应 404，got %d", resp.StatusCode)
	}

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

	block := make(chan struct{})
	srv.SetBuildFunc(func() (string, error) { <-block; return "", nil })

	go func() {
		http.Post(ts.URL+"/incremental", "application/json", nil)
	}()

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
