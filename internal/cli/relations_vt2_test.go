package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
)

// TestValueTraceMinConfCLI：Q161——value-trace --min-conf 剪枝低置信
// 候选边（0.7 < 0.8），且边级候选标注 JSON 输出。
func TestValueTraceMinConfCLI(t *testing.T) {
	dir := seedRepo(t)
	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	r := sqlite.NewRepo(db)
	callerID := "symbol:go:example.com/m:g"
	funcID := "symbol:go:example.com/m:f"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(callerID), Kind: domain.KindFunction, Name: "g", FilePath: "g.go"},
		{ID: domain.CanonicalID(funcID), Kind: domain.KindFunction, Name: "f", FilePath: "f.go"},
		{ID: domain.CanonicalID(callerID + "#t0"), Kind: domain.KindSSAValue, Name: "t0",
			Properties: map[string]any{"func_id": callerID}},
		{ID: domain.CanonicalID(funcID + "#a"), Kind: domain.KindSSAValue, Name: "a",
			Properties: map[string]any{"func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#a.X.read@3"), Kind: domain.KindFieldAccess, Name: "a.X",
			FilePath: "f.go", LineStart: 3,
			Properties: map[string]any{"func_id": funcID, "full_path": "example.com/m.T.X",
				"access_kind": "read"}},
	}
	if _, err := r.SaveBatchStats(nodes, []*domain.Fact{
		{SourceID: domain.CanonicalID(callerID + "#t0"), TargetID: domain.CanonicalID(funcID + "#a"),
			Kind: domain.FactArgument, ToolSource: domain.ToolSSA, Confidence: 1,
			Metadata: map[string]any{"interface": "example.com/m.Fee",
				"candidate_origin": "enum", "confidence": 0.7}},
		{SourceID: domain.CanonicalID(funcID + "#a"), TargetID: domain.CanonicalID(funcID + "#a.X.read@3"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
	}, nil); err != nil {
		t.Fatalf("save: %v", err)
	}
	anchor := string(domain.CanonicalID(funcID + "#a.X.read@3"))

	out := captureStdout(func() {
		if code := cmdQuery([]string{"value-trace", anchor, "--repo", dir, "--json"}); code != 0 {
			t.Errorf("value-trace exit = %d", code)
		}
	})
	var flows struct {
		Flows []map[string]any `json:"flows"`
	}
	if err := json.Unmarshal([]byte(out), &flows); err != nil {
		t.Fatalf("value-trace JSON: %v\n%s", err, out)
	}
	for _, f := range flows.Flows {
		if f["func_id"] == callerID {
			t.Error("默认模式候选路径不应出现（Q163 候选边剪枝）")
		}
	}

	out = captureStdout(func() {
		if code := cmdQuery([]string{"value-trace", anchor, "--repo", dir, "--min-conf", "0", "--json"}); code != 0 {
			t.Errorf("value-trace --min-conf exit = %d", code)
		}
	})
	if err := json.Unmarshal([]byte(out), &flows); err != nil {
		t.Fatalf("value-trace --min-conf JSON: %v", err)
	}
	marked := false
	for _, f := range flows.Flows {
		if ec, ok := f["edge_candidate"].(map[string]any); ok {
			marked = true
			if ec["origin"] != "enum" {
				t.Errorf("edge_candidate.origin = %v, want enum", ec["origin"])
			}
		}
	}
	if !marked {
		t.Error("--min-conf 0 后应有 edge_candidate 标注")
	}
}

// TestTraceBackwardIndirectCLI：Q172——trace-backward --follow-indirect
// 经 summary_origins 链到达下游真实写者；默认（无 flag）为空。
func TestTraceBackwardIndirectCLI(t *testing.T) {
	dir := seedRepo(t)
	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	r := sqlite.NewRepo(db)
	outerID := "symbol:go:example.com/m:outer"
	fillID := "symbol:go:example.com/m:fill"
	if _, err := r.SaveBatchStats([]*domain.CodeEntity{
		{ID: domain.CanonicalID(outerID), Kind: domain.KindFunction, Name: "outer"},
		{ID: domain.CanonicalID(fillID), Kind: domain.KindFunction, Name: "fill"},
		{ID: domain.CanonicalID(fillID + "#t.A.write@9"), Kind: domain.KindFieldAccess,
			Name: "t.A", FilePath: "f.go", LineStart: 9,
			Properties: map[string]any{"full_path": "example.com/m.T.A",
				"instance_path": "t.A", "access_kind": "write", "func_id": fillID}},
		{ID: domain.CanonicalID(fillID + "#v"), Kind: domain.KindSSAValue, Name: "v",
			Properties: map[string]any{"func_id": fillID}},
	}, []*domain.Fact{
		{SourceID: domain.CanonicalID(fillID + "#v"), TargetID: domain.CanonicalID(fillID + "#t.A.write@9"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
	}, []*domain.FunctionFieldSummary{
		{FunctionID: domain.CanonicalID(outerID), AccessKind: domain.SummaryIndirectWrite,
			FieldPath: "example.com/m.T.A", LineStart: 2},
	}, []*domain.SummaryOrigin{
		{FunctionID: domain.CanonicalID(outerID), AccessKind: domain.SummaryIndirectWrite,
			FieldPath: "example.com/m.T.A", CallLine: 3, CalleeID: domain.CanonicalID(fillID)},
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	out := captureStdout(func() {
		if code := cmdQuery([]string{"trace-backward", "example.com/m.T.A", "--func", "outer", "--repo", dir}); code != 0 {
			t.Errorf("trace-backward exit = %d", code)
		}
	})
	if strings.Contains(out, "t.A (9)") {
		t.Error("默认 trace-backward 不应跨函数间接写")
	}

	out = captureStdout(func() {
		if code := cmdQuery([]string{"trace-backward", "example.com/m.T.A", "--func", "outer", "--repo", dir, "--follow-indirect"}); code != 0 {
			t.Errorf("trace-backward --follow-indirect exit = %d", code)
		}
	})
	for _, want := range []string{"t.A (9)", "v"} {
		if !strings.Contains(out, want) {
			t.Errorf("--follow-indirect 输出缺 %q:\n%s", want, out)
		}
	}
}
