package ast

import (
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

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

	svcNode := false
	for _, f := range facts {
		if f.Kind == domain.FactGrpcCall {

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

// TestGrpcDirectCallEdge：§18.6 手写 client——conn.Invoke 字面量路径 /
// const 传播 / 顶层 grpc.Invoke / 变量（不产边）→ grpc_call 边
// （target = symbol:proto:<proto包>:svc.<服务名>，metadata method_path）。
func TestGrpcDirectCallEdge(t *testing.T) {
	_, facts := indexFixture(t, map[string]string{
		"go.mod": "module example.com/mtest\n\ngo 1.21\n",
		"grpc/conn.go": `package grpc

type ClientConn struct{}

func (c *ClientConn) Invoke(ctx any, method string, args ...any) {}

func (c *ClientConn) NewStream(ctx any, desc any, method string) {}

func Invoke(ctx any, target, method string, args ...any) {}
`,
		"svc_a/svc_a.go": `package svc_a

import "example.com/mtest/grpc"

func callDirect(conn *grpc.ClientConn) {
	conn.Invoke(nil, "/example.com.pb.Greeter/SayHello", nil)
}

func callConst(conn *grpc.ClientConn) {
	const method = "/example.com.pb.Order/Create"
	conn.Invoke(nil, method, nil)
}

func callStream(conn *grpc.ClientConn) {
	conn.NewStream(nil, nil, "/example.com.pb.Greeter/Stream")
}

func callTopLevel() {
	grpc.Invoke(nil, "localhost:8080", "/example.com.pb.Greeter/Ping", nil)
}

func callDynamic(conn *grpc.ClientConn, method string) {
	conn.Invoke(nil, method, nil) // 变量：不产边（盲区）
}
`,
	})
	type want struct {
		caller string
		svcID  string
		path   string
		method string
	}
	wants := []want{
		{"symbol:go:example.com/mtest/svc_a:callDirect", "symbol:proto:example.com.pb:svc.Greeter", "/example.com.pb.Greeter/SayHello", "SayHello"},
		{"symbol:go:example.com/mtest/svc_a:callConst", "symbol:proto:example.com.pb:svc.Order", "/example.com.pb.Order/Create", "Create"},
		{"symbol:go:example.com/mtest/svc_a:callStream", "symbol:proto:example.com.pb:svc.Greeter", "/example.com.pb.Greeter/Stream", "Stream"},
		{"symbol:go:example.com/mtest/svc_a:callTopLevel", "symbol:proto:example.com.pb:svc.Greeter", "/example.com.pb.Greeter/Ping", "Ping"},
	}
	got := map[string]map[string]bool{}
	for _, f := range facts {
		if f.Kind != domain.FactGrpcCall {
			continue
		}
		key := string(f.SourceID)
		if got[key] == nil {
			got[key] = map[string]bool{}
		}
		got[key][string(f.TargetID)+"|"+f.Metadata["method"].(string)+"|"+f.Metadata["method_path"].(string)] = true
	}
	for _, w := range wants {
		key := w.caller
		if got[key] == nil || !got[key][w.svcID+"|"+w.method+"|"+w.path] {
			t.Errorf("缺 grpc_call %s → %s (%s)，got %v", w.caller, w.svcID, w.path, got[key])
		}
	}

	if len(got["symbol:go:example.com/mtest/svc_a:callDynamic"]) != 0 {
		t.Errorf("变量方法路径不应产 grpc_call 边: %v", got["symbol:go:example.com/mtest/svc_a:callDynamic"])
	}
}

// TestGrpcClientCrossFunction：§21.1 跨函数客户端——形参类型是
// pb.GreeterClient 的函数内 c.Method() 归属服务 Greeter。
func TestGrpcClientCrossFunction(t *testing.T) {
	_, facts := indexFixture(t, map[string]string{
		"go.mod": "module example.com/mtest\n\ngo 1.21\n",
		"pb/greet.pb.go": `package pb

type GreeterClient interface{ SayHello(string) string }

func NewGreeterClient(conn any) GreeterClient { return nil }
`,
		"svc_a/svc_a.go": `package svc_a

import "example.com/mtest/pb"

// 形参类型是客户端接口：内部调用归属服务 Greeter
func handle(c pb.GreeterClient) {
	c.SayHello("hi")
}
`,
	})
	hit := false
	for _, f := range facts {
		if f.Kind == domain.FactGrpcCall &&
			string(f.SourceID) == "symbol:go:example.com/mtest/svc_a:handle" &&
			string(f.TargetID) == "symbol:go:example.com/mtest/pb:svc.Greeter" &&
			f.Metadata["method"] == "SayHello" {
			hit = true
		}
	}
	if !hit {
		t.Error("跨函数客户端调用未归属（形参类型识别缺失）")
	}
}

// TestGrpcServiceDesc：§21.2 ServiceDesc 动态注册——grpc.ServiceDesc
// 复合字面量的 ServiceName → grpc_service 节点（symbol:proto 标识，
// 与手写 client 合并）。
func TestGrpcServiceDesc(t *testing.T) {
	nodes, _ := indexFixture(t, map[string]string{
		"go.mod": "module example.com/mtest\n\ngo 1.21\n",
		"grpc/grpc.go": `package grpc

type ServiceDesc struct {
	ServiceName string
}

func RegisterService(s any, desc *ServiceDesc) {}
`,
		"svc_a/svc_a.go": `package svc_a

import "example.com/mtest/grpc"

var desc = &grpc.ServiceDesc{
	ServiceName: "example.com.pb.Greeter",
}

func register(s any) {
	grpc.RegisterService(s, desc)
}
`,
	})
	svcNode := false
	for _, n := range nodes {
		if n.Kind == domain.KindGrpcService && string(n.ID) == "symbol:proto:example.com.pb:svc.Greeter" &&
			n.Property("service_desc") == "true" {
			svcNode = true
		}
	}
	if !svcNode {
		t.Error("ServiceDesc 动态注册未发射 grpc_service 节点（symbol:proto 标识）")
	}
}
