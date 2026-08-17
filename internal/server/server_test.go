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

// newTestServer 建临时 DB + 预填数据 + 临时源码文件，返回 httptest server。

// TestValueTraceEndpoint：⑬ 猎 bug——/api/value-trace 端点（此前 0%
// 覆盖）：数据值全链 JSON 输出 + 不存在的节点返回空。

// TestValueTraceMissingID：P0 补全——/api/value-trace 缺 id 参数 → 400。

// TestValueTraceConditionsEndpoint：⑬ 猎 bug 回归——/api/value-trace
// 返回 conditions 字段（Q92 条件标注此前前端缺失，CLI 有 HTTP 无）。

// TestHandleIncremental：POST /incremental（field_trace.md §20.1）——
// 未配置 buildFn → 404；配置后 → 202 + 异步执行；执行中 → 409。

// TestModuleCallsEndpoint：/api/module-calls（field_trace.md §21.3）——
// 空数据返回空数组；module-calls JSON 结构。

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
