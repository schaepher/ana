package ssa

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
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
	dir := t.TempDir()
	for path, content := range files {
		writeFile(t, filepath.Join(dir, path), content)
	}
	var nodes []*domain.CodeEntity
	var facts []*domain.Fact
	adapter := &Adapter{}
	repo := &domain.Repository{Path: dir, Module: "example.com/mtest"}
	err := adapter.Index(context.Background(), repo, func(item domain.Item) error {
		if item.Node != nil {
			nodes = append(nodes, item.Node)
		}
		if item.Fact != nil {
			facts = append(facts, item.Fact)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	return nodes, facts
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
