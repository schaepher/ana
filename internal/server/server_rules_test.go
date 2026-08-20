package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/schaepher/codeintel/internal/action"
	"github.com/schaepher/codeintel/internal/domain"
	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
)

// TestHandleRules：/api/rules——ER 页面配置用户连线规则（Q226）。
// GET 列表 / POST 添加（JSON：from_col/to_table 等）/ DELETE 删除；
// 添加后 /api/er 响应合并规则生成的 fk 线（读取期合并，无需 reindex）。
func TestHandleRules(t *testing.T) {
	dir := t.TempDir()
	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	r := sqlite.NewRepo(db)

	funcID := "symbol:go:example.com/m:find"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(funcID), Kind: domain.KindFunction, Name: "find", FilePath: "a.go"},
		{ID: domain.CanonicalID(funcID + "#ext.sql.table_a.id.read@6"), Kind: domain.KindFieldAccess,
			Name: "table_a.id", FilePath: "a.go", LineStart: 6,
			Properties: map[string]any{"full_path": "table_a.id", "instance_path": "table_a.id",
				"access_kind": "read", "type_string": "sql", "is_external": "true", "func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#ext.sql.table_b.a_id.filter@9"), Kind: domain.KindFieldAccess,
			Name: "table_b.a_id", FilePath: "a.go", LineStart: 9,
			Properties: map[string]any{"full_path": "table_b.a_id", "instance_path": "table_b.a_id",
				"access_kind": "filter", "type_string": "sql", "is_external": "true", "func_id": funcID}},
	}
	if _, err := r.SaveBatchStats(nodes, nil, nil); err != nil {
		t.Fatalf("save: %v", err)
	}

	if err := r.Save(&domain.BuildMeta{BuildID: "b1", ToolName: "all", Status: "success"}); err != nil {
		t.Fatalf("Save build: %v", err)
	}
	if err := r.PrecomputeAllRelations(nil); err != nil {
		t.Fatalf("precompute: %v", err)
	}

	web := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<html></html>")}}
	srv := New(context.Background(), action.New(r), web, dir)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	resp, m := get(t, ts, "/api/rules")
	if resp.StatusCode != 200 {
		t.Fatalf("GET rules status = %d", resp.StatusCode)
	}
	rules, _ := m["rules"].([]any)
	if len(rules) != 0 {
		t.Fatalf("初始 rules = %v", rules)
	}

	resp, m = post(t, ts, "/api/rules", map[string]any{
		"from_table": "table_a", "from_col": "id",
		"to_table": "table_b", "to_col": "a_id",
	})
	if resp.StatusCode != 201 {
		t.Fatalf("POST rules status = %d, body=%v", resp.StatusCode, m)
	}
	ruleID, _ := m["id"].(float64)
	if ruleID <= 0 {
		t.Fatalf("POST rules id = %v", m)
	}

	resp, m = get(t, ts, "/api/rules")
	rules, _ = m["rules"].([]any)
	if len(rules) != 1 {
		t.Fatalf("rules = %v", rules)
	}
	r0 := rules[0].(map[string]any)
	if r0["from_table"] != "table_a" || r0["from_col"] != "id" ||
		r0["to_table"] != "table_b" || r0["to_col"] != "a_id" {
		t.Fatalf("rule = %v", r0)
	}

	resp, m = get(t, ts, "/api/er")
	rels, _ := m["relations"].([]any)
	found := false
	for _, rl := range rels {
		rr := rl.(map[string]any)
		if rr["from_table"] == "table_a" && rr["from_col"] == "id" &&
			rr["to_table"] == "table_b" && rr["to_col"] == "a_id" &&
			rr["type"] == "fk" {
			found = true
		}
	}
	if !found {
		t.Fatalf("ER relations 应含规则线 table_a.id → table_b.a_id [fk]，relations=%v", rels)
	}

	req := newReq(t, ts, "DELETE", "/api/rules?id="+fmt.Sprintf("%d", int64(ruleID)), nil)
	resp, m = doReq(t, req)
	if resp.StatusCode != 200 {
		t.Fatalf("DELETE rules status = %d, body=%v", resp.StatusCode, m)
	}

	resp, m = get(t, ts, "/api/rules")
	rules, _ = m["rules"].([]any)
	if len(rules) != 0 {
		t.Fatalf("删除后 rules = %v", rules)
	}

	resp, m = post(t, ts, "/api/rules", map[string]any{"from_col": "id"})
	if resp.StatusCode != 400 {
		t.Fatalf("无效规则 status = %d, body=%v", resp.StatusCode, m)
	}
}

// post 发送 JSON POST 请求（测试 helper）。
func post(t *testing.T, ts *httptest.Server, path string, body map[string]any) (*http.Response, map[string]any) {
	t.Helper()
	req := newReq(t, ts, "POST", path, body)
	return doReq(t, req)
}

// newReq 构造请求（测试 helper）。
func newReq(t *testing.T, ts *httptest.Server, method, path string, body map[string]any) *http.Request {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, ts.URL+path, rd)
	if err != nil {
		t.Fatalf("NewRequest %s %s: %v", method, path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

// doReq 执行请求并解码 JSON（测试 helper）。
func doReq(t *testing.T, req *http.Request) (*http.Response, map[string]any) {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do %s %s: %v", req.Method, req.URL, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {

		m = map[string]any{"_raw": string(body)}
	}
	return resp, m
}
