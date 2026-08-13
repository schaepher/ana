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

type Service struct{}

type Handler interface {
	Handle()
}

func (s *Service) Handle() {
	s.helper()
}

func (s *Service) helper() {}
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
	// 构建状态：不允许 failed（joern 缺失时为 degraded）
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
