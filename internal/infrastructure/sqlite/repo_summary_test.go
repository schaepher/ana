package sqlite

import (
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

// TestSummaryReplaceUpdatesStale：同 UNIQUE 键（function+access+field）
// 新内容插入 → 旧行被覆盖（行号更新）。
func TestSummaryReplaceUpdatesStale(t *testing.T) {
	r := newTestRepo(t)
	funcID := "symbol:go:example.com/m:Run"
	if err := r.SaveBatch(nodesForSummaryTest(funcID), nil); err != nil {
		t.Fatalf("save node: %v", err)
	}
	old := []*domain.FunctionFieldSummary{
		{FunctionID: domain.CanonicalID(funcID), AccessKind: "direct_read",
			FieldPath: "m.cfg.APIKey", InstancePath: "m.cfg.APIKey", LineStart: 10, CodeSnippet: "old"},
	}
	if _, err := r.SaveBatchStats(nil, nil, old); err != nil {
		t.Fatalf("save old summary: %v", err)
	}

	newS := []*domain.FunctionFieldSummary{
		{FunctionID: domain.CanonicalID(funcID), AccessKind: "direct_read",
			FieldPath: "m.cfg.APIKey", InstancePath: "m.cfg.APIKey", LineStart: 42, CodeSnippet: "new"},
	}
	if _, err := r.SaveBatchStats(nil, nil, newS); err != nil {
		t.Fatalf("save new summary: %v", err)
	}
	var line int
	if err := r.QueryRow(`SELECT line_start FROM function_field_summary
		WHERE function_id = ? AND access_kind = 'direct_read' AND field_path = 'm.cfg.APIKey'`,
		funcID).Scan(&line); err != nil {
		t.Fatalf("query summary: %v", err)
	}
	if line != 42 {
		t.Fatalf("OR REPLACE 应覆盖旧行（新行号 42），got %d", line)
	}
}

// TestOriginsReplaceIdempotent：origins 的 UNIQUE 含全部业务列（含
// call_line——不同调用点本来就是多行，设计语义），无内容陈旧问题；
// REPLACE 保证同键重复写入幂等（不产生重复行）。
func TestOriginsReplaceIdempotent(t *testing.T) {
	r := newTestRepo(t)
	funcID := "symbol:go:example.com/m:Run"
	callee := "symbol:go:example.com/m:Fill"
	if err := r.SaveBatch(nodesForSummaryTest(funcID, callee), nil); err != nil {
		t.Fatalf("save nodes: %v", err)
	}
	o := []*domain.SummaryOrigin{
		{FunctionID: domain.CanonicalID(funcID), AccessKind: "indirect_write",
			FieldPath: "m.cfg.APIKey", CallLine: 10, CalleeID: domain.CanonicalID(callee)},
	}
	for i := 0; i < 2; i++ {
		if _, err := r.SaveBatchStats(nil, nil, nil, o); err != nil {
			t.Fatalf("save origin #%d: %v", i, err)
		}
	}
	var cnt int
	if err := r.QueryRow(`SELECT COUNT(*) FROM summary_origins
		WHERE function_id = ? AND field_path = 'm.cfg.APIKey'`, funcID).Scan(&cnt); err != nil {
		t.Fatalf("count: %v", err)
	}
	if cnt != 1 {
		t.Fatalf("同键重复写入应幂等（1 行），got %d", cnt)
	}
}

// nodesForSummaryTest 摘要测试用函数节点（FK 端点必须存在）。
func nodesForSummaryTest(funcIDs ...string) []*domain.CodeEntity {
	var out []*domain.CodeEntity
	for _, id := range funcIDs {
		out = append(out, &domain.CodeEntity{
			ID: domain.CanonicalID(id), Kind: domain.KindFunction, Name: "Run",
			FilePath: "a.go", LineStart: 1,
			Properties: map[string]any{"func_id": id},
		})
	}
	return out
}
