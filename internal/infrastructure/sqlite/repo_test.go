package sqlite

import (
	"strings"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

// newTestRepo 打开临时目录下的数据库并创建仓储。
func newTestRepo(t *testing.T) *Repo {
	t.Helper()
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return NewRepo(db)
}

// save 便捷写入节点 + 边。
func save(t *testing.T, r *Repo, nodes []*domain.CodeEntity, edges []*domain.Fact) {
	t.Helper()
	if _, err := r.SaveBatchStats(nodes, edges, nil); err != nil {
		t.Fatalf("SaveBatchStats: %v", err)
	}
}

func node(id, kind, name, file string) *domain.CodeEntity {
	return &domain.CodeEntity{
		ID:       domain.CanonicalID(id),
		Kind:     domain.EntityKind(kind),
		Name:     name,
		FilePath: file,
		Properties: map[string]any{
			"signature": "sig:" + name,
		},
	}
}

// faNode 构造 field_access 节点（properties 含 full_path/func_id）。
func faNode(id, funcID, field, instance string, line int) *domain.CodeEntity {
	return faNodeAccess(id, funcID, field, instance, line, "write")
}

func faNodeAccess(id, funcID, field, instance string, line int, access string) *domain.CodeEntity {
	return &domain.CodeEntity{
		ID: domain.CanonicalID(id), Kind: domain.KindFieldAccess, Name: instance,
		FilePath: "main.go", LineStart: line,
		Properties: map[string]any{"full_path": field, "instance_path": instance,
			"access_kind": access, "func_id": funcID},
	}
}

func svNode(id, funcID string) *domain.CodeEntity {
	return &domain.CodeEntity{
		ID: domain.CanonicalID(id), Kind: domain.KindSSAValue, Name: id[strings.LastIndex(id, "#")+1:],
		Properties: map[string]any{"func_id": funcID},
	}
}

func dfEdge(a, b domain.CanonicalID) *domain.Fact {
	return &domain.Fact{SourceID: a, TargetID: b,
		Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1}
}

// mkParamNode 构造 parameter 节点。
func mkParamNode(id, name string, index int, funcID string) *domain.CodeEntity {
	n := node(id, "parameter", name, "f.go")
	n.Properties["index"] = index
	n.Properties["func_id"] = funcID
	return n
}
