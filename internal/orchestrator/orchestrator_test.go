package orchestrator

import (
	"context"

	"github.com/schaepher/codeintel/internal/domain"
	"golang.org/x/tools/go/packages"
)

// TestFullBuildAndQuery 端到端：临时 Go 模块 → 全量构建 → 校验图数据。
// 需要 scip-go 在 PATH（或 go bin）。

// TestDiscoverModules：P2-3——递归扫描 go.mod（根在前）；跳过
// .git/.codeintel/vendor；module 目录内不再嵌套扫描。

// TestFullBuildMultiModule：P2-3 端到端——双 go.mod monorepo：
// 根 module 的 main 调用子 module（app/）的函数，跨 module calls 边成立。

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

// TestInjectChangedFiles：P1-1——runAdapters 对实现 SetChangedFiles 的适配器
// 注入变更文件（增量）；全量构建注入 nil（每次运行重置，防残留）。

// TestFlushFKRetry：跨批 FK 冲突（P2）——边批先于节点批落库时外键冲突，
// flush 收集失败边不静默丢弃；构建尾部全部节点落库后重试 → 边不丢。
