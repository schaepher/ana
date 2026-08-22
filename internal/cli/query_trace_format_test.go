package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
)

// Q235-10 value-trace 四格式输出：文本（分组：写入值/对象/来源/去向 +
// 源码片段 + 短锚点）/ --format tree（ASCII 树）/ --format mermaid /
// --json。fixture 含真实 main.go 供源码片段读取。

const vtFormatSrc = `package m

type T struct {
	Brands []string
	ID     int
}

func makeItem() *T { return &T{} }

func makeBrands() []string { return []string{"a"} }

func main() {
	u := makeItem()        // 28
	brands := makeBrands() // 29
	u.Brands = brands      // 30
	_ = u.Brands[0]        // 31
}
`

func seedValueTraceFormat(t *testing.T) string {
	t.Helper()
	dir := seedRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(vtFormatSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	r := sqlite.NewRepo(db)
	funcID := "symbol:go:example.com/m:main"
	writeNode := &domain.CodeEntity{ID: domain.CanonicalID(funcID + "#u.Brands.write@15"),
		Kind: domain.KindFieldAccess, Name: "u.Brands", FilePath: "main.go", LineStart: 15,
		Properties: map[string]any{"full_path": "example.com/m.T.Brands", "instance_path": "u.Brands",
			"access_kind": "write", "func_id": funcID}}
	readNode := &domain.CodeEntity{ID: domain.CanonicalID(funcID + "#u.Brands.read@16"),
		Kind: domain.KindFieldAccess, Name: "u.Brands", FilePath: "main.go", LineStart: 16,
		Properties: map[string]any{"full_path": "example.com/m.T.Brands", "instance_path": "u.Brands",
			"access_kind": "read", "func_id": funcID}}
	uNode := &domain.CodeEntity{ID: domain.CanonicalID(funcID + "#t1"), Kind: domain.KindSSAValue,
		Name: "u", FilePath: "main.go", LineStart: 13, Properties: map[string]any{"func_id": funcID}}
	brandsNode := &domain.CodeEntity{ID: domain.CanonicalID(funcID + "#t3"), Kind: domain.KindSSAValue,
		Name: "brands", FilePath: "main.go", LineStart: 14, Properties: map[string]any{"func_id": funcID}}
	if _, err := r.SaveBatchStats([]*domain.CodeEntity{writeNode, readNode, uNode, brandsNode}, []*domain.Fact{
		{SourceID: uNode.ID, TargetID: writeNode.ID, Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: brandsNode.ID, TargetID: writeNode.ID, Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: writeNode.ID, TargetID: readNode.ID, Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
	}, nil); err != nil {
		t.Fatalf("save: %v", err)
	}
	return dir
}

// TestValueTraceFormatCompositeLiteral：§71/§73 遗留——复合字面量
// 键值对锚点分组：u := &T{ Email: req.Email, }（多行字面量，锚点行
// 无等号）——右侧来源 req.Email 应归「写入值」，而非退化全「来源」。
// splitAssign 扩展：无等号时识别 "Key: value," 冒号形态。
const vtCompositeSrc = `package m

type T struct {
	Brands []string
	ID     int
	Email  string
}

type Req struct {
	Email string
}

func main() {
	req := Req{Email: "a"} // 14
	u := &T{                // 15
		Email: req.Email, // 16
	}                       // 17
	_ = u.Email             // 18
}
`

func seedValueTraceComposite(t *testing.T) string {
	t.Helper()
	dir := seedRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(vtCompositeSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	r := sqlite.NewRepo(db)
	funcID := "symbol:go:example.com/m:main"
	writeNode := &domain.CodeEntity{ID: domain.CanonicalID(funcID + "#u.Email.write@16"),
		Kind: domain.KindFieldAccess, Name: "u.Email", FilePath: "main.go", LineStart: 16,
		Properties: map[string]any{"full_path": "example.com/m.T.Email", "instance_path": "u.Email",
			"access_kind": "write", "func_id": funcID}}
	reqNode := &domain.CodeEntity{ID: domain.CanonicalID(funcID + "#req.Email.read@16"),
		Kind: domain.KindFieldAccess, Name: "req.Email", FilePath: "main.go", LineStart: 16,
		Properties: map[string]any{"full_path": "example.com/m.Req.Email", "instance_path": "req.Email",
			"access_kind": "read", "func_id": funcID}}
	if _, err := r.SaveBatchStats([]*domain.CodeEntity{writeNode, reqNode}, []*domain.Fact{
		{SourceID: reqNode.ID, TargetID: writeNode.ID, Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
	}, nil); err != nil {
		t.Fatalf("save: %v", err)
	}
	return dir
}

func TestValueTraceFormatCompositeLiteral(t *testing.T) {
	dir := seedValueTraceComposite(t)
	out := captureStdout(func() {
		if code := cmdQuery([]string{"value-trace", "symbol:go:example.com/m:main#u.Email.write@16",
			"--repo", dir}); code != 0 {
			t.Errorf("value-trace exit = %d", code)
		}
	})
	if !strings.Contains(out, "写入值") {
		t.Errorf("复合字面量锚点应归「写入值」组（当前退化全来源）:\n%s", out)
	}
	if !strings.Contains(out, "req.Email") {
		t.Errorf("写入值组应含 req.Email 来源:\n%s", out)
	}
}

func TestValueTraceFormatCompositeLiteralTree(t *testing.T) {
	dir := seedValueTraceComposite(t)
	out := captureStdout(func() {
		if code := cmdQuery([]string{"value-trace", "symbol:go:example.com/m:main#u.Email.write@16",
			"--repo", dir, "--format", "tree"}); code != 0 {
			t.Errorf("value-trace tree exit = %d", code)
		}
	})
	if !strings.Contains(out, "写入值") {
		t.Errorf("tree 输出复合字面量锚点应归「写入值」:\n%s", out)
	}
}

// TestValueTraceFormatText：文本格式——短锚点、写入值/对象/去向分组、
// 源码片段。
func TestValueTraceFormatText(t *testing.T) {
	dir := seedValueTraceFormat(t)
	out := captureStdout(func() {
		if code := cmdQuery([]string{"value-trace", "symbol:go:example.com/m:main#u.Brands.write@15",
			"--repo", dir}); code != 0 {
			t.Errorf("value-trace exit = %d", code)
		}
	})
	for _, want := range []string{"值流:", "写入值", "对象", "去向", "u.Brands = brands", "brands := makeBrands()"} {
		if !strings.Contains(out, want) {
			t.Errorf("文本输出缺 %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "symbol:go:example.com/m:main#u.Brands.write@30\n") {
		t.Errorf("锚点应短名化（不打印完整 canonical ID 行）:\n%s", out)
	}
}

// TestValueTraceFormatTree：--format tree——ASCII 树形（├─/└─）。
func TestValueTraceFormatTree(t *testing.T) {
	dir := seedValueTraceFormat(t)
	out := captureStdout(func() {
		if code := cmdQuery([]string{"value-trace", "symbol:go:example.com/m:main#u.Brands.write@15",
			"--repo", dir, "--format", "tree"}); code != 0 {
			t.Errorf("value-trace --format tree exit = %d", code)
		}
	})
	for _, want := range []string{"├─", "└─", "写入值", "对象", "去向"} {
		if !strings.Contains(out, want) {
			t.Errorf("tree 输出缺 %q:\n%s", want, out)
		}
	}
}

// TestValueTraceFormatMermaid：--format mermaid——flowchart。
func TestValueTraceFormatMermaid(t *testing.T) {
	dir := seedValueTraceFormat(t)
	out := captureStdout(func() {
		if code := cmdQuery([]string{"value-trace", "symbol:go:example.com/m:main#u.Brands.write@15",
			"--repo", dir, "--format", "mermaid"}); code != 0 {
			t.Errorf("value-trace --format mermaid exit = %d", code)
		}
	})
	for _, want := range []string{"flowchart", "u.Brands [写]:15", "brands:14", "u:13", "u.Brands:16"} {
		if !strings.Contains(out, want) {
			t.Errorf("mermaid 输出缺 %q:\n%s", want, out)
		}
	}
}

// TestValueTraceFormatJSON：--json 保持可解析（flows 数组）。
func TestValueTraceFormatJSON(t *testing.T) {
	dir := seedValueTraceFormat(t)
	out := captureStdout(func() {
		if code := cmdQuery([]string{"value-trace", "symbol:go:example.com/m:main#u.Brands.write@15",
			"--repo", dir, "--json"}); code != 0 {
			t.Errorf("value-trace --json exit = %d", code)
		}
	})
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("json: %v\n%s", err, out)
	}
	if _, ok := m["flows"]; !ok {
		t.Errorf("json 缺 flows 字段: %v", m)
	}
}
