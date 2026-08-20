package ast

import (
	"context"
	"os"
	"path/filepath"
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

// indexFixture 在临时目录构建 Go 模块并跑 ast.Adapter.Index，收集全部产出。
func indexFixture(t *testing.T, files map[string]string) ([]*domain.CodeEntity, []*domain.Fact) {
	t.Helper()
	dir := t.TempDir()
	for path, content := range files {
		writeFile(t, filepath.Join(dir, path), content)
	}
	var nodes []*domain.CodeEntity
	var facts []*domain.Fact
	adapter := &Adapter{}
	repo := &domain.Repository{Path: dir, Module: "example.com/mtest", Modules: []string{"example.com/mtest"}}
	pkgs, err := astLoadTestPackages(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	err = adapter.Index(context.Background(), repo, pkgs, func(item domain.Item) error {
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

// findFact 按 (source, target, kind) 查找边。
func findFact(t *testing.T, facts []*domain.Fact, source, target, kind string) *domain.Fact {
	t.Helper()
	for _, f := range facts {
		if string(f.SourceID) == source && string(f.TargetID) == target && string(f.Kind) == kind {
			return f
		}
	}
	t.Fatalf("fact not found: %s -> %s [%s]", source, target, kind)
	return nil
}

func hasFact(facts []*domain.Fact, source, target, kind string) bool {
	for _, f := range facts {
		if string(f.SourceID) == source && string(f.TargetID) == target && string(f.Kind) == kind {
			return true
		}
	}
	return false
}

const fixtureGoMod = "module example.com/mtest\n\ngo 1.21\n"

// astLoadTestPackages 加载测试仓库 packages（共享加载改造后由测试提供）。
func astLoadTestPackages(dir string) ([]*packages.Package, error) {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax |
			packages.NeedTypes | packages.NeedTypesInfo | packages.NeedImports | packages.NeedDeps,
		Dir: dir,
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return nil, err
	}
	return pkgs, nil
}
