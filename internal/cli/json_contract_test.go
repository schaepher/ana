package cli

// Q243（Agent-JSON 契约）：所有 --json 输出统一 snake_case。
// query context 直接 marshal domain.CodeEntity/Fact/FunctionFieldSummary/
// TraceRow——这些类型曾无 json tag，输出 camelCase（ID/FilePath/SourceID...），
// 破坏机器消费契约（Agent/MCP）。本测试固化 context 输出的键名契约。

import (
	"encoding/json"
	"strings"
	"testing"
)

// camelCaseKeys 契约禁止出现的字段名（Go 无 tag 时默认输出的键）。
var camelCaseKeys = []string{
	`"ID"`, `"Name"`, `"Kind"`, `"FilePath"`, `"LineStart"`, `"LineEnd"`,
	`"Properties"`, `"SourceID"`, `"TargetID"`, `"ToolSource"`, `"Confidence"`,
	`"Metadata"`, `"FunctionID"`, `"AccessKind"`, `"FieldPath"`,
	`"InstancePath"`, `"CodeSnippet"`, `"ParentID"`, `"EdgeKinds"`,
	`"IsUsage"`, `"FuncID"`, `"FullPath"`, `"DispatchCandidate"`,
	`"Access"`, `"Dir"`, `"Depth"`,
}

// TestJSONContractContextSnakeCase：query context --json 全键 snake_case。
func TestJSONContractContextSnakeCase(t *testing.T) {
	dir := seedFieldTrace(t)
	out := captureStdout(func() {
		if code := cmdQuery([]string{"context", "main", "--repo", dir, "--json"}); code != 0 {
			t.Errorf("context exit = %d", code)
		}
	})
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("json: %v\n%s", err, out)
	}
	sym, ok := m["symbol"].(map[string]any)
	if !ok {
		t.Fatalf("symbol 键缺失: %v", m["symbol"])
	}
	for _, want := range []string{"id", "name", "kind", "file_path", "line_start"} {
		if _, ok := sym[want]; !ok {
			t.Errorf("symbol 应含 snake_case 键 %q（当前键: %v）", want, sym)
		}
	}
	// callers/callees 是 []*domain.Fact → source_id/target_id
	for _, k := range []string{"callers", "callees"} {
		if rows, ok := m[k].([]any); ok {
			for _, r := range rows {
				row, _ := r.(map[string]any)
				if _, hasSrc := row["source_id"]; !hasSrc {
					if _, hasTgt := row["target_id"]; !hasTgt {
						t.Errorf("%s[0] 应含 source_id/target_id: %v", k, row)
					}
				}
			}
		}
	}
	// 全输出不得出现 camelCase 键
	for _, bad := range camelCaseKeys {
		if strings.Contains(out, bad) {
			t.Errorf("输出含契约禁止的 camelCase 键 %s", bad)
		}
	}
}
