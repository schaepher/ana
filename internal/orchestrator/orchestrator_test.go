package orchestrator

import (
	"context"

	"github.com/schaepher/codeintel/internal/domain"
	"golang.org/x/tools/go/packages"
)

// mockAdapter 记录 SetChangedFiles 注入（P1-1 AST 文件级跳过），
// Index 产出固定节点避免空构建。
type mockAdapter struct {
	changed []string
}

func (m *mockAdapter) Name() string { return "mock" }

func (m *mockAdapter) Index(_ context.Context, repo *domain.Repository, _ []*packages.Package, emit domain.EmitFunc) error {
	return emit(domain.Item{Node: &domain.CodeEntity{
		ID: "symbol:go:example.com/e2e:mock", Kind: domain.KindFunction,
		Name: "mock", FilePath: "main.go",
	}})
}

func (m *mockAdapter) SetChangedFiles(files []string) { m.changed = files }
