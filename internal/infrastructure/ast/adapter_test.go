package ast

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
	"golang.org/x/tools/go/packages"
)

// writeFile 写入测试模块文件。
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// indexFixture 在临时目录构建 Go 模块并跑 ast.Adapter.Index，收集全部产出。
func indexFixture(t *testing.T, files map[string]string) ([]*domain.CodeEntity, []*domain.Fact) {
	t.Helper()
	dir := t.TempDir()
	for path, content := range files {
		writeFile(t, filepath.Join(dir, path), content)
	}
	var nodes []*domain.CodeEntity
	var facts []*domain.Fact
	adapter := &Adapter{}
	repo := &domain.Repository{Path: dir, Module: "example.com/mtest"}
	pkgs, err := astLoadTestPackages(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	err = adapter.Index(context.Background(), repo, pkgs, func(item domain.Item) error {
		if item.Node != nil {
			nodes = append(nodes, item.Node)
		}
		if item.Fact != nil {
			facts = append(facts, item.Fact)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	return nodes, facts
}

// findNode 按 ID 查找节点。
func findNode(t *testing.T, nodes []*domain.CodeEntity, id string) *domain.CodeEntity {
	t.Helper()
	for _, n := range nodes {
		if string(n.ID) == id {
			return n
		}
	}
	t.Fatalf("node not found: %s", id)
	return nil
}

// findFact 按 (source, target, kind) 查找边。
func findFact(t *testing.T, facts []*domain.Fact, source, target, kind string) *domain.Fact {
	t.Helper()
	for _, f := range facts {
		if string(f.SourceID) == source && string(f.TargetID) == target && string(f.Kind) == kind {
			return f
		}
	}
	t.Fatalf("fact not found: %s -> %s [%s]", source, target, kind)
	return nil
}

func hasFact(facts []*domain.Fact, source, target, kind string) bool {
	for _, f := range facts {
		if string(f.SourceID) == source && string(f.TargetID) == target && string(f.Kind) == kind {
			return true
		}
	}
	return false
}

const fixtureGoMod = "module example.com/mtest\n\ngo 1.21\n"

func TestIndexCallsAndImports(t *testing.T) {
	nodes, facts := indexFixture(t, map[string]string{
		"go.mod":    fixtureGoMod,
		"main.go":   "package main\n\nimport \"example.com/mtest/svc\"\n\nfunc main() {\n\ts := &svc.Service{}\n\ts.Handle()\n}\n",
		"svc/s.go":  "package svc\n\ntype Service struct{}\n\nfunc (s *Service) Handle() {}\n",
		"util/u.go": "package util\n\nfunc Helper() {}\n",
	})

	// main → (Service).Handle 跨包方法调用
	findFact(t, facts,
		"symbol:go:example.com/mtest:main",
		"symbol:go:example.com/mtest/svc:(Service).Handle",
		"calls")
	// imports：main 包 → svc 包（包节点 ID = symbol:go:<path>:<basename>）
	findFact(t, facts,
		"symbol:go:example.com/mtest:mtest",
		"symbol:go:example.com/mtest/svc:svc",
		"imports")
	// 方法节点存在
	if n := findNode(t, nodes, "symbol:go:example.com/mtest/svc:(Service).Handle"); n.Kind != domain.KindMethod {
		t.Errorf("Handle kind = %s", n.Kind)
	}
	// 未导入的包（util）不产生 imports 边
	if hasFact(facts, "symbol:go:example.com/mtest:mtest", "symbol:go:example.com/mtest/util:util", "imports") {
		t.Error("util should not be imported")
	}
}

func TestIndexNestedArgPassesResult(t *testing.T) {
	// A(B(C()))：A 调用 B 且参数是 C 的返回 → B 持有 C 的返回参数（passes_result）
	_, facts := indexFixture(t, map[string]string{
		"go.mod":  fixtureGoMod,
		"main.go": "package main\n\nfunc C() int { return 0 }\nfunc B(x int) int { return x }\nfunc A(x int) {}\n\nfunc caller() {\n\tA(B(C()))\n}\n",
	})
	// A→B、B→C passes_result 链；A 不直接 calls B（参数位置不建 calls）
	findFact(t, facts,
		"symbol:go:example.com/mtest:A",
		"symbol:go:example.com/mtest:B",
		"passes_result")
	findFact(t, facts,
		"symbol:go:example.com/mtest:B",
		"symbol:go:example.com/mtest:C",
		"passes_result")
	if hasFact(facts, "symbol:go:example.com/mtest:A", "symbol:go:example.com/mtest:B", "calls") {
		t.Error("A should not have direct calls edge to B (arg position)")
	}
	// caller 调用 A
	findFact(t, facts,
		"symbol:go:example.com/mtest:caller",
		"symbol:go:example.com/mtest:A",
		"calls")
}

func TestIndexFunctionAsArgPassesTo(t *testing.T) {
	// foo(bar)：foo 持有参数 bar（passes_to，接收函数 → 参数函数）
	_, facts := indexFixture(t, map[string]string{
		"go.mod":  fixtureGoMod,
		"main.go": "package main\n\nfunc foo(fn func()) { fn() }\n\nfunc bar() {}\n\nfunc caller() {\n\tfoo(bar)\n}\n",
	})
	findFact(t, facts,
		"symbol:go:example.com/mtest:foo",
		"symbol:go:example.com/mtest:bar",
		"passes_to")
	// foo 调用 bar？bar 是参数（不是 foo 体内的调用点）→ 不应有 calls
	if hasFact(facts, "symbol:go:example.com/mtest:foo", "symbol:go:example.com/mtest:bar", "calls") {
		t.Error("foo should not calls bar (bar is arg)")
	}
}

func TestIndexExternalCalleeAsArg(t *testing.T) {
	// 外部包函数作为参数接收者（如 net/http.HandleFunc）：
	// 调用者 → 外部接收函数建 calls 边（允许展开一层外部包），
	// 外部接收函数 → 参数函数建 passes_to 边
	_, facts := indexFixture(t, map[string]string{
		"go.mod":  fixtureGoMod,
		"main.go": "package main\n\nimport \"net/http\"\n\nfunc handler(w http.ResponseWriter, r *http.Request) {}\n\nfunc setup() {\n\thttp.HandleFunc(\"/x\", handler)\n}\n",
	})
	// setup → (ServeMux).HandleFunc（calls，外部包展开层）
	findFact(t, facts,
		"symbol:go:example.com/mtest:setup",
		"symbol:go:net/http:HandleFunc",
		"calls")
	// (ServeMux).HandleFunc → handler（passes_to，持有参数）
	findFact(t, facts,
		"symbol:go:net/http:HandleFunc",
		"symbol:go:example.com/mtest:handler",
		"passes_to")
	// 普通外部调用（fmt.Println）不建边
	_, facts2 := indexFixture(t, map[string]string{
		"go.mod":  fixtureGoMod,
		"main.go": "package main\n\nimport \"fmt\"\n\nfunc logIt() {\n\tfmt.Println(\"hello\")\n}\n",
	})
	if hasFact(facts2, "symbol:go:example.com/mtest:logIt", "symbol:go:fmt:Println", "calls") {
		t.Error("plain external call must not create calls edge")
	}
}

func TestIndexInitializesAndUses(t *testing.T) {
	// s := &Service{} → initializes（main → Service）+ uses（Service → 方法）
	_, facts := indexFixture(t, map[string]string{
		"go.mod":  fixtureGoMod,
		"main.go": "package main\n\nimport \"example.com/mtest/svc\"\n\nfunc main() {\n\ts := &svc.Service{}\n\ts.Handle()\n}\n",
		"svc/s.go": "package svc\n\ntype Service struct{}\n\nfunc (s *Service) Handle() {}\n",
	})
	findFact(t, facts,
		"symbol:go:example.com/mtest:main",
		"symbol:go:example.com/mtest/svc:Service",
		"initializes")
	// uses：对象无独立节点（MVP 对象直接挂在 struct 上）→ struct → 方法
	findFact(t, facts,
		"symbol:go:example.com/mtest/svc:Service",
		"symbol:go:example.com/mtest/svc:(Service).Handle",
		"uses")
}

func TestIndexHasMethod(t *testing.T) {
	// 接收者类型 → 方法（has_method）
	_, facts := indexFixture(t, map[string]string{
		"go.mod":  fixtureGoMod,
		"main.go": "package main\n\nfunc main() {}\n",
		"svc/s.go": "package svc\n\ntype Service struct{}\n\nfunc (s *Service) Handle() {}\n\nfunc (s *Service) helper() {}\n",
	})
	findFact(t, facts,
		"symbol:go:example.com/mtest/svc:Service",
		"symbol:go:example.com/mtest/svc:(Service).Handle",
		"has_method")
	findFact(t, facts,
		"symbol:go:example.com/mtest/svc:Service",
		"symbol:go:example.com/mtest/svc:(Service).helper",
		"has_method")
}

func TestIndexServiceFlags(t *testing.T) {
	// 调用 net/http 的函数标记 serves_http
	nodes, _ := indexFixture(t, map[string]string{
		"go.mod":  fixtureGoMod,
		"main.go": "package main\n\nimport \"net/http\"\n\nfunc serve() {\n\thttp.ListenAndServe(\":8080\", nil)\n}\n",
	})
	n := findNode(t, nodes, "symbol:go:example.com/mtest:serve")
	if n.Properties["serves_http"] != "true" {
		t.Errorf("serve should carry serves_http flag, got %+v", n.Properties)
	}
}

func TestIndexStructFields(t *testing.T) {
	// struct 节点的 properties.fields（字段名 | 类型）
	nodes, _ := indexFixture(t, map[string]string{
		"go.mod":  fixtureGoMod,
		"main.go": "package main\n\nfunc main() {}\n",
		"svc/s.go": "package svc\n\ntype Service struct {\n\tName string\n\tAddr *netAddr\n}\n\ntype netAddr struct{}\n",
	})
	n := findNode(t, nodes, "symbol:go:example.com/mtest/svc:Service")
	fields, ok := n.Properties["fields"].([]map[string]any)
	if !ok || len(fields) != 2 {
		t.Fatalf("fields = %+v", n.Properties["fields"])
	}
	f0 := fields[0]
	if f0["name"] != "Name" || f0["type"] != "string" {
		t.Errorf("field0 = %+v", f0)
	}
}

func TestIndexInterfaceMethodChaining(t *testing.T) {
	// 链式调用接口方法：main → p.Handle()（p 是接口类型，Handle 是接口方法）。
	// 静态分析 return 具体类型后仍应产出 calls 边
	_, facts := indexFixture(t, map[string]string{
		"go.mod":  fixtureGoMod,
		"main.go": "package main\n\nimport \"example.com/mtest/svc\"\n\nfunc main() {\n\ts := svc.New()\n\ts.Handle()\n}\n",
		"svc/s.go": "package svc\n\ntype Service struct{}\n\nfunc (s *Service) Handle() {}\n\nfunc New() *Service {\n\treturn &Service{}\n}\n",
	})
	// main 调用 (Service).Handle（通过返回的具体类型解析）
	findFact(t, facts,
		"symbol:go:example.com/mtest:main",
		"symbol:go:example.com/mtest/svc:(Service).Handle",
		"calls")
}

func TestIndexUnrelatedPackagesNotIncluded(t *testing.T) {
	// 外部依赖包（不在 module 内）不产出节点
	_, facts := indexFixture(t, map[string]string{
		"go.mod":  fixtureGoMod,
		"main.go": "package main\n\nimport \"example.com/mtest/svc\"\n\nfunc main() {\n\tsvc.Helper()\n}\n",
		"svc/s.go": "package svc\n\nfunc Helper() {}\n",
	})
	for _, f := range facts {
		if !strings.HasPrefix(string(f.SourceID), "symbol:go:example.com/") {
			t.Errorf("fact escapes module: %+v", f)
		}
	}
}

// astLoadTestPackages 加载测试仓库 packages（共享加载改造后由测试提供）。
func astLoadTestPackages(dir string) ([]*packages.Package, error) {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax |
			packages.NeedTypes | packages.NeedTypesInfo | packages.NeedImports | packages.NeedDeps,
		Dir: dir,
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return nil, err
	}
	return pkgs, nil
}

// TestIndexHTTPHandlerLeaves：⑬ 猎 bug——ast 叶子覆盖：接口方法链式调用
// 具体化（concreteMethodFor/concreteReturnType）、匿名嵌入字段名
// （embeddedTypeName）、protoc 风格 RegisterXxxServer 服务实现
// （serviceImplNode/isRegisterServerName）。
func TestIndexHTTPHandlerLeaves(t *testing.T) {
	nodes, facts := indexFixture(t, map[string]string{
		"go.mod": fixtureGoMod,
		"main.go": `package main

import "example.com/mtest/pb"

type Service interface {
	Handle()
}

type impl struct{}

func (i *impl) Handle() {}

func NewService() Service {
	return &impl{}
}

// 匿名嵌入字段（embeddedTypeName）
type Base struct{}

type S struct {
	*Base
}

func setup() {
	pb.RegisterFooServer(nil, &pb.FooImpl{})
	NewService().Handle()
}

func main() {}
`,
		// protoc 生成的注册函数在 .pb.go 文件（collectRegisterServers 按
		// 文件名后缀收集）
		"pb/service.pb.go": `package pb

type FooImpl struct{}

func RegisterFooServer(s any, impl any) {}
`,
	})
	// 接口方法具体化：setup → (impl).Handle（而非接口 Service）
	found := false
	for _, f := range facts {
		if f.Kind == domain.FactCalls && strings.Contains(string(f.TargetID), "(impl).Handle") {
			found = true
		}
	}
	if !found {
		t.Errorf("接口方法具体化未产出 (impl).Handle 调用边")
	}
	// 注册服务实现节点：FooImpl 标记 serves_grpc（节点可能先以普通 struct
	// 产出再补标记——同 ID 属性经 DB json_patch 合并，测试侧遍历断言）
	marked := false
	for _, n := range nodes {
		if string(n.ID) == "symbol:go:example.com/mtest/pb:FooImpl" && n.Properties["serves_grpc"] == "true" {
			marked = true
		}
	}
	if !marked {
		t.Errorf("FooImpl 应标记 serves_grpc")
	}
	// 匿名嵌入字段显示名（embeddedTypeName：*Base → Base）
	s := findNode(t, nodes, "symbol:go:example.com/mtest:S")
	fields, ok := s.Properties["fields"].([]map[string]any)
	if !ok || len(fields) != 1 {
		t.Fatalf("S fields = %+v", s.Properties["fields"])
	}
	if fields[0]["name"] != "Base" {
		t.Errorf("嵌入字段名 = %v, want Base", fields[0]["name"])
	}
}

// TestVarInitCallEdge：Q108——包级 var 初始化中的函数调用（var x = NewFoo()）
// 须建 calls 边（此前不建，构造函数被误报"未调用"）。
func TestVarInitCallEdge(t *testing.T) {
	_, facts := indexFixture(t, map[string]string{
		"go.mod": "module example.com/mtest\n\ngo 1.21\n",
		"main.go": `package m

type Foo struct{ A int }

func NewFoo() *Foo { return &Foo{} }

var G = NewFoo()

func main() { _ = G }
`,
	})
	hit := false
	for _, f := range facts {
		if f.Kind == domain.FactCalls && string(f.TargetID) == "symbol:go:example.com/mtest:NewFoo" {
			hit = true
		}
	}
	if !hit {
		t.Error("var x = NewFoo() 未建 calls 边（构造函数会被误报未调用）")
	}
}

// TestGrpcClientCallEdge：模块间调用（field_trace.md §18）——模拟 protoc
// 生成代码（.pb.go）：RegisterGreeterServer（服务端）+ NewGreeterClient
// （客户端）→ grpc_service 节点、grpc_impl 边（实现类型）、grpc_call 边
// （客户端调用方函数 → 服务，metadata 带方法名与行号）。
func TestGrpcClientCallEdge(t *testing.T) {
	_, facts := indexFixture(t, map[string]string{
		"go.mod": "module example.com/mtest\n\ngo 1.21\n",
		"pb/greet.pb.go": `package pb

type GreeterServer interface{ SayHello(string) string }

func RegisterGreeterServer(s any, impl GreeterServer) {}

type GreeterClient interface{ SayHello(string) string }

func NewGreeterClient(conn any) GreeterClient { return nil }
`,
		"svc_a/client.go": `package svc_a

import "example.com/mtest/pb"

func callGreeter(conn any) {
	c := pb.NewGreeterClient(conn)
	c.SayHello("hi")
}
`,
		"svc_b/server.go": `package svc_b

import "example.com/mtest/pb"

type greeterImpl struct{}

func (g *greeterImpl) SayHello(s string) string { return s }

func register(s any) {
	pb.RegisterGreeterServer(s, &greeterImpl{})
}
`,
	})
	// grpc_service 节点
	svcNode := false
	for _, f := range facts {
		if f.Kind == domain.FactGrpcCall {
			// grpc_call：调用方函数 → grpc_service
			if string(f.SourceID) != "symbol:go:example.com/mtest/svc_a:callGreeter" {
				t.Errorf("grpc_call source = %s", f.SourceID)
			}
			if string(f.TargetID) != "symbol:go:example.com/mtest/pb:svc.Greeter" {
				t.Errorf("grpc_call target = %s", f.TargetID)
			}
			if f.Metadata["method"] != "SayHello" {
				t.Errorf("grpc_call method = %v", f.Metadata["method"])
			}
			svcNode = true
		}
		if f.Kind == domain.FactGrpcImpl {
			if string(f.SourceID) != "symbol:go:example.com/mtest/svc_b:greeterImpl" {
				t.Errorf("grpc_impl source = %s", f.SourceID)
			}
			if string(f.TargetID) != "symbol:go:example.com/mtest/pb:svc.Greeter" {
				t.Errorf("grpc_impl target = %s", f.TargetID)
			}
		}
	}
	if !svcNode {
		t.Error("grpc_call 边缺失（NewGreeterClient 客户端调用未归属）")
	}
}
