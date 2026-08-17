package ssa

import (
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

func TestFieldShadowingDisambiguated(t *testing.T) {

	nodes, _ := indexFixture(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

type T struct {
	A int
}

func s() {
	x := T{}
	{
		x := T{}
		x.A = 1
	}
	x.A = 2
}
`,
	})
	funcID := "symbol:go:example.com/mtest:s"
	var ids []string
	for _, n := range nodes {
		if n.Kind == domain.KindFieldAccess && n.Property("func_id") == funcID &&
			n.Property("instance_path") == "x.A" && n.Property("access_kind") == "write" {
			ids = append(ids, string(n.ID))
		}
	}
	if len(ids) != 2 {
		t.Fatalf("shadowed x.A writes = %d, want 2: %v", len(ids), ids)
	}
	if ids[0] == ids[1] {
		t.Errorf("shadowed accesses must be distinct, both = %s", ids[0])
	}
}
func TestFieldNestedReadPropagates(t *testing.T) {

	nodes, facts := indexFixture(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

type Config struct {
	APIKey string
}

type Manager struct {
	cfg Config
}

func newLLM(m *Manager) {
	if m.cfg.APIKey == "" {
		return
	}
	_ = m.cfg.APIKey
}
`,
	})
	funcID := "symbol:go:example.com/mtest:newLLM"

	findFieldAccess(t, nodes, funcID, "m.cfg", "read")
	ids := []string{}
	for _, n := range nodes {
		if n.Kind == domain.KindFieldAccess && n.Property("func_id") == funcID &&
			n.Property("instance_path") == "m.cfg" && n.Property("access_kind") == "write" {
			ids = append(ids, string(n.ID))
		}
	}
	if len(ids) != 0 {
		t.Errorf("m.cfg write nodes = %v, want none（读链中间层不应标写）", ids)
	}

	findFieldAccess(t, nodes, funcID, "m.cfg.APIKey", "read")

	inner := findFieldAccess(t, nodes, funcID, "m.cfg", "read")
	outer := findFieldAccess(t, nodes, funcID, "m.cfg.APIKey", "read")
	paramM := findSSAValue(t, nodes, funcID, "m")
	for _, f := range factsFrom(facts, string(paramM.ID)) {
		if f.TargetID == inner.ID {
			goto innerLinked
		}
	}
	t.Error("参数 m → 内层 m.cfg 边缺失")
innerLinked:
	for _, f := range factsFrom(facts, string(inner.ID)) {
		if f.TargetID == outer.ID {
			return
		}
	}
	t.Error("内层 m.cfg → 外层 m.cfg.APIKey 边缺失（平行 ssa_value 链应已合并）")
}
func TestFieldAnonymousStructFallback(t *testing.T) {

	nodes, _ := indexFixture(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

func f() {
	x := struct{ A int }{}
	x.A = 1
}
`,
	})
	funcID := "symbol:go:example.com/mtest:f"
	var fa *domain.CodeEntity
	for _, n := range nodes {
		if n.Kind == domain.KindFieldAccess && n.Property("func_id") == funcID {
			fa = n
		}
	}
	if fa == nil {
		t.Fatal("anonymous struct field access node missing (fallback 应产出节点)")
	}
	if fa.Property("full_path") != fa.Property("instance_path") {
		t.Errorf("fallback full_path = %q, want 回退为 instance_path %q",
			fa.Property("full_path"), fa.Property("instance_path"))
	}
	if fa.Property("access_kind") != "write" {
		t.Errorf("access = %q", fa.Property("access_kind"))
	}
}
