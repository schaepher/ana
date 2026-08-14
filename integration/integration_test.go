//go:build integration

// Package integration 端到端集成测试：真实仓库 → CLI init → SQLite 查询 →
// HTTP serve → clean 全流程（需要 scip-go；缺失时跳过）。
// 运行：make it（= go test -count=1 -tags integration ./integration/）
package integration

import (
	"context"
	"encoding/json"
	"io"
	iofs "io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/schaepher/codeintel/assets"
	"github.com/schaepher/codeintel/internal/cli"
	"github.com/schaepher/codeintel/internal/domain"
	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
	"github.com/schaepher/codeintel/internal/server"
)

// scipGoAvailable：scip-go 在 PATH 或 GOBIN/GOPATH/bin 中可用。
func scipGoAvailable() bool {
	if _, err := exec.LookPath("scip-go"); err == nil {
		return true
	}
	for _, envVar := range []string{"GOBIN", "GOPATH"} {
		out, err := exec.Command("go", "env", envVar).Output()
		if err != nil {
			continue
		}
		dir := strings.TrimSpace(string(out))
		if dir == "" {
			continue
		}
		p := filepath.Join(dir, "scip-go")
		if envVar == "GOPATH" {
			p = filepath.Join(dir, "bin", "scip-go")
		}
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return true
		}
	}
	return false
}

// fixtureRepo 建真实 Go 模块仓库（覆盖：跨包方法调用/接口/回调/普通函数）。
func fixtureRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/app\n\ngo 1.21\n")
	writeFile(t, filepath.Join(dir, "main.go"), `package main

import "example.com/app/svc"

func main() {
	s := &svc.Service{}
	s.Handle()
}

func greet(name string) string {
	return "hi " + name
}
`)
	writeFile(t, filepath.Join(dir, "handler.go"), `package main

import "net/http"

func handler(w http.ResponseWriter, r *http.Request) {}

func setup() {
	http.HandleFunc("/x", handler)
}
`)
	writeFile(t, filepath.Join(dir, "svc", "svc.go"), `package svc

type Service struct {
	Name string
}

type Handler interface {
	Handle()
}

func (s *Service) Handle() string {
	s.Name = "x"
	s.helper()
	return s.Name
}

func (s *Service) helper() {}

// 别名分析场景（Q80）：fillParam 写实参；fillLocal 写自己创建的对象
type Cfg struct {
	Key   string
	Local string
}

func fillParam(c *Cfg) {
	c.Key = "x"
}

func fillLocal() {
	c := &Cfg{}
	c.Local = "x"
}

func run(c *Cfg) {
	fillParam(c)
	fillLocal()
}

func aliasLocal() {
	a := &Cfg{}
	b := a
	b.Key = "y"
}

// map/slice 元素场景（Q83）：fillM 写实参容器元素 → useMap 间接写
type M map[string]int

func fillM(m M) {
	m["a"] = 1
}

func useMap() {
	m := M{}
	fillM(m)
	s := make([]int, 3)
	s[0] = 2
}
`)
	return dir
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// runCLI 以完整命令行入口跑 CLI，返回退出码。
func runCLI(t *testing.T, args ...string) int {
	t.Helper()
	return cli.Main(context.Background(), args)
}

// runCLIOut 同 runCLI，额外捕获 stdout（CLI 输出断言用）。
func runCLIOut(t *testing.T, args ...string) (int, string) {
	t.Helper()
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	code := cli.Main(context.Background(), args)
	w.Close()
	os.Stdout = old
	var buf strings.Builder
	if _, err := io.Copy(&buf, r); err != nil {
		return code, ""
	}
	return code, buf.String()
}

// TestCLIFullFlow：init → DB 内容验证 → query → clean 全流程。
func TestCLIFullFlow(t *testing.T) {
	if !scipGoAvailable() {
		t.Skip("scip-go not found (install: go install github.com/scip-code/scip-go/cmd/scip-go@latest)")
	}
	dir := fixtureRepo(t)

	// 1. init 构建索引
	if code := runCLI(t, "init", "--repo", dir); code != 0 {
		t.Fatalf("init exit = %d, want 0", code)
	}
	if _, err := os.Stat(filepath.Join(dir, ".codeintel", "codeintel.db")); err != nil {
		t.Fatalf("index db missing: %v", err)
	}

	// 2. DB 内容验证
	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	repo := sqlite.NewRepo(db)

	// 符号（SCIP 权威）
	sym, err := repo.GetSymbol("symbol:go:example.com/app:main")
	if err != nil || sym.Kind != domain.KindFunction {
		t.Errorf("main symbol = %+v, err %v", sym, err)
	}
	if _, err := repo.GetSymbol("symbol:go:example.com/app/svc:(Service).Handle"); err != nil {
		t.Errorf("Handle symbol: %v", err)
	}
	// 调用链：main → (Service).Handle
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
	// 方法线：Service → Handle
	if _, _, err := repo.Expand("symbol:go:example.com/app/svc:Service"); err != nil {
		t.Errorf("expand Service: %v", err)
	}
	// 外部包层：setup → net/http:HandleFunc → handler（持有参数）
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
	// 顶层入口：main
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
	// 构建状态：不允许 failed（适配器失败时为 degraded）
	meta, err := repo.GetLatest()
	if err != nil {
		t.Fatalf("GetLatest: %v", err)
	}
	if meta.Status == domain.BuildFailed {
		t.Errorf("build status = %s, want success/degraded (%s)", meta.Status, meta.ErrorMsg)
	}

	// 3. query 子命令
	if code := runCLI(t, "query", "symbol", "main", "--repo", dir); code != 0 {
		t.Errorf("query symbol exit = %d", code)
	}
	if code := runCLI(t, "query", "callees", "main", "--repo", dir); code != 0 {
		t.Errorf("query callees exit = %d", code)
	}
	if code := runCLI(t, "query", "callers", "symbol:go:example.com/app/svc:(Service).Handle", "--repo", dir); code != 0 {
		t.Errorf("query callers exit = %d", code)
	}
	// 深度遍历（修复后的递归 CTE）
	if code := runCLI(t, "query", "impact", "main", "--repo", dir, "--depth", "3"); code != 0 {
		t.Errorf("query impact exit = %d", code)
	}

	// 4. 字段追溯命令（SSA 字段追溯，field_trace.md）
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

	// 5. 轻量别名分析（Q80，docs/field_trace.md §14.8）：
	//    run 调 fillParam（写实参 → 间接写 Key）+ fillLocal（写内部对象，
	//    无别名 → 不得计入 Local）
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
	//    map/slice 元素追踪（Q83）：fillM 直接写元素；useMap 经 fillM 间接写
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
	//    aliasLocal 内 b := a 别名同一 alloc → expand 返回 alias 边
	aliasFacts, _, err := repo.Expand("symbol:go:example.com/app/svc:aliasLocal")
	if err != nil {
		t.Fatalf("expand aliasLocal: %v", err)
	}
	aliasHit := false
	for _, f := range aliasFacts {
		if f.Kind == domain.FactAlias {
			aliasHit = true
		}
	}
	if !aliasHit {
		t.Errorf("expand aliasLocal 应返回 alias 边（b 与 a 别名同一 alloc）: %+v", aliasFacts)
	}

	// 4. clean 删除索引
	if code := runCLI(t, "clean", "--repo", dir, "--force"); code != 0 {
		t.Fatalf("clean exit = %d", code)
	}
	if _, err := os.Stat(filepath.Join(dir, ".codeintel")); !os.IsNotExist(err) {
		t.Error(".codeintel should be removed after clean")
	}
}

// TestServerEndToEnd：init 后起 HTTP serve（真实前端资源），全 API 验证。
func TestServerEndToEnd(t *testing.T) {
	if !scipGoAvailable() {
		t.Skip("scip-go not found (install: go install github.com/scip-code/scip-go/cmd/scip-go@latest)")
	}
	dir := fixtureRepo(t)
	if code := runCLI(t, "init", "--repo", dir); code != 0 {
		t.Fatalf("init exit = %d", code)
	}
	handleID := "symbol:go:example.com/app/svc:(Service).Handle"

	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	// 与 cli/serve.go 一致：取 web/ 子目录作为 FS 根（否则 embed 根目录
	// 无法 Open("/")）
	webFS, err := iofs.Sub(assets.WebFS, "web")
	if err != nil {
		t.Fatalf("fs.Sub: %v", err)
	}
	srv := server.New(context.Background(), sqlite.NewRepo(db), webFS, dir)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	getJSON := func(path string) (int, map[string]any) {
		t.Helper()
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer resp.Body.Close()
		var m map[string]any
		json.NewDecoder(resp.Body).Decode(&m)
		return resp.StatusCode, m
	}

	// 前端页面（真实 embed 资源）
	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("index status = %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if !strings.Contains(string(body), "codeintel") {
		t.Errorf("index content missing codeintel")
	}

	// /api/roots：main 入口
	code, m := getJSON("/api/roots")
	if code != 200 {
		t.Fatalf("roots status = %d", code)
	}
	nodes, _ := m["nodes"].([]any)
	if len(nodes) == 0 {
		t.Fatal("roots empty")
	}
	var rootID string
	for _, raw := range nodes {
		n := raw.(map[string]any)
		if n["name"] == "main" {
			rootID = n["id"].(string)
		}
	}
	if rootID == "" {
		t.Fatalf("roots missing main: %v", nodes)
	}

	// /api/expand：main 的邻居（含 (Service).Handle）
	code, m = getJSON("/api/expand?id=" + rootID)
	if code != 200 {
		t.Fatalf("expand status = %d", code)
	}
	edges, _ := m["edges"].([]any)
	edgeHit := false
	for _, raw := range edges {
		e := raw.(map[string]any)
		if strings.Contains(e["target"].(string), "(Service).Handle") && e["direction"] == "out" {
			edgeHit = true
		}
	}
	if !edgeHit {
		t.Errorf("expand edges = %v, want (Service).Handle out edge", edges)
	}

	// /api/search
	if code, _ := getJSON("/api/search"); code != 400 {
		t.Errorf("search without q = %d, want 400", code)
	}
	code, m = getJSON("/api/search?q=greet")
	if code != 200 {
		t.Fatalf("search status = %d", code)
	}
	if nodes, _ := m["nodes"].([]any); len(nodes) != 1 {
		t.Errorf("search greet = %v", nodes)
	}

	// /api/source：真实源码
	code, m = getJSON("/api/source?id=symbol:go:example.com/app:greet")
	if code != 200 {
		t.Fatalf("source status = %d", code)
	}
	src, _ := m["code"].(string)
	if !strings.Contains(src, "func greet") || !strings.Contains(src, "hi ") {
		t.Errorf("source = %q", src)
	}
	// 外部包节点展开：HandleFunc → handler（持有参数）
	code, m = getJSON("/api/expand?id=symbol:go:net/http:HandleFunc")
	if code != 200 {
		t.Fatalf("expand HandleFunc status = %d", code)
	}

	// 参数/返回节点展开（has_param / has_result）
	code, m = getJSON("/api/expand?id=" + handleID)
	if code != 200 {
		t.Fatalf("expand handle status = %d", code)
	}
	edges, _ = m["edges"].([]any)
	paramHit, resultHit := false, false
	paramNode, resultNode := false, false
	for _, raw := range edges {
		e := raw.(map[string]any)
		if e["kind"] == "has_param" {
			paramHit = true
		}
		if e["kind"] == "has_result" {
			resultHit = true
		}
	}
	if !paramHit || !resultHit {
		t.Errorf("expand handle edges = %v, want has_param+has_result", edges)
	}
	if nodes, _ := m["neighbors"].([]any); len(nodes) > 0 {
		for _, raw := range nodes {
			n := raw.(map[string]any)
			switch n["kind"] {
			case "parameter":
				paramNode = true
			case "result":
				resultNode = true
			}
		}
	}
	if !paramNode || !resultNode {
		t.Errorf("expand handle neighbors = %v, want parameter+result nodes", m["neighbors"])
	}

	// /api/flows：函数内字段数据流
	code, m = getJSON("/api/flows?id=" + handleID)
	if code != 200 {
		t.Fatalf("flows status = %d", code)
	}
	if flows, _ := m["flows"].([]any); len(flows) == 0 {
		t.Errorf("flows empty for %s", handleID)
	}
	code, m = getJSON("/api/expand?id=symbol:go:net/http:HandleFunc")
	if code != 200 {
		t.Fatalf("expand HandleFunc status = %d", code)
	}
	edges, _ = m["edges"].([]any)
	passHit := false
	for _, raw := range edges {
		e := raw.(map[string]any)
		if e["kind"] == "passes_to" && strings.Contains(e["target"].(string), ":handler") {
			passHit = true
		}
	}
	if !passHit {
		t.Errorf("HandleFunc expand = %v, want passes_to handler", edges)
	}
}
