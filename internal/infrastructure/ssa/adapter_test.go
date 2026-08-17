package ssa

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"strings"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
	"golang.org/x/tools/go/packages"
)

// writeFile 写入测试模块文件。
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// indexFixture 在临时目录构建 Go 模块并跑 ssa.Adapter.Index，收集全部产出。
func indexFixture(t *testing.T, files map[string]string) ([]*domain.CodeEntity, []*domain.Fact) {
	t.Helper()
	nodes, facts, _ := indexFixtureFull(t, files)
	return nodes, facts
}

// indexFixtureFull 同 indexFixture，额外收集函数字段摘要行。
func indexFixtureFull(t *testing.T, files map[string]string) ([]*domain.CodeEntity, []*domain.Fact, []*domain.FunctionFieldSummary) {
	nodes, facts, summaries, _ := indexFixtureFullOrigins(t, files)
	return nodes, facts, summaries
}

// indexFixtureFullOrigins 同 indexFixtureFull，额外收集 Q161 origins
// （emitSummaries 发射的 Item.Origins）。
func indexFixtureFullOrigins(t *testing.T, files map[string]string) ([]*domain.CodeEntity, []*domain.Fact, []*domain.FunctionFieldSummary, []*domain.SummaryOrigin) {
	t.Helper()
	dir := t.TempDir()
	for path, content := range files {
		writeFile(t, filepath.Join(dir, path), content)
	}
	var nodes []*domain.CodeEntity
	var facts []*domain.Fact
	var summaries []*domain.FunctionFieldSummary
	var origins []*domain.SummaryOrigin
	// Q169：emit 回调并发安全（按包并发后多 goroutine 同时调 emit）
	var emitMu sync.Mutex
	adapter := &Adapter{}
	repo := &domain.Repository{Path: dir, Module: "example.com/mtest", Modules: []string{"example.com/mtest"}}
	pkgs, err := loadTestPackages(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	err = adapter.Index(context.Background(), repo, pkgs, func(item domain.Item) error {
		emitMu.Lock()
		defer emitMu.Unlock()
		if item.Node != nil {
			nodes = append(nodes, item.Node)
		}
		if item.Fact != nil {
			facts = append(facts, item.Fact)
		}
		if item.Summary != nil {
			summaries = append(summaries, item.Summary)
		}
		if item.Origins != nil {
			origins = append(origins, item.Origins...)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	return nodes, facts, summaries, origins
}

// findNode 按 ID 查找节点。
func findNode(t *testing.T, nodes []*domain.CodeEntity, id string) *domain.CodeEntity {
	t.Helper()
	for _, n := range nodes {
		if string(n.ID) == id {
			return n
		}
	}
	t.Fatalf("node not found: %s", id)
	return nil
}

// 基础模块：函数 + 方法 + 闭包 + 外部调用。
const baseModule = `
module example.com/mtest

go 1.26
`

const baseMain = `package main

type Svc struct{}

func (s *Svc) Handle(req string) error {
	return nil
}

func main() {
	fn := func() {}
	fn()
	println("hi")
}
`

func TestIndexFunctions(t *testing.T) {
	nodes, _ := indexFixture(t, map[string]string{
		"go.mod": baseModule,
		"main.go": baseMain,
	})

	// 函数节点：symbol:go:example.com/mtest:main
	fn := findNode(t, nodes, "symbol:go:example.com/mtest:main")
	if fn.Kind != domain.KindFunction {
		t.Errorf("main kind = %s, want function", fn.Kind)
	}
	if fn.FilePath != "main.go" {
		t.Errorf("main file = %q", fn.FilePath)
	}
	if fn.LineStart != 9 {
		t.Errorf("main line = %d, want 9", fn.LineStart)
	}
	if !strings.Contains(fn.Property("signature"), "func main()") {
		t.Errorf("main signature = %q", fn.Property("signature"))
	}

	// 方法节点：方法统一 (T).method（值/指针接收者不区分）
	m := findNode(t, nodes, "symbol:go:example.com/mtest:(Svc).Handle")
	if m.Kind != domain.KindMethod {
		t.Errorf("Handle kind = %s, want method", m.Kind)
	}
	if !strings.Contains(m.Property("signature"), "func (*Svc).Handle(req string) error") {
		t.Errorf("Handle signature = %q", m.Property("signature"))
	}

	// 包节点不由本适配器产出（SCIP/AST 负责）
	for _, n := range nodes {
		if n.Kind == domain.KindPackage {
			t.Errorf("ssa adapter must not emit package nodes: %s", n.ID)
		}
	}
}

func TestIndexSkipsClosuresAndExternal(t *testing.T) {
	nodes, _ := indexFixture(t, map[string]string{
		"go.mod":  baseModule,
		"main.go": baseMain,
	})

	// 闭包（fn := func(){}）不建节点：sp.Members 只有顶层声明
	for _, n := range nodes {
		if strings.Contains(string(n.ID), "$") {
			t.Errorf("closure node should not exist: %s", n.ID)
		}
	}
	// 外部依赖（fmt 等）不产出节点
	for _, n := range nodes {
		if strings.HasPrefix(string(n.ID), "symbol:go:fmt") {
			t.Errorf("external node should not exist: %s", n.ID)
		}
	}
}

func TestIndexStructMethodValueReceiver(t *testing.T) {
	// 值接收者方法同样归一为 (T).m
	nodes, _ := indexFixture(t, map[string]string{
		"go.mod": baseModule,
		"main.go": `package main

type Svc struct{}

func (s Svc) Get() int { return 0 }

func main() {}
`,
	})
	m := findNode(t, nodes, "symbol:go:example.com/mtest:(Svc).Get")
	if m.Kind != domain.KindMethod {
		t.Errorf("Get kind = %s, want method", m.Kind)
	}
}

func TestSignatureNodes(t *testing.T) {
	nodes, facts := indexFixture(t, map[string]string{
		"go.mod":  moduleGoMod,
		"main.go": `package m

type T struct {
	A int
}

func (s *T) Handle(req string, n int) (int, error) {
	return n, nil
}

func plain(x bool) bool {
	return x
}
`,
	})
	mID := "symbol:go:example.com/mtest:(T).Handle"
	fID := "symbol:go:example.com/mtest:plain"

	// 方法：接收者（独立 kind=receiver）+ 2 参数（has_param 边），
	// 2 个返回（has_result 边，索引后缀）
	recv := findNode(t, nodes, mID+"#param.recv.s")
	if recv.Kind != domain.KindReceiver || recv.Property("receiver") != "true" {
		t.Errorf("receiver node = %+v", recv)
	}
	if recv.Property("type_string") != "*example.com/mtest.T" {
		t.Errorf("receiver type = %q", recv.Property("type_string"))
	}
	findNode(t, nodes, mID+"#param.req")
	findNode(t, nodes, mID+"#param.n")
	r0 := findNode(t, nodes, mID+"#result.0")
	if r0.Kind != domain.KindResult || r0.Property("type_string") != "int" {
		t.Errorf("result.0 = %+v", r0)
	}
	findNode(t, nodes, mID+"#result.1")

	findFact(t, facts, mID, mID+"#param.recv.s", string(domain.FactHasParam))
	findFact(t, facts, mID, mID+"#result.0", string(domain.FactHasResult))
	if findFact(t, facts, mID, mID+"#param.n", string(domain.FactHasParam)).TargetID != domain.CanonicalID(mID+"#param.n") {
		t.Error("has_param edge for n wrong")
	}

	// 普通函数：单返回无索引后缀
	p := findNode(t, nodes, fID+"#param.x")
	if p.Kind != domain.KindParameter || p.Property("receiver") != "" {
		t.Errorf("plain param = %+v", p)
	}
	findNode(t, nodes, fID+"#result")
}

// loadTestPackages 加载测试仓库的 packages（共享加载改造后由测试提供）。
func loadTestPackages(dir string) ([]*packages.Package, error) {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedSyntax |
			packages.NeedTypes | packages.NeedTypesInfo | packages.NeedImports | packages.NeedDeps,
		Dir: dir,
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return nil, err
	}
	return pkgs, nil
}
