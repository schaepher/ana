package ssa

import (
	"strings"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

const moduleGoMod = `
module example.com/mtest

go 1.26
`

// findFieldAccess 按 (函数, 实例路径, access_kind) 查找字段访问节点。
func findFieldAccess(t *testing.T, nodes []*domain.CodeEntity, funcID, instance, access string) *domain.CodeEntity {
	t.Helper()
	for _, n := range nodes {
		if n.Kind != domain.KindFieldAccess {
			continue
		}
		if n.Property("func_id") != funcID {
			continue
		}
		if n.Property("instance_path") != instance || n.Property("access_kind") != access {
			continue
		}
		return n
	}
	t.Fatalf("field_access not found: func=%s instance=%s access=%s", funcID, instance, access)
	return nil
}

// findSSAValue 按 (函数, slot 前缀) 查找值节点——ssa_value 或参数节点
// （Q178：参数统一用签名参数节点 #param.<name>，kind=parameter）。
// slot 用前缀匹配，SSA 临时名不稳定。
func findSSAValue(t *testing.T, nodes []*domain.CodeEntity, funcID, slotPrefix string) *domain.CodeEntity {
	t.Helper()
	for _, n := range nodes {
		if n.Kind != domain.KindSSAValue && n.Kind != domain.KindParameter {
			continue
		}
		if n.Property("func_id") != funcID {
			continue
		}
		if strings.HasPrefix(n.Name, slotPrefix) {
			return n
		}
	}
	t.Fatalf("ssa_value not found: func=%s slot~%s", funcID, slotPrefix)
	return nil
}

// factsFrom 取所有 source 为该节点的边。
func factsFrom(facts []*domain.Fact, id string) []*domain.Fact {
	var out []*domain.Fact
	for _, f := range facts {
		if string(f.SourceID) == id {
			out = append(out, f)
		}
	}
	return out
}

// nodeByID 按 ID 查找节点。
func nodeByID(t *testing.T, nodes []*domain.CodeEntity, id string) *domain.CodeEntity {
	t.Helper()
	for _, n := range nodes {
		if string(n.ID) == id {
			return n
		}
	}
	t.Fatalf("node not found: %s", id)
	return nil
}
