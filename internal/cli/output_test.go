package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestQueryJSON：query 子命令 --json 输出合法 JSON 且不含日志噪音。
func TestQueryJSON(t *testing.T) {
	dir := seedFieldTrace(t)

	out := captureStdout(func() {
		if code := cmdQuery([]string{"symbol", "main", "--repo", dir, "--json"}); code != 0 {
			t.Errorf("query symbol --json exit = %d", code)
		}
	})
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("--json 输出应可解析: %v\n%s", err, out)
	}
	if m["id"] != "symbol:go:example.com/m:main" || m["name"] != "main" {
		t.Errorf("json fields = %v", m)
	}
	if strings.Contains(out, `"Name": "codeintel.main"`) {
		t.Errorf("stdout 不应混入 OTel span 日志: %s", out)
	}

	// value-trace --json：flows 数组结构
	writeNode := "symbol:go:example.com/m:main#t.A.write@5"
	out = captureStdout(func() {
		if code := cmdQuery([]string{"value-trace", writeNode, "--repo", dir, "--json"}); code != 0 {
			t.Errorf("query value-trace --json exit = %d", code)
		}
	})
	var v struct {
		Flows []map[string]any `json:"flows"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("value-trace --json 应可解析: %v\n%s", err, out)
	}
	if len(v.Flows) == 0 {
		t.Error("value-trace --json flows 为空")
	}
}

// TestQueryCompact：--compact 输出单行紧凑（无树形缩进）。
func TestQueryCompact(t *testing.T) {
	dir := seedFieldTrace(t)
	out := captureStdout(func() {
		if code := cmdQuery([]string{"trace-backward", "example.com/m.T.A",
			"--func", "main", "--repo", dir, "--compact"}); code != 0 {
			t.Errorf("query trace-backward --compact exit = %d", code)
		}
	})
	// 紧凑输出：不含多级空格缩进（树形渲染是 "  " 前缀），每行非缩进开头
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "→") && !strings.HasPrefix(line, "←") {
			t.Errorf("--compact 不应有缩进行: %q", line)
		}
	}
}

// TestExportGraph：export graph 子命令输出 mermaid/dot。
func TestExportGraph(t *testing.T) {
	dir := seedFieldTrace(t)
	target := "symbol:go:example.com/m:main"

	// dot（callees）
	out := captureStdout(func() {
		if code := cmdExport([]string{"graph", "--type", "callees", "--target", target, "--repo", dir}); code != 0 {
			t.Errorf("export graph callees exit = %d", code)
		}
	})
	if !strings.Contains(out, "digraph") {
		t.Errorf("dot 输出应含 digraph: %s", out)
	}
	if !strings.Contains(out, "(Svc).Run") {
		t.Errorf("dot 输出应含被调节点: %s", out)
	}

	// mermaid（value-trace）
	writeNode := "symbol:go:example.com/m:main#t.A.write@5"
	out = captureStdout(func() {
		if code := cmdExport([]string{"graph", "--type", "value-trace", "--target", writeNode, "--repo", dir}); code != 0 {
			t.Errorf("export graph value-trace exit = %d", code)
		}
	})
	if !strings.Contains(out, "flowchart") {
		t.Errorf("mermaid 输出应含 flowchart: %s", out)
	}

	// 参数校验：缺 --target
	out = captureStdout(func() {
		if code := cmdExport([]string{"graph", "--type", "callees", "--repo", dir}); code == 0 {
			t.Error("export graph 缺 --target 应失败")
		}
	})
	// 非法 --type
	if code := cmdExport([]string{"graph", "--type", "bogus", "--target", target, "--repo", dir}); code == 0 {
		t.Error("export graph 非法 --type 应失败")
	}
}
