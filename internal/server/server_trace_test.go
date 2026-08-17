package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/schaepher/codeintel/internal/action"
	"github.com/schaepher/codeintel/internal/domain"
	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
)

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

	resp, body = get(t, ts, "/api/value-trace?id=nope")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("missing node status = %d", resp.StatusCode)
	}
	if flows, _ := body["flows"].([]any); len(flows) != 0 {
		t.Fatalf("missing node flows = %v, want empty", flows)
	}
}

// TestValueTraceMissingID：P0 补全——/api/value-trace 缺 id 参数 → 400。
func TestValueTraceMissingID(t *testing.T) {
	repoDir := t.TempDir()
	db, err := sqlite.Open(repoDir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	web := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<html>x</html>")}}
	ts := httptest.NewServer(New(context.Background(), action.New(sqlite.NewRepo(db)), web, repoDir).Handler())
	t.Cleanup(ts.Close)

	resp, body := get(t, ts, "/api/value-trace")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing id status = %d, want 400 (body=%v)", resp.StatusCode, body)
	}
	if msg, _ := body["error"].(string); !strings.Contains(msg, "id") {
		t.Errorf("missing id body = %v, want error 提示缺 id", body)
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
