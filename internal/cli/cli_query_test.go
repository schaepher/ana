package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
)

func TestQuerySymbol(t *testing.T) {
	dir := seedRepo(t)
	if code := cmdQuery([]string{"symbol", "main", "--repo", dir}); code != 0 {
		t.Errorf("query symbol main = %d, want 0", code)
	}

	if code := cmdQuery([]string{"symbol", "nope_nope", "--repo", dir}); code == 0 {
		t.Error("query unknown symbol should fail")
	}
}
func TestQueryCalleesCallers(t *testing.T) {
	dir := seedRepo(t)
	if code := cmdQuery([]string{"callees", "main", "--repo", dir}); code != 0 {
		t.Errorf("query callees = %d, want 0", code)
	}
	if code := cmdQuery([]string{"callers", "symbol:go:example.com/m/svc:(Svc).Run", "--repo", dir}); code != 0 {
		t.Errorf("query callers = %d, want 0", code)
	}

	if code := cmdQuery([]string{"callees"}); code != 2 {
		t.Errorf("callees without symbol = %d, want 2", code)
	}

	if code := cmdQuery([]string{}); code != 2 {
		t.Errorf("query without subcommand = %d, want 2", code)
	}
}
func TestQueryNoRepo(t *testing.T) {

	if code := cmdQuery([]string{"symbol", "main", "--repo", filepath.Join(t.TempDir(), "nope")}); code != 1 {
		t.Errorf("query with bad repo = %d, want 1", code)
	}
}
func TestQueryFields(t *testing.T) {
	dir := seedFieldTrace(t)
	out := captureStdout(func() {
		if code := cmdQuery([]string{"fields", "main", "--repo", dir}); code != 0 {
			t.Errorf("query fields exit = %d", code)
		}
	})
	for _, want := range []string{"[direct_read]", "[direct_write]", "example.com/m.T.A", "t.A = v"} {
		if !strings.Contains(out, want) {
			t.Errorf("query fields output missing %q:\n%s", want, out)
		}
	}
}

// TestQuerySymbolCandidates：接口类型 symbol 详情展示候选实现
// （Q95：candidates + 置信度 + 注册点）。
func TestQuerySymbolCandidates(t *testing.T) {
	dir := seedRepo(t)
	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	r := sqlite.NewRepo(db)

	ifaceID := "symbol:go:example.com/m/svc:Handler"
	implID := "symbol:go:example.com/m/svc:(Svc).Run"
	r.SaveBatchStats([]*domain.CodeEntity{
		{ID: domain.CanonicalID(ifaceID), Kind: domain.KindInterface, Name: "Handler", FilePath: "svc/svc.go", LineStart: 3},
	}, []*domain.Fact{{
		SourceID: domain.CanonicalID(ifaceID), TargetID: domain.CanonicalID(implID),
		Kind: domain.FactDispatchTo, ToolSource: domain.ToolSSA, Confidence: 0.9,
		Metadata: map[string]any{"origin": "register", "interface_method": "Handle",
			"register_site": float64(5), "confidence": 0.9},
	}}, nil)

	out := captureStdout(func() {
		if code := cmdQuery([]string{"symbol", ifaceID, "--repo", dir}); code != 0 {
			t.Errorf("query symbol iface exit = %d", code)
		}
	})
	for _, want := range []string{"候选实现", "(Svc).Run", "0.9"} {
		if !strings.Contains(out, want) {
			t.Errorf("symbol 候选实现输出缺 %q:\n%s", want, out)
		}
	}

	out = captureStdout(func() {
		if code := cmdQuery([]string{"symbol", ifaceID, "--repo", dir, "--json"}); code != 0 {
			t.Errorf("query symbol iface --json exit = %d", code)
		}
	})
	if !strings.Contains(out, `"candidates"`) {
		t.Errorf("symbol --json 应含 candidates:\n%s", out)
	}
}

// TestQueryFieldsCallSite：indirect_write 摘要展示调用点（Q90 调用点级回连）：
// INDIRECT_WRITE 边 metadata 的调用点行号与实参变量名出现在 fields 输出。
func TestQueryFieldsCallSite(t *testing.T) {
	dir := seedFieldTrace(t)
	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	r := sqlite.NewRepo(db)

	funcID := "symbol:go:example.com/m:main"
	calleeID := "symbol:go:example.com/m/svc:(Svc).Run"
	r.SaveBatchStats(nil, nil, []*domain.FunctionFieldSummary{
		{FunctionID: domain.CanonicalID(funcID), AccessKind: domain.SummaryIndirectWrite,
			FieldPath: "example.com/m.T.A", InstancePath: "t.A", LineStart: 9, CodeSnippet: "t.A = v"},
	})
	if _, err := r.SaveBatchStats(nil, []*domain.Fact{{
		SourceID: domain.CanonicalID(funcID), TargetID: domain.CanonicalID(calleeID),
		Kind: domain.FactIndirectWrite, ToolSource: domain.ToolSSA, Confidence: 1,
		Metadata: map[string]any{"call_line": float64(16), "call_args": "t"},
	}}, nil); err != nil {
		t.Fatalf("save indirect edge: %v", err)
	}
	out := captureStdout(func() {
		if code := cmdQuery([]string{"fields", "main", "--repo", dir}); code != 0 {
			t.Errorf("query fields exit = %d", code)
		}
	})
	for _, want := range []string{"调用点", "16", "t"} {
		if !strings.Contains(out, want) {
			t.Errorf("query fields 输出应含调用点信息 %q:\n%s", want, out)
		}
	}
}
