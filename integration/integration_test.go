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
	"github.com/schaepher/codeintel/internal/action"
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

import "database/sql"

type Service struct {
	Name string
}

type Handler interface {
	Handle() string
}

func (s *Service) Handle() string {
	s.Name = "x"
	s.helper()
	return s.Name
}

func (s *Service) helper() {}

// 全局溯源场景（Q98）：全局变量跨函数共享节点
var DefaultService = Service{Name: "default"}

func defaultServiceName() string {
	return DefaultService.Name
}

// 持久化场景（Q97）：SQL 写操作 → 字段→表.列 映射
func saveService(db *sql.DB, s *Service) {
	db.Exec("INSERT INTO services(name) VALUES(?)", s.Name)
}

// 动态派发场景（Q91）：接口值调用 + 注册点（&Service{} 经 MakeInterface 传入）
func invoke(h Handler) {
	h.Handle()
}

func dispatchMain() {
	invoke(&Service{})
}

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

// 嵌套字段读链场景（fieldAddrUse 传播）：newLLM 读 m.cfg.APIKey——
// 读链中间层 m.cfg 应为 read，不误报 write、不污染调用者的间接写
type Config struct {
	APIKey  string
	BaseURL string
}

type Manager struct {
	cfg Config
}

func NewManager(cfg Config) *Manager {
	return &Manager{cfg: cfg}
}

func newLLM(m *Manager) {
	if m.cfg.APIKey == "" {
		return
	}
	_ = m.cfg.BaseURL
}

func runNested() {
	m := NewManager(Config{APIKey: "x"})
	newLLM(m)
}

// 字面量与数组场景：[]T{...} 字面量初始化（lifting 无源码位置）不产
// 元素节点；真数组变量 a[0]（有源码位置）保留
type Option struct{ V int }

func opts() {
	_ = []Option{{V: 1}, {V: 2}}
}

func arr() {
	var a [3]int
	a[0] = 1
	_ = a[0]
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

// fieldAccessID 查函数内指定 instance_path + access_kind 的字段访问节点 ID
// （value-trace 锚点用；行号不硬编码，避免 fixture 行号漂移）。
func fieldAccessID(t *testing.T, repo *sqlite.Repo, funcID, instance, access string) string {
	t.Helper()
	rows, err := repo.Query(`SELECT id FROM nodes
		WHERE kind = 'field_access'
		  AND json_extract(properties, '$.func_id') = ?
		  AND json_extract(properties, '$.instance_path') = ?
		  AND json_extract(properties, '$.access_kind') = ?
		LIMIT 1`, funcID, instance, access)
	if err != nil {
		t.Fatalf("fieldAccessID: %v", err)
	}
	defer rows.Close()
	var id string
	if rows.Next() {
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan fieldAccessID: %v", err)
		}
		return id
	}
	return ""
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
	// 动态派发（Q91）：fixture 的 Handler 接口 → (Service).Handle 实现——
	// dispatch_to 边存在且 expand 返回（白名单）
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
	//    调用点级回连（Q90）：run 的间接写展示调用点（run 调 fillParam 的行）
	if !strings.Contains(out, "调用点") {
		t.Errorf("run 间接写应展示调用点信息，output=%q", out)
	}
	//    持久化识别（Q97）：saveService 的 SQL 写 → 字段→表.列 映射
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

	// 6. 嵌套字段读链（fieldAddrUse 传播，radar m.cfg.APIKey 场景固化为
	//    fixture）：newLLM 读 m.cfg.APIKey——读链中间层 m.cfg 是 read，
	//    不误报 write；runNested 无 Manager.cfg 间接写（newLLM 只读）
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

	// 7. lifting 字面量噪音：[]T{...} 初始化不产元素节点；真数组 a[0] 保留
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

	// 8. value-trace 穿层：从 newLLM 的 m.cfg.APIKey 读节点出发，反向
	//    链应穿过嵌套字段层与函数边界：
	//    newLLM.m ← argument ← runNested.m ← returns ← NewManager
	llmReadID := fieldAccessID(t, repo, llmID, "m.cfg.APIKey", "read")
	if llmReadID == "" {
		t.Fatalf("newLLM m.cfg.APIKey read node missing")
	}
	code, out = runCLIOut(t, "query", "value-trace", llmReadID, "--repo", dir)
	if code != 0 {
		t.Errorf("query value-trace exit = %d", code)
	}
	for _, want := range []string{"argument", "returns", "runNested", "NewManager", "m.cfg.APIKey"} {
		if !strings.Contains(out, want) {
			t.Errorf("value-trace 输出缺 %q（追溯链应穿层跨函数），output=%q", want, out)
		}
	}

	// 9. 条件标注（Q92）：newLLM 的 m.cfg.APIKey 读在 if 分支内——
	//    trace 输出带 [条件: ...]
	llmCondID := fieldAccessID(t, repo, llmID, "m.cfg.APIKey", "read")
	if llmCondID == "" {
		t.Fatalf("newLLM m.cfg.APIKey read node missing")
	}
	code, out = runCLIOut(t, "query", "trace-backward", "example.com/app/svc.Config.APIKey",
		"--func", llmID, "--repo", dir)
	if code != 0 {
		t.Errorf("trace-backward 条件 exit = %d", code)
	}
	if !strings.Contains(out, "[条件:") {
		t.Errorf("trace 输出应含条件标注 [条件:...]，output=%q", out[:min(len(out), 200)])
	}

	// 10. 跨层摘要（Q100）：saveService 字段主链（nameNode 复用第 8 节）
	code, out = runCLIOut(t, "query", "summary", nameNode, "--repo", dir)
	if code != 0 {
		t.Errorf("query summary exit = %d", code)
	}
	for _, want := range []string{"[entry]", "s.Name"} {
		if !strings.Contains(out, want) {
			t.Errorf("summary 输出缺 %q，output=%q", want, out[:min(len(out), 200)])
		}
	}

	// 11. 全局溯源（Q98）：DefaultService.Name 读 → 反向可达 var.DefaultService
	gfName := fieldAccessID(t, repo, "symbol:go:example.com/app/svc:defaultServiceName", "DefaultService.Name", "read")
	if gfName == "" {
		t.Fatalf("defaultServiceName read node missing")
	}
	code, out = runCLIOut(t, "query", "value-trace", gfName, "--repo", dir)
	if code != 0 {
		t.Errorf("value-trace 全局 exit = %d", code)
	}
	if !strings.Contains(out, "DefaultService") {
		t.Errorf("value-trace 应显示全局节点 DefaultService（溯源链），output=%q", out[:min(len(out), 200)])
	}

	// 12. lifecycle 导出（Q99）：export graph --type lifecycle
	code, out = runCLIOut(t, "export", "graph", "--type", "lifecycle", "--target", nameNode, "--repo", dir)
	if code != 0 {
		t.Errorf("export graph lifecycle exit = %d", code)
	}
	if !strings.Contains(out, "flowchart") {
		t.Errorf("lifecycle 应输出 flowchart，output=%q", out[:min(len(out), 200)])
	}

	// 13. symbol 接口候选展示（Q95）：svc.Handler 详情含候选实现
	code, out = runCLIOut(t, "query", "symbol", "symbol:go:example.com/app/svc:Handler", "--repo", dir)
	if code != 0 {
		t.Errorf("symbol Handler exit = %d", code)
	}
	if !strings.Contains(out, "候选实现") {
		t.Errorf("symbol Handler 应展示候选实现，output=%q", out[:min(len(out), 200)])
	}

	// 14. trace-forward 跨函数（问题①）：从 run 出发（run 内无 Cfg.Key
	//     直接访问）应经 argument 进入 callee fillParam 的实际写入
	code, out = runCLIOut(t, "query", "trace-forward", "example.com/app/svc.Cfg.Key",
		"--func", "symbol:go:example.com/app/svc:run", "--repo", dir)
	if code != 0 {
		t.Errorf("trace-forward 跨函数 exit = %d", code)
	}
	if !strings.Contains(out, "c.Key") {
		t.Errorf("trace-forward 应从 run 经 argument 进入 fillParam 的 c.Key 写入，output=%q", out[:min(len(out), 200)])
	}

	// 15. summary 写锚点下游（③）：从 s.Name 写节点出发应含使用链
	//     （同字段读节点 → 返回消费）
	handleWrite := fieldAccessID(t, repo, "symbol:go:example.com/app/svc:(Service).Handle", "s.Name", "write")
	if handleWrite == "" {
		t.Fatalf("Handle s.Name write node missing")
	}
	code, out = runCLIOut(t, "query", "summary", handleWrite, "--repo", dir)
	if code != 0 {
		t.Errorf("summary 写锚点 exit = %d", code)
	}
	if !strings.Contains(out, "consume") {
		t.Errorf("summary 写锚点应含下游使用链（consume 读节点），output=%q", out[:min(len(out), 400)])
	}

	// 16. clean 删除索引
	if code := runCLI(t, "clean", "--repo", dir, "--force"); code != 0 {
		t.Fatalf("clean exit = %d", code)
	}
	if _, err := os.Stat(filepath.Join(dir, ".codeintel")); !os.IsNotExist(err) {
		t.Error(".codeintel should be removed after clean")
	}
}

// TestIncrementalUpdate：init → 修改文件 → update → 新符号出现、
// 旧符号保留、被删除符号消失（TD.md 5.2 增量语义）。
func TestIncrementalUpdate(t *testing.T) {
	if !scipGoAvailable() {
		t.Skip("scip-go not found (install: go install github.com/scip-code/scip-go/cmd/scip-go@latest)")
	}
	dir := fixtureRepo(t)
	// git 初始化并提交（增量检测依赖 git）
	gitRun(t, dir, "init", "-q")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "init")
	if code := runCLI(t, "init", "--repo", dir); code != 0 {
		t.Fatalf("init exit = %d", code)
	}

	// 1. 修改 svc.go：新增 newFunc、删除 aliasLocal（无调用者，删除后
	//    其 alias 边与符号应消失；run 的 fillParam 闭包摘要应保留）
	svcPath := filepath.Join(dir, "svc", "svc.go")
	data, err := os.ReadFile(svcPath)
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(string(data), "func aliasLocal() {\n\ta := &Cfg{}\n\tb := a\n\tb.Key = \"y\"\n}\n",
		"", 1)
	updated += "\nfunc newFunc() int { return 42 }\n"
	writeFile(t, svcPath, updated)

	// 2. 增量更新
	if code := runCLI(t, "update", "--repo", dir); code != 0 {
		t.Fatalf("update exit = %d", code)
	}

	// 3. 验证
	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	repo := sqlite.NewRepo(db)

	// 新增符号可查
	if _, err := repo.GetSymbol("symbol:go:example.com/app/svc:newFunc"); err != nil {
		t.Errorf("newFunc 应出现在增量后索引: %v", err)
	}
	// 未变更文件的符号保留
	if _, err := repo.GetSymbol("symbol:go:example.com/app/svc:(Service).Handle"); err != nil {
		t.Errorf("未变更符号应保留: %v", err)
	}
	if _, err := repo.GetSymbol("symbol:go:example.com/app:main"); err != nil {
		t.Errorf("main 应保留: %v", err)
	}
	// 被删除函数的符号与摘要消失
	if _, err := repo.GetSymbol("symbol:go:example.com/app/svc:aliasLocal"); err == nil {
		t.Error("aliasLocal 应从索引消失")
	}
	aliasFields, err := repo.GetFunctionFields("symbol:go:example.com/app/svc:aliasLocal")
	if err != nil || len(aliasFields) > 0 {
		t.Errorf("aliasLocal 摘要应清空: %v, %v", aliasFields, err)
	}
	// 变更文件内未删除符号的字段追溯仍完整（fillM 的 map 元素写）
	fillMRows, err := repo.GetFunctionFields("symbol:go:example.com/app/svc:fillM")
	if err != nil || len(fillMRows) == 0 {
		t.Errorf("fillM 摘要应保留（变更文件内重新索引）: %v, %v", fillMRows, err)
	}
	// 未变更数据仍完整：run 的间接写（fillParam 别名命中）保留
	runRows, err := repo.GetFunctionFields("symbol:go:example.com/app/svc:run")
	if err != nil {
		t.Fatalf("GetFunctionFields run: %v", err)
	}
	keyHit := false
	for _, s := range runRows {
		if s.AccessKind == domain.SummaryIndirectWrite && strings.Contains(s.FieldPath, "Cfg.Key") {
			keyHit = true
		}
	}
	if !keyHit {
		t.Errorf("run 的间接写摘要（跨文件闭包）应保留: %+v", runRows)
	}
	// build_metadata 标记 incremental
	meta, err := repo.GetLatest()
	if err != nil {
		t.Fatalf("GetLatest: %v", err)
	}
	if meta.ToolName != "incremental" {
		t.Errorf("build tool_name = %s, want incremental", meta.ToolName)
	}
}

// gitRun 在指定目录执行 git（注入 user 配置供 commit 使用）。
func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// TestServerEndToEnd：init 后起 HTTP serve（真实前端资源），全 API 验证。
// TestOutputNoiseFree：真实仓库 init 后——stdout 无日志混流（日志入
// .codeintel/codeintel.log）、--json 可解析、--compact 生效、export graph
// 输出 mermaid/dot（Q88/Q89/Q96）。
func TestOutputNoiseFree(t *testing.T) {
	if !scipGoAvailable() {
		t.Skip("scip-go not found (install: go install github.com/scip-code/scip-go/cmd/scip-go@latest)")
	}
	dir := fixtureRepo(t)

	// init 输出 stdout 不含 OTel span 日志
	code, out := runCLIOut(t, "init", "--repo", dir)
	if code != 0 {
		t.Fatalf("init exit = %d", code)
	}
	if strings.Contains(out, `"Name": "codeintel.main"`) {
		t.Errorf("init stdout 不应混入 OTel span 日志")
	}
	// 日志文件落位（与 db 同目录，Q88）
	logPath := filepath.Join(dir, ".codeintel", "codeintel.log")
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("日志文件缺失: %v", err)
	}
	// 测试入口（cli.Main 直调）不处理 --verbose（main() 扫描并 Setup），
	// debug 级日志写文件在 radar 实测验证（真实二进制路径）；此处验证
	// 核心契约：日志文件存在且 stdout 无日志混流（默认 info 级下
	// entrylog 的 enter/exit 为 debug 不产生输出，stdout 干净即证明
	// 日志通道已切文件）
	code, out = runCLIOut(t, "query", "symbol", "main", "--repo", dir)
	if code != 0 {
		t.Fatalf("query exit = %d", code)
	}
	if strings.Contains(out, "enter cmdQuery") || strings.Contains(out, "DEBUG") ||
		strings.Contains(out, `"Name": "codeintel.main"`) {
		t.Errorf("日志不应出现在 stdout: %s", out[:min(len(out), 200)])
	}

	// --json：stdout 为纯 JSON
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

	// export graph：callees → dot；value-trace → mermaid
	handleID := "symbol:go:example.com/app/svc:(Service).Handle"
	code, out = runCLIOut(t, "export", "graph", "--type", "callees", "--target", handleID, "--repo", dir)
	if code != 0 {
		t.Fatalf("export graph callees exit = %d", code)
	}
	if !strings.Contains(out, "digraph") {
		t.Errorf("callees graph 应输出 dot: %s", out[:min(len(out), 200)])
	}
	// value-trace 锚点：Handle 的 s.Name 读节点
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

// mustOpen 打开仓库 DB（测试辅助）。
func mustOpen(t *testing.T, dir string) *sqlite.DB {
	t.Helper()
	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// min 返回较小值（Go 1.21 无内置 min）。
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

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
	srv := server.New(context.Background(), action.New(sqlite.NewRepo(db)), webFS, dir)
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
			case "parameter", "receiver": // receiver 为独立 kind（与参数区分）
				paramNode = true
			case "result":
				resultNode = true
			}
		}
	}
	if !paramNode || !resultNode {
		t.Errorf("expand handle neighbors = %v, want parameter/receiver+result nodes", m["neighbors"])
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
