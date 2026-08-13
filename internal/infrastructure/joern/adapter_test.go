package joern

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

func TestIndexJoernMissing(t *testing.T) {
	// JoernBinDir 指向不存在的目录 → resolveJoern 返回该目录（无校验），
	// 随后 joern-parse 执行失败 → Index 返回错误（降级路径）
	adapter := &Adapter{JoernBinDir: filepath.Join(t.TempDir(), "no-such-joern")}
	err := adapter.Index(context.Background(), &domain.Repository{Path: t.TempDir()}, func(domain.Item) error { return nil })
	if err == nil {
		t.Error("Index with missing joern should fail")
	}
}

// writeSlices 构造 joern-slice data-flow 输出的 JSON 文件。
func writeSlices(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "slices.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParseSlicesMethodIntraFlow(t *testing.T) {
	// 同一方法内的 REACHING_DEF 边 → 聚合为 properties.data_flows。
	// 注意：每条边只记录源节点 code（sink 不记录），两条边（x→y→z）
	// 才凑出 ["x", "y"] 两个片段
	path := writeSlices(t, `{
  "$type": "io.joern.dataflow.tracing.semantics.Slice",
  "nodes": [
    {"id": 1, "label": "IDENTIFIER", "name": "x", "code": "x", "parentMethod": "main.process", "parentFile": "main.go", "lineNumber": 10},
    {"id": 2, "label": "IDENTIFIER", "name": "y", "code": "y", "parentMethod": "main.process", "parentFile": "main.go", "lineNumber": 11},
    {"id": 3, "label": "IDENTIFIER", "name": "z", "code": "z", "parentMethod": "main.process", "parentFile": "main.go", "lineNumber": 12}
  ],
  "edges": [{"src": 1, "dst": 2, "label": "REACHING_DEF"}, {"src": 2, "dst": 3, "label": "REACHING_DEF"}]
}`)
	repo := &domain.Repository{Module: "example.com/m"}
	var flows []*domain.CodeEntity
	if err := parseSlices(repo, path, func(item domain.Item) error {
		if item.Node != nil {
			flows = append(flows, item.Node)
		}
		return nil
	}); err != nil {
		t.Fatalf("parseSlices: %v", err)
	}
	if len(flows) != 1 {
		t.Fatalf("method flow nodes = %d, want 1", len(flows))
	}
	fl := flows[0]
	if fl.ID != "symbol:go:example.com/m:process" || fl.Kind != domain.KindFunction {
		t.Errorf("flow node = %+v", fl)
	}
	df, ok := fl.Properties["data_flows"].([]string)
	if !ok || len(df) != 1 || df[0] != "x -> y" {
		t.Errorf("data_flows = %+v", fl.Properties["data_flows"])
	}
}

func TestParseSlicesCrossMethod(t *testing.T) {
	// 跨方法 REACHING_DEF → DATA_FLOWS_TO 边 + 两端节点
	path := writeSlices(t, `{
  "$type": "io.joern.dataflow.tracing.semantics.Slice",
  "nodes": [
    {"id": 1, "label": "IDENTIFIER", "name": "a", "code": "a", "parentMethod": "caller.run", "parentFile": "a.go", "lineNumber": 3},
    {"id": 2, "label": "IDENTIFIER", "name": "b", "code": "b", "parentMethod": "(Svc).Work", "parentFile": "svc/svc.go", "lineNumber": 7}
  ],
  "edges": [{"src": 1, "dst": 2, "label": "REACHING_DEF"}]
}`)
	repo := &domain.Repository{Module: "example.com/m"}
	var nodes []*domain.CodeEntity
	var facts []*domain.Fact
	if err := parseSlices(repo, path, func(item domain.Item) error {
		if item.Node != nil {
			nodes = append(nodes, item.Node)
		}
		if item.Fact != nil {
			facts = append(facts, item.Fact)
		}
		return nil
	}); err != nil {
		t.Fatalf("parseSlices: %v", err)
	}
	// 边 + 两端节点
	if len(facts) != 1 {
		t.Fatalf("cross facts = %d, want 1", len(facts))
	}
	f := facts[0]
	if f.SourceID != "symbol:go:example.com/m:run" ||
		f.TargetID != "symbol:go:example.com/m/svc:(Svc).Work" ||
		f.Kind != domain.FactDataFlowsTo || f.Confidence != 0.7 {
		t.Errorf("fact = %+v", f)
	}
	if f.Metadata["source"] != "a" || f.Metadata["sink"] != "b" {
		t.Errorf("metadata = %+v", f.Metadata)
	}
	// 节点（跨方法也 emit 轻量节点）
	if len(nodes) != 2 {
		t.Errorf("nodes = %d, want 2", len(nodes))
	}
}

func TestParseSlicesInvalidJSON(t *testing.T) {
	path := writeSlices(t, "{not json")
	repo := &domain.Repository{Module: "example.com/m"}
	if err := parseSlices(repo, path, func(domain.Item) error { return nil }); err == nil {
		t.Error("invalid JSON should fail")
	}
}

func TestParseSlicesMissingNodes(t *testing.T) {
	// 边引用不存在的节点 → 静默跳过
	path := writeSlices(t, `{
  "nodes": [{"id": 1, "label": "X", "parentMethod": "a.b", "parentFile": "a.go", "lineNumber": 1}],
  "edges": [{"src": 1, "dst": 999, "label": "REACHING_DEF"}]
}`)
	repo := &domain.Repository{Module: "example.com/m"}
	if err := parseSlices(repo, path, func(domain.Item) error { return nil }); err != nil {
		t.Fatalf("parseSlices: %v", err)
	}
}
