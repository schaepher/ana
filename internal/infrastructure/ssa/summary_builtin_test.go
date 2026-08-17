package ssa

import (
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

	extID := "symbol:go:encoding/json:Unmarshal"
	for _, v := range []*domain.CodeEntity{vA, vB} {
		findFact(t, facts, extID, string(v.ID), string(domain.FactSummaryIO))

		findFact(t, facts, "symbol:go:example.com/mtest:f", string(v.ID), string(domain.FactIndirectWrite))
	}

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

	out := factsFrom(facts, string(vA.ID))
	if len(out) != 1 || out[0].Kind != domain.FactDataFlowsTo {
		t.Errorf("read virtual edges = %+v", out)
	}

	for _, f := range facts {
		if f.Kind == domain.FactIndirectWrite && string(f.TargetID) == string(vA.ID) {
			t.Error("read summary must not produce INDIRECT_WRITE")
		}
	}
}
func TestBuiltinSummaryNestedAllFields(t *testing.T) {

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
func TestSummaryKey(t *testing.T) {

	_, facts, _ := indexFixtureFull(t, map[string]string{
		"go.mod": moduleGoMod,
		"main.go": `package m

import "database/sql"

type Dest struct{ A int }

func f(rows *sql.Rows, dest *Dest) error {
	return rows.Scan(dest)
}
`,
	})

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
