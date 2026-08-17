package ast

import (
	"strings"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

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

		"pb/service.pb.go": `package pb

type FooImpl struct{}

func RegisterFooServer(s any, impl any) {}
`,
	})

	found := false
	for _, f := range facts {
		if f.Kind == domain.FactCalls && strings.Contains(string(f.TargetID), "(impl).Handle") {
			found = true
		}
	}
	if !found {
		t.Errorf("接口方法具体化未产出 (impl).Handle 调用边")
	}

	marked := false
	for _, n := range nodes {
		if string(n.ID) == "symbol:go:example.com/mtest/pb:FooImpl" && n.Properties["serves_grpc"] == "true" {
			marked = true
		}
	}
	if !marked {
		t.Errorf("FooImpl 应标记 serves_grpc")
	}

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

// TestHTTPCallEdge：§18.7 HTTP 模块间调用——routes.yaml 路由表 +
// http.Get/NewRequest 客户端（URL 字面量）→ http_route 节点 +
// http_call 边（匹配路由 / 前缀 / 未匹配外部虚拟节点）。
func TestHTTPCallEdge(t *testing.T) {
	_, facts := indexFixture(t, map[string]string{
		"go.mod": "module example.com/mtest\n\ngo 1.21\n",
		"routes.yaml": `routes:
  - path: "/api/orders"
    handler: "svc_orders:(Handler).ListOrders"
    method: "GET"
  - path: "/api/users/"
    handler: "svc_users:(Handler).List"
`,
		"http/http.go": `package http

func Get(url string) {}

func NewRequest(method, url string, body any) {}
`,
		"svc_a/client.go": `package svc_a

import "example.com/mtest/http"

func callOrders() {
	http.Get("https://orders.example.com/api/orders")
}

func callUsers() {
	http.NewRequest("GET", "https://u.example.com/api/users/1", nil)
}

func callExt() {
	http.Get("https://ext.example.com/other")
}
`,
		"svc_orders/svc.go": `package svc_orders

type Handler struct{}

func (h *Handler) ListOrders() {}
`,
		"svc_users/svc.go": `package svc_users

type Handler struct{}

func (h *Handler) List() {}
`,
	})
	gotCall := map[string]string{}
	for _, f := range facts {
		if f.Kind == domain.FactHTTPCall {
			key := string(f.SourceID)
			gotCall[key] = string(f.TargetID) + "|" + f.Metadata["path"].(string) + "|" + f.Metadata["host"].(string)
		}
	}

	want := "symbol:go:example.com/mtest/svc_orders:route./api/orders"
	key := "symbol:go:example.com/mtest/svc_a:callOrders"
	if gotCall[key] == "" || gotCall[key][:len(want)] != want {
		t.Errorf("callOrders http_call = %q, want target 前缀 %s", gotCall[key], want)
	}

	want2 := "symbol:go:example.com/mtest/svc_users:route./api/users/"
	key2 := "symbol:go:example.com/mtest/svc_a:callUsers"
	if gotCall[key2] == "" || gotCall[key2][:len(want2)] != want2 {
		t.Errorf("callUsers http_call = %q, want target 前缀 %s", gotCall[key2], want2)
	}

	key3 := "symbol:go:example.com/mtest/svc_a:callExt"
	if gotCall[key3] == "" || !strings.HasPrefix(gotCall[key3], "symbol:http:") {
		t.Errorf("callExt http_call = %q, want 外部虚拟节点", gotCall[key3])
	}

	if !strings.Contains(gotCall[key3], "ext.example.com") {
		t.Errorf("callExt host 缺失: %q", gotCall[key3])
	}
}

// TestHTTPClientDoReq：P1-3——HTTP 客户端识别三项扩展：
// ① NewRequestWithContext（同 NewRequest 参数位，此前完全漏识别）
// ② const 字符串拼接 URL（extractStringArg 常量传播扩展）
// ③ req := http.NewRequest(...) + client.Do(req) 组合不重复建边
func TestHTTPClientDoReq(t *testing.T) {
	_, facts := indexFixture(t, map[string]string{
		"go.mod": "module example.com/mtest\n\ngo 1.21\n",
		"routes.yaml": `routes:
  - path: "/api/orders"
    handler: "svc_orders:(Handler).ListOrders"
    method: "GET"
`,
		"http/http.go": `package http

type Request struct{}

type Client struct{}

func Get(url string) {}

func NewRequest(method, url string, body any) (*Request, error) { return nil, nil }

func NewRequestWithContext(ctx any, method, url string, body any) (*Request, error) { return nil, nil }

func (c *Client) Do(req any) {}
`,
		"svc_a/client.go": `package svc_a

import "example.com/mtest/http"

const apiBase = "https://orders.example.com"

// ① NewRequestWithContext（真实 http 包同签名，验证仓库 openai.go 在用）
func callWithCtx() {
	http.NewRequestWithContext(nil, "GET", "https://orders.example.com/api/orders", nil)
}

// ② const 拼接：apiBase + "/api/orders"
func callConstConcat() {
	http.Get(apiBase + "/api/orders")
}

// ③ NewRequest + Do(req) 组合：请求发出点是 Do，但 URL 信息在
// NewRequest——只在 NewRequest 调用点建边，Do 不重复
func callReqDo() {
	req, _ := http.NewRequest("GET", "https://orders.example.com/api/orders", nil)
	var c http.Client
	c.Do(req)
}
`,
		"svc_orders/svc.go": `package svc_orders

type Handler struct{}

func (h *Handler) ListOrders() {}
`,
	})
	gotCall := map[string][]string{}
	for _, f := range facts {
		if f.Kind == domain.FactHTTPCall {
			key := string(f.SourceID)
			gotCall[key] = append(gotCall[key], string(f.TargetID))
		}
	}
	want := "symbol:go:example.com/mtest/svc_orders:route./api/orders"

	if len(gotCall["symbol:go:example.com/mtest/svc_a:callWithCtx"]) != 1 ||
		gotCall["symbol:go:example.com/mtest/svc_a:callWithCtx"][0] != want {
		t.Errorf("callWithCtx http_call = %v, want [%s]（NewRequestWithContext 漏识别）", gotCall["symbol:go:example.com/mtest/svc_a:callWithCtx"], want)
	}

	if len(gotCall["symbol:go:example.com/mtest/svc_a:callConstConcat"]) != 1 ||
		gotCall["symbol:go:example.com/mtest/svc_a:callConstConcat"][0] != want {
		t.Errorf("callConstConcat http_call = %v, want [%s]（const 拼接未传播）", gotCall["symbol:go:example.com/mtest/svc_a:callConstConcat"], want)
	}

	if len(gotCall["symbol:go:example.com/mtest/svc_a:callReqDo"]) != 1 {
		t.Errorf("callReqDo http_call = %d 条, want 1（Do 处重复建边）: %v", len(gotCall["symbol:go:example.com/mtest/svc_a:callReqDo"]), gotCall["symbol:go:example.com/mtest/svc_a:callReqDo"])
	}
}
