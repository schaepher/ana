package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/schaepher/codeintel/internal/action"
	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
)

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
