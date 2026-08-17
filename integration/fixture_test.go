//go:build integration

package integration

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/schaepher/codeintel/internal/cli"
	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
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

// gitRun 在指定目录执行 git（注入 user 配置供 commit 使用）。
func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
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
