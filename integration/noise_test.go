//go:build integration

package integration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
)

// TestServerEndToEnd：init 后起 HTTP serve（真实前端资源），全 API 验证。
// TestOutputNoiseFree：真实仓库 init 后——stdout 无日志混流（日志入
// .codeintel/codeintel.log）、--json 可解析、--compact 生效、export graph
// 输出 mermaid/dot（Q88/Q89/Q96）。
func TestOutputNoiseFree(t *testing.T) {
	if !scipGoAvailable() {
		t.Skip("scip-go not found (install: go install github.com/scip-code/scip-go/cmd/scip-go@latest)")
	}
	dir := fixtureRepo(t)

	code, out := runCLIOut(t, "init", "--repo", dir)
	if code != 0 {
		t.Fatalf("init exit = %d", code)
	}
	if strings.Contains(out, `"Name": "codeintel.main"`) {
		t.Errorf("init stdout 不应混入 OTel span 日志")
	}

	logPath := filepath.Join(dir, ".codeintel", "codeintel.log")
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("日志文件缺失: %v", err)
	}

	code, out = runCLIOut(t, "query", "symbol", "main", "--repo", dir)
	if code != 0 {
		t.Fatalf("query exit = %d", code)
	}
	if strings.Contains(out, "enter cmdQuery") || strings.Contains(out, "DEBUG") ||
		strings.Contains(out, `"Name": "codeintel.main"`) {
		t.Errorf("日志不应出现在 stdout: %s", out[:min(len(out), 200)])
	}

	code, out = runCLIOut(t, "query", "symbol", "main", "--repo", dir, "--json")
	if code != 0 {
		t.Fatalf("query symbol --json exit = %d", code)
	}
	var sym map[string]any
	if err := json.Unmarshal([]byte(out), &sym); err != nil {
		t.Fatalf("symbol --json 应可解析: %v\n%s", err, out)
	}
	if sym["name"] != "main" {
		t.Errorf("symbol json = %v", sym)
	}

	handleID := "symbol:go:example.com/app/svc:(Service).Handle"
	code, out = runCLIOut(t, "export", "graph", "--type", "callees", "--target", handleID, "--repo", dir)
	if code != 0 {
		t.Fatalf("export graph callees exit = %d", code)
	}
	if !strings.Contains(out, "digraph") {
		t.Errorf("callees graph 应输出 dot: %s", out[:min(len(out), 200)])
	}

	faID := fieldAccessID(t, sqlite.NewRepo(mustOpen(t, dir)), handleID, "s.Name", "read")
	if faID == "" {
		t.Fatalf("Handle s.Name read node missing")
	}
	code, out = runCLIOut(t, "export", "graph", "--type", "value-trace", "--target", faID, "--repo", dir)
	if code != 0 {
		t.Fatalf("export graph value-trace exit = %d", code)
	}
	if !strings.Contains(out, "flowchart") {
		t.Errorf("value-trace graph 应输出 mermaid: %s", out[:min(len(out), 200)])
	}
}
