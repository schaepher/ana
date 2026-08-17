//go:build integration

package integration

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestModuleCallsSelfContained：模块间 gRPC 调用（field_trace.md §18）——
// fixture 模拟 protoc 生成代码（.pb.go）+ modules.yaml → query module-calls
// 输出 svc_a → svc_b: Greeter.SayHello；export graph --type modules 产出图。
func TestModuleCallsSelfContained(t *testing.T) {
	if !scipGoAvailable() {
		t.Skip("scip-go not found")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/mono\n\ngo 1.21\n")
	writeFile(t, filepath.Join(dir, "pb/greet.pb.go"), `package pb

type GreeterServer interface{ SayHello(string) string }

func RegisterGreeterServer(s any, impl GreeterServer) {}

type GreeterClient interface{ SayHello(string) string }

func NewGreeterClient(conn any) GreeterClient { return nil }
`)
	writeFile(t, filepath.Join(dir, "svc_a/client.go"), `package svc_a

import "example.com/mono/pb"

func callGreeter(conn any) {
	c := pb.NewGreeterClient(conn)
	c.SayHello("hi")
}
`)
	writeFile(t, filepath.Join(dir, "svc_b/server.go"), `package svc_b

import "example.com/mono/pb"

type greeterImpl struct{}

func (g *greeterImpl) SayHello(s string) string { return s }

func register(s any) {
	pb.RegisterGreeterServer(s, &greeterImpl{})
}
`)
	writeFile(t, filepath.Join(dir, "modules.yaml"), `modules:
  - prefix: "svc_a"
    name: "svc_a"
  - prefix: "svc_b"
    name: "svc_b"
`)
	if code := runCLI(t, "init", "--repo", dir); code != 0 {
		t.Fatalf("init exit = %d", code)
	}
	code, out := runCLIOut(t, "query", "module-calls", "--repo", dir)
	if code != 0 {
		t.Fatalf("module-calls exit = %d", code)
	}
	if !strings.Contains(out, "svc_a → svc_b") || !strings.Contains(out, "Greeter.SayHello") {
		t.Errorf("module-calls 应输出 svc_a → svc_b: Greeter.SayHello，output=%q", out)
	}

	code, out = runCLIOut(t, "query", "module-calls", "--repo", dir, "--json")
	if code != 0 || !strings.Contains(out, `"from_module": "svc_a"`) || !strings.Contains(out, `"to_module": "svc_b"`) {
		t.Errorf("module-calls --json 缺失，code=%d output=%q", code, out[:min(len(out), 300)])
	}

	code, out = runCLIOut(t, "query", "module-calls", "svc_b", "--repo", dir)
	if code != 0 || strings.Contains(out, "svc_a →") {
		t.Errorf("--module svc_b 应无调用（svc_b 是被调方），code=%d output=%q", code, out)
	}

	code, out = runCLIOut(t, "export", "graph", "--type", "modules", "--repo", dir)
	if code != 0 || !strings.Contains(out, "flowchart") || !strings.Contains(out, "Greeter.SayHello") {
		t.Errorf("export graph modules 缺失，code=%d output=%q", code, out[:min(len(out), 300)])
	}
}

// TestModuleCallsDirectSelfContained：§18.6 手写 client——conn.Invoke
// 方法路径调用 + const 传播 → module-calls 输出含服务端模块（impl 按
// grpc_service name 匹配，跨 symbol:go / symbol:proto 标识）。
func TestModuleCallsDirectSelfContained(t *testing.T) {
	if !scipGoAvailable() {
		t.Skip("scip-go not found")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/mono\n\ngo 1.21\n")
	writeFile(t, filepath.Join(dir, "grpc/conn.go"), `package grpc

type ClientConn struct{}

func (c *ClientConn) Invoke(ctx any, method string, args ...any) {}
`)
	writeFile(t, filepath.Join(dir, "pb/greet.pb.go"), `package pb

type GreeterServer interface{ SayHello(string) string }

func RegisterGreeterServer(s any, impl GreeterServer) {}
`)
	writeFile(t, filepath.Join(dir, "svc_a/client.go"), `package svc_a

import "example.com/mono/grpc"

func callGreeter(conn *grpc.ClientConn) {
	conn.Invoke(nil, "/example.com.pb.Greeter/SayHello", nil)
}
`)
	writeFile(t, filepath.Join(dir, "svc_b/server.go"), `package svc_b

import "example.com/mono/pb"

type greeterImpl struct{}

func (g *greeterImpl) SayHello(s string) string { return s }

func register(s any) {
	pb.RegisterGreeterServer(s, &greeterImpl{})
}
`)
	writeFile(t, filepath.Join(dir, "modules.yaml"), `modules:
  - prefix: "svc_a"
    name: "svc_a"
  - prefix: "svc_b"
    name: "svc_b"
`)
	if code := runCLI(t, "init", "--repo", dir); code != 0 {
		t.Fatalf("init exit = %d", code)
	}
	code, out := runCLIOut(t, "query", "module-calls", "--repo", dir)
	if code != 0 {
		t.Fatalf("module-calls exit = %d", code)
	}

	if !strings.Contains(out, "svc_a → svc_b") || !strings.Contains(out, "Greeter.SayHello") {
		t.Errorf("module-calls 应输出 svc_a → svc_b: Greeter.SayHello（手写形态），output=%q", out)
	}
}

// TestModuleCallsHTTPSelfContained：§18.7 HTTP 模块间调用——routes.yaml
// 路由表 + http.Get 客户端 → module-calls 输出 http 调用（含 transport）。
func TestModuleCallsHTTPSelfContained(t *testing.T) {
	if !scipGoAvailable() {
		t.Skip("scip-go not found")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/mono\n\ngo 1.21\n")
	writeFile(t, filepath.Join(dir, "routes.yaml"), `routes:
  - path: "/api/orders"
    handler: "svc_orders:(Handler).ListOrders"
    method: "GET"
`)
	writeFile(t, filepath.Join(dir, "http/http.go"), `package http

func Get(url string) {}
`)
	writeFile(t, filepath.Join(dir, "svc_a/client.go"), `package svc_a

import "example.com/mono/http"

func callOrders() {
	http.Get("https://orders.example.com/api/orders")
}
`)
	writeFile(t, filepath.Join(dir, "svc_orders/svc.go"), `package svc_orders

type Handler struct{}

func (h *Handler) ListOrders() {}
`)
	writeFile(t, filepath.Join(dir, "modules.yaml"), `modules:
  - prefix: "svc_a"
    name: "svc_a"
  - prefix: "svc_orders"
    name: "svc_orders"
`)
	if code := runCLI(t, "init", "--repo", dir); code != 0 {
		t.Fatalf("init exit = %d", code)
	}
	code, out := runCLIOut(t, "query", "module-calls", "--repo", dir)
	if code != 0 {
		t.Fatalf("module-calls exit = %d", code)
	}

	if !strings.Contains(out, "svc_a → svc_orders") || !strings.Contains(out, "[http]") ||
		!strings.Contains(out, "api/orders") {
		t.Errorf("module-calls 应输出 http 调用 svc_a → svc_orders，output=%q", out)
	}
}
