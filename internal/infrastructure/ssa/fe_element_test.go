package ssa

import (
	"strings"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

func TestElementLiteralInitFiltered(t *testing.T) {

	nodes, _ := indexFixture(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

type Option struct{ V int }

func f() {
	opts := []Option{{V: 1}, {V: 2}}
	_ = opts
}
`,
	})
	funcID := "symbol:go:example.com/mtest:f"
	for _, n := range nodes {
		if n.Kind == domain.KindFieldAccess && n.Property("func_id") == funcID &&
			strings.Contains(n.Property("instance_path"), "[") {
			t.Errorf("字面量初始化不应产元素节点: %v", n.Property("instance_path"))
		}
	}
}
func TestElementArrayVarKept(t *testing.T) {

	nodes, _ := indexFixture(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

func f() {
	var a [3]int
	a[0] = 1
	_ = a[0]
}
`,
	})
	funcID := "symbol:go:example.com/mtest:f"
	findFieldByPath(t, nodes, funcID, `a[0]`)
	findFieldAccess(t, nodes, funcID, `a[0]`, "read")
}

// TestAnonymousStructFieldAccessHasLine：B3 回归——匿名 struct（range 元素
// 等）的字段访问须有行号与文件（fieldInfo 的匿名分支曾提前 return，
// line_start=0 导致 CLI 无定位、前端无锚点）。
func TestAnonymousStructFieldAccessHasLine(t *testing.T) {
	nodes, _ := indexFixture(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

type Conf struct {
	Items []struct {
		Key string
	}
}

func f(c Conf) {
	for _, s := range c.Items {
		_ = s.Key
	}
}
`,
	})
	funcID := "symbol:go:example.com/mtest:f"
	var keyNode *domain.CodeEntity
	for _, n := range nodes {
		if n.Kind != domain.KindFieldAccess || n.Property("func_id") != funcID {
			continue
		}
		if n.Property("instance_path") == "s.Key" && n.Property("access_kind") == "read" {
			keyNode = n
			break
		}
	}
	if keyNode == nil {
		t.Fatal("s.Key 读节点未生成（匿名 struct 字段访问丢失）")
	}
	if keyNode.LineStart <= 0 {
		t.Errorf("匿名 struct 字段访问 line_start = %d, want > 0", keyNode.LineStart)
	}
	if keyNode.FilePath != "main.go" {
		t.Errorf("匿名 struct 字段访问 file = %q, want main.go", keyNode.FilePath)
	}
}
