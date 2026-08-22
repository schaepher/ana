package cli

import (
	"encoding/json"
	"testing"
)

// TestQueryContext：query context <节点> 一次调用输出全链上下文
// JSON——symbol/callees 字段齐全（Q235-5）。
func TestQueryContext(t *testing.T) {
	dir := seedRepo(t)
	out := captureStdout(func() {
		if code := cmdQuery([]string{"context", "symbol:go:example.com/m:main", "--repo", dir}); code != 0 {
			t.Errorf("query context exit = %d", code)
		}
	})
	var ctx map[string]any
	if err := json.Unmarshal([]byte(out), &ctx); err != nil {
		t.Fatalf("context JSON: %v\n%s", err, out)
	}
	sym, ok := ctx["symbol"].(map[string]any)
	if !ok || sym["Name"] != "main" {
		t.Errorf("symbol 应为主函数节点，got %v", ctx["symbol"])
	}
	if _, ok := ctx["callees"]; !ok {
		t.Errorf("callees 字段应存在（seedRepo 有 main → Run 调用边）")
	}
}

// TestQueryContextUnknown：未知符号报错（exit 1）。
func TestQueryContextUnknown(t *testing.T) {
	dir := seedRepo(t)
	if code := cmdQuery([]string{"context", "symbol:go:example.com/m:nope", "--repo", dir}); code != 1 {
		t.Errorf("未知符号 exit = %d, want 1", code)
	}
}
