package ssa

import (
	"path/filepath"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

func TestBuiltinSummaryUnmarshalWrite(t *testing.T) {
	nodes, facts, summaries := indexFixtureFull(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

import "encoding/json"

type T struct {
	A int
	B string
}

func f(data []byte, t *T) error {
	return json.Unmarshal(data, t)
}
`,
	})
	// 虚拟写节点：t.A / t.B（is_external，func_id=f）
	var vA, vB *domain.CodeEntity
	for _, n := range nodes {
		if n.Kind != domain.KindFieldAccess || n.Property("is_external") != "true" {
			continue
		}
		switch n.Property("full_path") {
		case "example.com/mtest.T.A":
			vA = n
		case "example.com/mtest.T.B":
			vB = n
		}
	}
	if vA == nil || vB == nil {
		t.Fatalf("external field nodes missing: %+v", nodes)
	}
	for _, v := range []*domain.CodeEntity{vA, vB} {
		if v.Property("access_kind") != "write" || v.Property("func_id") != "symbol:go:example.com/mtest:f" {
			t.Errorf("external node = %+v", v)
		}
	}
	// summary_io 边：external_summary → 虚拟节点
	extID := "symbol:go:encoding/json:Unmarshal"
	for _, v := range []*domain.CodeEntity{vA, vB} {
		findFact(t, facts, extID, string(v.ID), string(domain.FactSummaryIO))
		// INDIRECT_WRITE：f → 虚拟节点
		findFact(t, facts, "symbol:go:example.com/mtest:f", string(v.ID), string(domain.FactIndirectWrite))
	}
	// 摘要表：f 的间接写含 T.A/T.B
	findSummary(t, summaries, "symbol:go:example.com/mtest:f", domain.SummaryIndirectWrite, "example.com/mtest.T.A")
	findSummary(t, summaries, "symbol:go:example.com/mtest:f", domain.SummaryIndirectWrite, "example.com/mtest.T.B")
}

func TestBuiltinSummaryPrintfRead(t *testing.T) {
	nodes, facts, _ := indexFixtureFull(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

import "fmt"

type T struct {
	A int
}

func f(t *T) {
	fmt.Printf("%v", t)
}
`,
	})
	// 虚拟读节点：t.A（read）
	var vA *domain.CodeEntity
	for _, n := range nodes {
		if n.Kind == domain.KindFieldAccess && n.Property("is_external") == "true" &&
			n.Property("full_path") == "example.com/mtest.T.A" {
			vA = n
		}
	}
	if vA == nil {
		t.Fatal("external read node missing")
	}
	if vA.Property("access_kind") != "read" {
		t.Errorf("access = %q, want read", vA.Property("access_kind"))
	}
	// 读摘要：虚拟节点 → 实参（data_flows_to）
	out := factsFrom(facts, string(vA.ID))
	if len(out) != 1 || out[0].Kind != domain.FactDataFlowsTo {
		t.Errorf("read virtual edges = %+v", out)
	}
	// 读摘要不产生 INDIRECT_WRITE
	for _, f := range facts {
		if f.Kind == domain.FactIndirectWrite && string(f.TargetID) == string(vA.ID) {
			t.Error("read summary must not produce INDIRECT_WRITE")
		}
	}
}

func TestBuiltinSummaryNestedAllFields(t *testing.T) {
	// 递归展开：内嵌 struct 的字段也生成虚拟节点
	nodes, _, _ := indexFixtureFull(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

import "encoding/json"

type Inner struct {
	X int
}

type Outer struct {
	A int
	In Inner
}

func f(data []byte, o *Outer) error {
	return json.Unmarshal(data, o)
}
`,
	})
	found := map[string]bool{}
	for _, n := range nodes {
		if n.Kind == domain.KindFieldAccess && n.Property("is_external") == "true" {
			found[n.Property("full_path")] = true
		}
	}
	for _, want := range []string{"example.com/mtest.Outer.A", "example.com/mtest.Outer.In", "example.com/mtest.Inner.X"} {
		if !found[want] {
			t.Errorf("nested external field %s missing (have %v)", want, found)
		}
	}
}

func TestLoadSummariesUserYAML(t *testing.T) {
	dir := t.TempDir()
	// 1. 正常解析
	writeFile(t, filepath.Join(dir, "field-summary.yaml"), `
summaries:
  - func: "example.com/foo.Bar"
    reads: ["user.ID"]
    writes: ["user.Name"]
    param_index: 1
`)
	specs, warnings := loadSummaries(dir)
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v", warnings)
	}
	spec, ok := specs["example.com/foo.Bar"]
	if !ok || len(spec.Reads) != 1 || spec.Reads[0] != "user.ID" ||
		len(spec.Writes) != 1 || spec.Writes[0] != "user.Name" || spec.ParamIndex != 1 {
		t.Errorf("user spec = %+v", spec)
	}
	// 内置仍在
	if _, ok := specs["encoding/json.Unmarshal"]; !ok {
		t.Error("builtin summaries missing")
	}

	// 2. 用户覆盖内置（同名）
	writeFile(t, filepath.Join(dir, "field-summary.yaml"), `
summaries:
  - func: "encoding/json.Unmarshal"
    writes: ["v.ID"]
    param_index: 1
`)
	specs, _ = loadSummaries(dir)
	if spec := specs["encoding/json.Unmarshal"]; len(spec.Writes) != 1 || spec.Writes[0] != "v.ID" {
		t.Errorf("override spec = %+v", spec)
	}

	// 3. 重复定义 → 警告，忽略后续
	writeFile(t, filepath.Join(dir, "field-summary.yaml"), `
summaries:
  - func: "example.com/foo.Bar"
    reads: ["a"]
  - func: "example.com/foo.Bar"
    reads: ["b"]
`)
	specs, warnings = loadSummaries(dir)
	if len(warnings) == 0 {
		t.Error("duplicate should warn")
	}
	if spec := specs["example.com/foo.Bar"]; len(spec.Reads) != 1 || spec.Reads[0] != "a" {
		t.Errorf("duplicate spec = %+v", spec)
	}

	// 4. 坏 YAML → 警告降级，内置可用
	writeFile(t, filepath.Join(dir, "field-summary.yaml"), "summaries: [broken")
	specs, warnings = loadSummaries(dir)
	if len(warnings) == 0 {
		t.Error("bad yaml should warn")
	}
	if _, ok := specs["encoding/json.Unmarshal"]; !ok {
		t.Error("builtin lost after bad yaml")
	}
}

func TestSummaryKey(t *testing.T) {
	// summaryKey 对方法生成 pkg.(T).Method 形式（值/指针接收者归一）
	_, facts, _ := indexFixtureFull(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

import "database/sql"

func f(rows *sql.Rows, dest *struct{ A int }) error {
	return rows.Scan(dest)
}
`,
	})
	// Rows.Scan 摘要（WritesAll）应生成虚拟写节点（dest 的字段）
	ext := false
	for _, f := range facts {
		if f.Kind == domain.FactSummaryIO {
			ext = true
		}
	}
	if !ext {
		t.Error("Rows.Scan summary not applied (summaryKey mismatch?)")
	}
	_ = facts
}

// TestBuiltinSummaryMetrics：prometheus 观测函数内置摘要（Q99 观测指标识别）。
func TestBuiltinSummaryMetrics(t *testing.T) {
	specs := builtinSummaries()
	for _, fn := range []string{
		"github.com/prometheus/client_golang/prometheus.(Counter).Inc",
		"github.com/prometheus/client_golang/prometheus.(CounterVec).WithLabelValues",
		"github.com/prometheus/client_golang/prometheus.(Histogram).Observe",
		"github.com/prometheus/client_golang/prometheus.(Gauge).Set",
	} {
		if _, ok := specs[fn]; !ok {
			t.Errorf("内置摘要缺观测函数: %s", fn)
		}
	}
}

// TestBuiltinSummaryGORM：GORM 写操作内置摘要（②：ORM 更新映射字段→列）。
func TestBuiltinSummaryGORM(t *testing.T) {
	specs := builtinSummaries()
	for _, fn := range []string{
		"gorm.io/gorm.(DB).Create",
		"gorm.io/gorm.(DB).Save",
		"gorm.io/gorm.(DB).Updates",
		"gorm.io/gorm.(DB).Delete",
		"gorm.io/gorm.(DB).Update",
	} {
		if _, ok := specs[fn]; !ok {
			t.Errorf("内置摘要缺 GORM 写函数: %s", fn)
		}
	}
}

// TestSnakeCase：表名/列名转换——与 GORM 默认命名完全一致（缩写表
// Title 化 + 大小写扫描：SessionID → session_id、SourceURL → source_url、
// SQLiteKnowledgeGraph → sq_lite_knowledge_graph）。
func TestSnakeCase(t *testing.T) {
	cases := map[string]string{
		"UserProfile":            "user_profile",
		"APIKey":                 "api_key",
		"Name":                   "name",
		"HTTPServer":             "http_server",
		"ID":                     "id",
		"SQLiteKnowledgeGraph":   "sq_lite_knowledge_graph",
		"SQLiteKnowledgeGraphID": "sq_lite_knowledge_graph_id",
		"ChatMessage":            "chat_message",
		"SessionID":              "session_id",
		"SourceURL":              "source_url",
		"SessionIDAndURL":        "session_id_and_url",
	}
	for in, want := range cases {
		if got := snakeCase(in); got != want {
			t.Errorf("snakeCase(%q) = %q, want %q", in, got, want)
		}
	}
}
