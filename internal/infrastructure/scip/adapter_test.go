package scip

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"

	"github.com/schaepher/codeintel/internal/domain"
)

func TestIndexScipMissingBin(t *testing.T) {
	// BinPath 指向不存在的二进制 → Index 返回错误（Orchestrator 标记失败）
	adapter := &Adapter{BinPath: filepath.Join(t.TempDir(), "no-such-scip-go")}
	err := adapter.Index(context.Background(), &domain.Repository{Path: t.TempDir()}, nil, func(domain.Item) error { return nil })
	if err == nil {
		t.Error("Index with missing scip-go should fail")
	}
}

// processFixture 构造一个 SCIP document 并跑 processDocument 收集产出。
func processFixture(t *testing.T, doc *scip.Document) ([]*domain.CodeEntity, []*domain.Fact) {
	t.Helper()
	var nodes []*domain.CodeEntity
	var facts []*domain.Fact
	adapter := &Adapter{}
	repo := &domain.Repository{Module: "example.com/m"}
	err := adapter.processDocument(repo, doc, func(item domain.Item) error {
		if item.Node != nil {
			nodes = append(nodes, item.Node)
		}
		if item.Fact != nil {
			facts = append(facts, item.Fact)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("processDocument: %v", err)
	}
	return nodes, facts
}

func TestProcessDocumentNodes(t *testing.T) {
	doc := &scip.Document{
		RelativePath: "svc/svc.go",
		Symbols: []*scip.SymbolInformation{
			{
				Symbol: "scip-go gomod example.com/m . `example.com/m/svc`/Service#",
				Kind:   scip.SymbolInformation_Struct,
			},
			{
				Symbol: "scip-go gomod example.com/m . `example.com/m/svc`/Service#Handle().",
				Kind:   scip.SymbolInformation_Method,
			},
			{
				// 接口方法：不作为独立节点
				Symbol: "scip-go gomod example.com/m . `example.com/m/svc`/Payer#Pay.",
				Kind:   scip.SymbolInformation_MethodSpecification,
			},
			{
				// 变量：不建节点
				Symbol: "scip-go gomod example.com/m . `example.com/m/svc`/global.",
				Kind:   scip.SymbolInformation_Variable,
			},
		},
		Occurrences: []*scip.Occurrence{
			{
				Symbol:     "scip-go gomod example.com/m . `example.com/m/svc`/Service#",
				SymbolRoles: int32(scip.SymbolRole_Definition),
				Range:      []int32{10, 0, 7},
			},
		},
	}
	nodes, _ := processFixture(t, doc)
	// file 节点 + Service + Handle = 3（接口方法/变量跳过）
	if len(nodes) != 3 {
		t.Fatalf("nodes = %d, want 3 (file + struct + method)", len(nodes))
	}
	seen := map[string]*domain.CodeEntity{}
	for _, n := range nodes {
		seen[string(n.ID)] = n
	}
	svc := seen["symbol:go:example.com/m/svc:Service"]
	if svc == nil || svc.Kind != domain.KindStruct || svc.FilePath != "svc/svc.go" {
		t.Errorf("Service node = %+v", svc)
	}
	// 定义行范围（range[0]+1）
	if svc.LineStart != 11 || svc.LineEnd != 11 {
		t.Errorf("Service line = %d-%d, want 11-11", svc.LineStart, svc.LineEnd)
	}
	if seen["symbol:go:example.com/m/svc:(Service).Handle"] == nil {
		t.Error("Handle node missing")
	}
	if seen["symbol:go:example.com/m/svc:(Payer).Pay"] != nil {
		t.Error("interface method must not be a node")
	}
	// file 节点
	fn := seen["file:svc/svc.go"]
	if fn == nil || fn.Kind != domain.KindFile {
		t.Errorf("file node = %+v", fn)
	}
}

func TestProcessDocumentDocComment(t *testing.T) {
	doc := &scip.Document{
		RelativePath: "svc/svc.go",
		Symbols: []*scip.SymbolInformation{
			{
				Symbol:        "scip-go gomod example.com/m . `example.com/m/svc`/Service#",
				Kind:          scip.SymbolInformation_Struct,
				Documentation: []string{"Service 处理业务"},
			},
		},
	}
	nodes, _ := processFixture(t, doc)
	for _, n := range nodes {
		if string(n.ID) == "symbol:go:example.com/m/svc:Service" {
			if n.Property("doc_comment") != "Service 处理业务" {
				t.Errorf("doc_comment = %q", n.Property("doc_comment"))
			}
			return
		}
	}
	t.Fatal("Service node missing")
}

func TestProcessDocumentImplements(t *testing.T) {
	doc := &scip.Document{
		RelativePath: "svc/svc.go",
		Symbols: []*scip.SymbolInformation{
			{
				// 实现者 Service（is_implementation 关系指向接口）
				Symbol: "scip-go gomod example.com/m . `example.com/m/svc`/Service#",
				Kind:   scip.SymbolInformation_Struct,
				Relationships: []*scip.Relationship{
					{
						Symbol:         "scip-go gomod example.com/m . `example.com/m/svc`/Payer#",
						IsImplementation: true,
					},
					// 非实现关系不建边
					{
						Symbol:         "scip-go gomod example.com/m . `example.com/m/other`/X#",
						IsImplementation: false,
					},
				},
			},
			{
				// 外部模块接口：无节点，implements 跳过（外键约束）
				Symbol: "scip-go gomod example.com/m . `example.com/other`/Iface#",
				Kind:   scip.SymbolInformation_Interface,
			},
			{
				// 接口方法关系：不建 implements 边
				Symbol: "scip-go gomod example.com/m . `example.com/m/svc`/Payer#Pay.",
				Kind:   scip.SymbolInformation_MethodSpecification,
				Relationships: []*scip.Relationship{
					{
						Symbol:         "scip-go gomod example.com/m . `example.com/m/svc`/Service#Handle().",
						IsImplementation: true,
					},
				},
			},
		},
	}
	nodes, facts := processFixture(t, doc)
	_ = nodes
	// 只有一条 implements：接口 Payer → 实现者 Service（方向：接口 → 实现）
	if len(facts) != 1 {
		t.Fatalf("implements facts = %d, want 1", len(facts))
	}
	f := facts[0]
	if f.SourceID != "symbol:go:example.com/m/svc:Payer" ||
		f.TargetID != "symbol:go:example.com/m/svc:Service" ||
		f.Kind != domain.FactImplements || f.Confidence != 1.0 {
		t.Errorf("fact = %+v", f)
	}
}

func TestProcessDocumentLocalSymbol(t *testing.T) {
	// local 符号跳过（FromScipSymbol 报错）
	doc := &scip.Document{
		RelativePath: "a.go",
		Symbols: []*scip.SymbolInformation{
			{Symbol: "local 3", Kind: scip.SymbolInformation_Function},
		},
	}
	nodes, facts := processFixture(t, doc)
	// 只有 file 节点
	if len(nodes) != 1 || len(facts) != 0 {
		t.Errorf("nodes = %d facts = %d", len(nodes), len(facts))
	}
}

func TestIsInModule(t *testing.T) {
	cases := map[string]bool{
		"example.com/m":        true,
		"example.com/m/svc":    true,
		"example.com/other":    false,
		"example.com/more":     false, // 前缀匹配不能误判 example.com/more
		"example.com/m/extra":  true,
	}
	for p, want := range cases {
		if got := isInModule(p, "example.com/m"); got != want {
			t.Errorf("isInModule(%q) = %v, want %v", p, got, want)
		}
	}
}

// TestProcessDocumentProtobufRoundtrip：验证构造的 document 能 proto 序列化
// （adapter 解析真实 scip 输出用同一路径）。
func TestProcessDocumentProtobufRoundtrip(t *testing.T) {
	doc := &scip.Document{
		RelativePath: "svc/svc.go",
		Symbols: []*scip.SymbolInformation{
			{Symbol: "scip-go gomod example.com/m . `example.com/m/svc`/Service#", Kind: scip.SymbolInformation_Struct},
		},
	}
	data, err := proto.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out scip.Document
	if err := proto.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Symbols) != 1 || out.Symbols[0].Symbol == "" {
		t.Error("roundtrip lost symbols")
	}
}
