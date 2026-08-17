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

// findSSAValue 按 (函数, slot 前缀) 查找 ssa_value 节点（slot 用前缀匹配，SSA 临时名不稳定）。
func findSSAValue(t *testing.T, nodes []*domain.CodeEntity, funcID, slotPrefix string) *domain.CodeEntity {
	t.Helper()
	for _, n := range nodes {
		if n.Kind != domain.KindSSAValue {
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

// TestClosureWriteNodeUnit：已验场景单元测试化——闭包内字段写入节点
// 生成且 func_id 归外层函数（Q14 适配修复回归）。

// TestFuncValueCallEdgeUnit：已验场景单元测试化——函数值调用
// （f := getHandler(); f(rec)）的 argument 边。

// TestInterfaceCallEdgesUnit：已验场景单元测试化——接口动态调用
// argument + returns 边（⑮）。

// TestAliasEdgeSourceIsValueNode：B1 回归——alias 边 source 应为 ssa_value
// 值节点（funcID#slot），而非函数/方法节点。此前 funcIDOfValue 返回函数
// ID，alias 边全部错挂在函数节点上（值节点看不到别名关系）。

// TestAnonymousStructFieldAccessHasLine：B3 回归——匿名 struct（range 元素
// 等）的字段访问须有行号与文件（fieldInfo 的匿名分支曾提前 return，
// line_start=0 导致 CLI 无定位、前端无锚点）。
