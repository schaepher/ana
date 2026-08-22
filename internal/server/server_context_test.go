package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"testing/fstest"

	"github.com/schaepher/codeintel/internal/action"
	"github.com/schaepher/codeintel/internal/domain"
	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
)

// TestContextEndpoint：/api/context?node=<锚点> 返回全链上下文
// JSON（Q235-5）——symbol/callees 字段齐全；缺 node 参数 400；
// 未知符号 500。
func TestContextEndpoint(t *testing.T) {
	repoDir := t.TempDir()
	db, err := sqlite.Open(repoDir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	r := sqlite.NewRepo(db)
	mainID := domain.CanonicalID("symbol:go:example.com/app:main")
	runID := domain.CanonicalID("symbol:go:example.com/app/svc:(Svc).Run")
	if _, err := r.SaveBatchStats([]*domain.CodeEntity{
		{ID: mainID, Kind: domain.KindFunction, Name: "main", FilePath: "app.go", LineStart: 1},
		{ID: runID, Kind: domain.KindMethod, Name: "(Svc).Run", FilePath: "svc.go", LineStart: 3},
	}, []*domain.Fact{{
		SourceID: mainID, TargetID: runID, Kind: domain.FactCalls,
		ToolSource: domain.ToolCodeGraph, Confidence: 0.9,
	}}, nil); err != nil {
		t.Fatalf("save: %v", err)
	}
	web := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<html>x</html>")}}
	ts := httptest.NewServer(New(context.Background(), action.New(r), web, repoDir).Handler())
	t.Cleanup(ts.Close)

	resp, body := get(t, ts, "/api/context?node="+url.QueryEscape(string(mainID)))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %v", resp.StatusCode, body)
	}
	sym, ok := body["symbol"].(map[string]any)
	if !ok || sym["name"] != "main" {
		t.Errorf("symbol 应为主函数节点，got %v", body["symbol"])
	}
	if _, ok := body["callees"]; !ok {
		t.Errorf("callees 字段应存在，got %v", body)
	}
	// 缺 node 参数 → 400
	if resp, _ := get(t, ts, "/api/context"); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("缺 node 参数 status = %d, want 400", resp.StatusCode)
	}
	// 未知符号 → 500
	if resp, _ := get(t, ts, "/api/context?node="+url.QueryEscape("symbol:go:example.com/app:nope")); resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("未知符号 status = %d, want 500", resp.StatusCode)
	}
}
