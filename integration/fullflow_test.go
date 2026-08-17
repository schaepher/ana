//go:build integration

package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
)

// TestCLIFullFlow：init → DB 内容验证 → query → clean 全流程。
func TestCLIFullFlowPart1(t *testing.T) {
	if !scipGoAvailable() {
		t.Skip("scip-go not found (install: go install github.com/scip-code/scip-go/cmd/scip-go@latest)")
	}
	dir := fixtureRepo(t)

	if code := runCLI(t, "init", "--repo", dir); code != 0 {
		t.Fatalf("init exit = %d, want 0", code)
	}
	if _, err := os.Stat(filepath.Join(dir, ".codeintel", "codeintel.db")); err != nil {
		t.Fatalf("index db missing: %v", err)
	}

	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	repo := sqlite.NewRepo(db)

	sym, err := repo.GetSymbol("symbol:go:example.com/app:main")
	if err != nil || sym.Kind != domain.KindFunction {
		t.Errorf("main symbol = %+v, err %v", sym, err)
	}
	if _, err := repo.GetSymbol("symbol:go:example.com/app/svc:(Service).Handle"); err != nil {
		t.Errorf("Handle symbol: %v", err)
	}

	callees, err := repo.GetCallees("symbol:go:example.com/app:main", 1, 0.8)
	if err != nil {
		t.Fatalf("GetCallees: %v", err)
	}
	hit := false
	for _, f := range callees {
		if f.TargetID == "symbol:go:example.com/app/svc:(Service).Handle" {
			hit = true
		}
	}
	if !hit {
		t.Errorf("main callees = %+v, want (Service).Handle", callees)
	}

	if _, _, err := repo.Expand("symbol:go:example.com/app/svc:Service"); err != nil {
		t.Errorf("expand Service: %v", err)
	}

	dispatchFacts, _, err := repo.Expand("symbol:go:example.com/app/svc:Handler")
	if err != nil {
		t.Fatalf("expand Handler: %v", err)
	}
	dispatchHit := false
	for _, f := range dispatchFacts {
		if f.Kind == domain.FactDispatchTo && string(f.TargetID) == "symbol:go:example.com/app/svc:(Service).Handle" {
			dispatchHit = true
		}
	}
	if !dispatchHit {
		t.Errorf("expand Handler 应返回 dispatch_to 边（Handler → (Service).Handle）: %+v", dispatchFacts)
	}

	extFacts, _, err := repo.Expand("symbol:go:example.com/app:setup")
	if err != nil {
		t.Fatalf("expand setup: %v", err)
	}
	extHit := false
	for _, f := range extFacts {
		if string(f.TargetID) == "symbol:go:net/http:HandleFunc" && f.Kind == domain.FactCalls {
			extHit = true
		}
	}
	if !extHit {
		t.Errorf("setup expand = %+v, want external HandleFunc edge", extFacts)
	}

	roots, err := repo.GetRoots()
	if err != nil {
		t.Fatalf("GetRoots: %v", err)
	}
	rootHit := false
	for _, n := range roots {
		if n.ID == "symbol:go:example.com/app:main" {
			rootHit = true
		}
	}
	if !rootHit {
		t.Errorf("roots = %+v, want main", roots)
	}

	meta, err := repo.GetLatest()
	if err != nil {
		t.Fatalf("GetLatest: %v", err)
	}
	if meta.Status == domain.BuildFailed {
		t.Errorf("build status = %s, want success/degraded (%s)", meta.Status, meta.ErrorMsg)
	}

	if code := runCLI(t, "query", "symbol", "main", "--repo", dir); code != 0 {
		t.Errorf("query symbol exit = %d", code)
	}
	if code := runCLI(t, "query", "callees", "main", "--repo", dir); code != 0 {
		t.Errorf("query callees exit = %d", code)
	}
	if code := runCLI(t, "query", "callers", "symbol:go:example.com/app/svc:(Service).Handle", "--repo", dir); code != 0 {
		t.Errorf("query callers exit = %d", code)
	}

	if code := runCLI(t, "query", "impact", "main", "--repo", dir, "--depth", "3"); code != 0 {
		t.Errorf("query impact exit = %d", code)
	}

	handleID := "symbol:go:example.com/app/svc:(Service).Handle"
	code, out := runCLIOut(t, "query", "fields", handleID, "--repo", dir)
	if code != 0 {
		t.Errorf("query fields exit = %d", code)
	}
	if !strings.Contains(out, "direct_write") || !strings.Contains(out, "example.com/app/svc.Service.Name") {
		t.Errorf("query fields output = %q", out)
	}
	if code := runCLI(t, "query", "trace-backward", "example.com/app/svc.Service.Name",
		"--func", handleID, "--repo", dir); code != 0 {
		t.Errorf("trace-backward exit = %d", code)
	}
	if code := runCLI(t, "query", "trace-forward", "example.com/app/svc.Service.Name",
		"--func", handleID, "--repo", dir); code != 0 {
		t.Errorf("trace-forward exit = %d", code)
	}
	exportPath := filepath.Join(dir, "analysis.json")
	if code := runCLI(t, "export", "--repo", dir, "--out", exportPath); code != 0 {
		t.Errorf("export exit = %d", code)
	}
	if _, err := os.Stat(exportPath); err != nil {
		t.Errorf("export file missing: %v", err)
	}

	runID := "symbol:go:example.com/app/svc:run"
	code, out = runCLIOut(t, "query", "fields", runID, "--repo", dir)
	if code != 0 {
		t.Errorf("query fields run exit = %d", code)
	}
	if !strings.Contains(out, "svc.Cfg.Key") {
		t.Errorf("run 间接写应含 Cfg.Key（fillParam 别名命中），output=%q", out)
	}
	if strings.Contains(out, "svc.Cfg.Local") {
		t.Errorf("run 间接写不应含 Cfg.Local（fillLocal 写内部对象，无别名），output=%q", out)
	}

	if !strings.Contains(out, "调用点") {
		t.Errorf("run 间接写应展示调用点信息，output=%q", out)
	}

	saveID := "symbol:go:example.com/app/svc:saveService"
	nameNode := fieldAccessID(t, repo, saveID, "s.Name", "read")
	if nameNode == "" {
		t.Fatalf("saveService s.Name read node missing")
	}
	code, out = runCLIOut(t, "query", "value-trace", nameNode, "--repo", dir)
	if code != 0 {
		t.Errorf("value-trace saveService exit = %d", code)
	}
	if !strings.Contains(out, "services.name") {
		t.Errorf("value-trace 应显示持久化映射 services.name，output=%q", out)
	}

	code, out = runCLIOut(t, "query", "fields", "symbol:go:example.com/app/svc:fillM", "--repo", dir)
	if code != 0 {
		t.Errorf("query fields fillM exit = %d", code)
	}
	if !strings.Contains(out, `svc.M["a"]`) {
		t.Errorf("fillM direct_write 应含元素路径 svc.M[\"a\"]，output=%q", out)
	}
	code, out = runCLIOut(t, "query", "fields", "symbol:go:example.com/app/svc:useMap", "--repo", dir)
	if code != 0 {
		t.Errorf("query fields useMap exit = %d", code)
	}
	if !strings.Contains(out, `svc.M["a"]`) {
		t.Errorf("useMap 间接写应含元素路径 svc.M[\"a\"]，output=%q", out)
	}
	if !strings.Contains(out, `s[0]`) {
		t.Errorf("useMap direct_write 应含 slice 元素 s[0]，output=%q", out)
	}

	aliasFacts, _, err := repo.Expand("symbol:go:example.com/app/svc:aliasLocal")
	if err != nil {
		t.Fatalf("expand aliasLocal: %v", err)
	}
	for _, f := range aliasFacts {
		if f.Kind == domain.FactAlias {
			t.Errorf("expand 函数节点不应返回 alias 边（B1 后 alias 挂值节点）: %+v", f)
		}
	}
	valFacts, _, err := repo.Expand("symbol:go:example.com/app/svc:aliasLocal#t0")
	if err != nil {
		t.Fatalf("expand aliasLocal#t0: %v", err)
	}
	aliasHit := false
	for _, f := range valFacts {
		if f.Kind == domain.FactAlias {
			aliasHit = true
		}
	}
	if !aliasHit {
		t.Errorf("expand 值节点应返回 alias 边（b 与 a 别名同一 alloc）: %+v", valFacts)
	}

	llmID := "symbol:go:example.com/app/svc:newLLM"
	code, out = runCLIOut(t, "query", "fields", llmID, "--repo", dir)
	if code != 0 {
		t.Errorf("query fields newLLM exit = %d", code)
	}
	llmRows, err := repo.GetFunctionFields(domain.CanonicalID(llmID))
	if err != nil {
		t.Fatalf("GetFunctionFields newLLM: %v", err)
	}
	llmReadCfg, llmReadKey, llmWriteCfg := false, false, false
	for _, s := range llmRows {
		switch {
		case s.AccessKind == domain.SummaryDirectRead && strings.Contains(s.FieldPath, "Manager.cfg"):
			llmReadCfg = true
		case s.AccessKind == domain.SummaryDirectRead && strings.Contains(s.FieldPath, "Config.APIKey"):
			llmReadKey = true
		case s.AccessKind == domain.SummaryDirectWrite && strings.Contains(s.FieldPath, "Manager.cfg"):
			llmWriteCfg = true
		}
	}
	if !llmReadCfg || !llmReadKey {
		t.Errorf("newLLM 应读 Manager.cfg（内层）与 Config.APIKey，rows=%+v", llmRows)
	}
	if llmWriteCfg {
		t.Errorf("newLLM 读链中间层 Manager.cfg 不应标 write（污染间接写闭包）")
	}
	if !strings.Contains(out, "Config.APIKey") {
		t.Errorf("query fields newLLM output = %q", out)
	}
	code, out = runCLIOut(t, "query", "fields", "symbol:go:example.com/app/svc:runNested", "--repo", dir)
	if code != 0 {
		t.Errorf("query fields runNested exit = %d", code)
	}
	runRows, err := repo.GetFunctionFields("symbol:go:example.com/app/svc:runNested")
	if err != nil {
		t.Fatalf("GetFunctionFields runNested: %v", err)
	}
	for _, s := range runRows {
		if s.AccessKind == domain.SummaryIndirectWrite && strings.Contains(s.FieldPath, "Manager.cfg") {
			t.Errorf("runNested 不应有 Manager.cfg 间接写（newLLM 只读 cfg），rows=%+v", runRows)
		}
	}

	optsRows, err := repo.GetFunctionFields("symbol:go:example.com/app/svc:opts")
	if err != nil {
		t.Fatalf("GetFunctionFields opts: %v", err)
	}
	for _, s := range optsRows {
		if strings.Contains(s.FieldPath, "opts[") {
			t.Errorf("[]T{...} 字面量初始化不应产元素路径 opts[i]，rows=%+v", optsRows)
		}
	}
	arrRows, err := repo.GetFunctionFields("symbol:go:example.com/app/svc:arr")
	if err != nil {
		t.Fatalf("GetFunctionFields arr: %v", err)
	}
	arrHit := false
	for _, s := range arrRows {
		if strings.Contains(s.FieldPath, "a[0]") {
			arrHit = true
		}
	}
	if !arrHit {
		t.Errorf("真数组变量 a[0] 应保留元素访问，rows=%+v", arrRows)
	}
}
