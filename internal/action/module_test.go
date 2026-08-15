package action

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
)

// TestModuleOf：modules.yaml 前缀→模块映射 + 未匹配归 _root（field_trace.md §18.1）。
func TestModuleOf(t *testing.T) {
	dir := t.TempDir()
	writeModuleYAML(t, dir, `modules:
  - prefix: "internal/svc_a"
    name: "svc_a"
  - prefix: "pkg/common"
    name: "common"
`)
	a := newModuleActions(t, dir)
	cases := []struct{ pkg, want string }{
		{"example.com/app/internal/svc_a/client", "svc_a"},
		{"example.com/app/internal/svc_a", "svc_a"},
		{"example.com/app/pkg/common/util", "common"},
		{"example.com/app/internal/svc_b/server", "_root"}, // 未匹配
		{"example.com/app/main", "_root"},                  // 根包
	}
	for _, c := range cases {
		if got := a.ModuleOf(c.pkg); got != c.want {
			t.Errorf("ModuleOf(%s) = %q, want %q", c.pkg, got, c.want)
		}
	}
	// 无 modules.yaml → 全部 _root
	dir2 := t.TempDir()
	a2 := newModuleActions(t, dir2)
	if got := a2.ModuleOf("example.com/app/internal/x"); got != "_root" {
		t.Errorf("无配置 ModuleOf = %q, want _root", got)
	}
}

// TestModuleOfMultiGoMod：P2-3——多 go.mod monorepo：包路径匹配子 module
// 前缀时剥离子 module 前缀再匹配 modules.yaml 配置。
func TestModuleOfMultiGoMod(t *testing.T) {
	dir := t.TempDir()
	// 根 module + 子 module（app/）
	if err := os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module example.com/root\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app", "go.mod"),
		[]byte("module example.com/app\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeModuleYAML(t, dir, `modules:
  - prefix: "internal/svc"
    name: "svc"
  - prefix: "internal/order"
    name: "order"
`)
	// 自建 Actions（newModuleActions 会覆盖根 go.mod，破坏双 module 结构）
	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	a := New(sqlite.NewRepo(db))
	cases := []struct{ pkg, want string }{
		{"example.com/root/internal/svc/client", "svc"},     // 根 module 前缀剥离
		{"example.com/app/internal/order/handler", "order"}, // 子 module 前缀剥离（P2-3）
		{"example.com/app/other", "_root"},                  // 子 module 未匹配配置
		{"example.com/external/pkg", "_root"},               // 外部包
	}
	for _, c := range cases {
		if got := a.ModuleOf(c.pkg); got != c.want {
			t.Errorf("ModuleOf(%s) = %q, want %q", c.pkg, got, c.want)
		}
	}
}

// TestModuleCalls：grpc_call/grpc_impl 边 → 模块级调用表（18.4）。
func TestModuleCalls(t *testing.T) {
	dir := t.TempDir()
	writeModuleYAML(t, dir, `modules:
  - prefix: "internal/svc_a"
    name: "svc_a"
  - prefix: "internal/svc_b"
    name: "svc_b"
  - prefix: "pb"
    name: "proto"
`)
	// ModuleOf 需 go.mod 的 module 前缀（配置前缀为 module 相对路径）
	if err := os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module example.com/app\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	r := sqlite.NewRepo(db)
	// grpc_service 节点 + grpc_call（svc_a 的 callGreeter → Greeter.SayHello）
	// + grpc_impl（svc_b 的 greeterImpl → Greeter）
	nodes := []*domain.CodeEntity{
		{ID: "symbol:go:example.com/app/internal/svc_a:callGreeter", Kind: domain.KindFunction, Name: "callGreeter", FilePath: "internal/svc_a/client.go"},
		{ID: "symbol:go:example.com/app/internal/svc_b:greeterImpl", Kind: domain.KindStruct, Name: "greeterImpl", FilePath: "internal/svc_b/server.go"},
		{ID: "symbol:go:example.com/app/pb:svc.Greeter", Kind: domain.KindGrpcService, Name: "svc.Greeter", FilePath: "pb/greet.pb.go"},
		// 外部服务：客户端调用但仓库内无实现
		{ID: "symbol:go:example.com/app/internal/svc_a:callExternal", Kind: domain.KindFunction, Name: "callExternal", FilePath: "internal/svc_a/client.go"},
		{ID: "symbol:go:example.com/app/pb:svc.External", Kind: domain.KindGrpcService, Name: "svc.External", FilePath: "pb/ext.pb.go"},
	}
	edges := []*domain.Fact{
		{SourceID: "symbol:go:example.com/app/internal/svc_a:callGreeter", TargetID: "symbol:go:example.com/app/pb:svc.Greeter",
			Kind: domain.FactGrpcCall, ToolSource: domain.ToolCodeGraph, Confidence: 1,
			Metadata: map[string]any{"method": "SayHello", "line_num": 5}},
		{SourceID: "symbol:go:example.com/app/internal/svc_b:greeterImpl", TargetID: "symbol:go:example.com/app/pb:svc.Greeter",
			Kind: domain.FactGrpcImpl, ToolSource: domain.ToolCodeGraph, Confidence: 1},
		{SourceID: "symbol:go:example.com/app/internal/svc_a:callExternal", TargetID: "symbol:go:example.com/app/pb:svc.External",
			Kind: domain.FactGrpcCall, ToolSource: domain.ToolCodeGraph, Confidence: 1,
			Metadata: map[string]any{"method": "Ping", "line_num": 9}},
	}
	if _, err := r.SaveBatchStats(nodes, edges, nil); err != nil {
		t.Fatal(err)
	}
	a := New(r)
	calls, err := a.ModuleCalls("")
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 {
		t.Fatalf("calls = %d, want 2: %+v", len(calls), calls)
	}
	var greeter, external *ModuleCall
	for i := range calls {
		if calls[i].Service == "example.com/app/pb.Greeter" {
			greeter = &calls[i]
		}
		if calls[i].Service == "example.com/app/pb.External" {
			external = &calls[i]
		}
	}
	if greeter == nil || greeter.FromModule != "svc_a" || greeter.ToModule != "svc_b" ||
		greeter.Method != "SayHello" {
		t.Errorf("greeter call = %+v", greeter)
	}
	if external == nil || external.FromModule != "svc_a" || external.ToModule != "" ||
		external.Method != "Ping" {
		t.Errorf("external call = %+v（服务端不在仓库内 → ToModule 空）", external)
	}
	// --module 过滤
	only, err := a.ModuleCalls("svc_a")
	if err != nil {
		t.Fatal(err)
	}
	if len(only) != 2 {
		t.Errorf("module 过滤 = %d, want 2（svc_a 是两条调用的调用方）", len(only))
	}
}

func writeModuleYAML(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "modules.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newModuleActions(t *testing.T, dir string) *Actions {
	t.Helper()
	// ModuleOf 需从 go.mod 解析 module 前缀（配置前缀为 module 相对路径）
	if err := os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module example.com/app\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return New(sqlite.NewRepo(db))
}
