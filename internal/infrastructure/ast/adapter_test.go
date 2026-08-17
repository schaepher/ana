package ast

import (
	"context"
	"os"
	"path/filepath"
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
	repo := &domain.Repository{Path: dir, Module: "example.com/mtest", Modules: []string{"example.com/mtest"}}
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

// TestSetChangedFilesSkipsUnchanged：P1-1——SetChangedFiles 后只分析变更
// 文件（增量更新 AST 文件级跳过，§20.3 唯一真实加速点）；未设置时全量。

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

// TestVarInitCallEdge：Q108——包级 var 初始化中的函数调用（var x = NewFoo()）
// 须建 calls 边（此前不建，构造函数被误报"未调用"）。

// TestGrpcClientCallEdge：模块间调用（field_trace.md §18）——模拟 protoc
// 生成代码（.pb.go）：RegisterGreeterServer（服务端）+ NewGreeterClient
// （客户端）→ grpc_service 节点、grpc_impl 边（实现类型）、grpc_call 边
// （客户端调用方函数 → 服务，metadata 带方法名与行号）。

// TestGrpcDirectCallEdge：§18.6 手写 client——conn.Invoke 字面量路径 /
// const 传播 / 顶层 grpc.Invoke / 变量（不产边）→ grpc_call 边
// （target = symbol:proto:<proto包>:svc.<服务名>，metadata method_path）。

// TestHTTPCallEdge：§18.7 HTTP 模块间调用——routes.yaml 路由表 +
// http.Get/NewRequest 客户端（URL 字面量）→ http_route 节点 +
// http_call 边（匹配路由 / 前缀 / 未匹配外部虚拟节点）。

// TestHTTPClientDoReq：P1-3——HTTP 客户端识别三项扩展：
// ① NewRequestWithContext（同 NewRequest 参数位，此前完全漏识别）
// ② const 字符串拼接 URL（extractStringArg 常量传播扩展）
// ③ req := http.NewRequest(...) + client.Do(req) 组合不重复建边

// TestNestedArgExternalCallee：P2-2——外层 callee 是外部包函数时，
// 参数位置的嵌套调用仍建 passes_result（fmt.Errorf("%v", joinIDs(x))
// → joinIDs 有入边，unused 不误报）。

// TestEmbeddedPromotedMethodCalled：P2-2 固化——嵌入提升方法调用
// （a.Exec，Exec 由嵌入字段提升）建 calls 边到声明方法 (DB).Exec，
// unused 不误报（§16.2 旧盲区，Selection 解析已解决）。

// TestFuncValueCall：P2-1——函数值赋值盲区收敛：
// f := g; f() 建 calls 边（h→g）；方法值 fn := obj.M; fn() 建边（m→(T).M）。

// TestGrpcClientCrossFunction：§21.1 跨函数客户端——形参类型是
// pb.GreeterClient 的函数内 c.Method() 归属服务 Greeter。

// TestGrpcServiceDesc：§21.2 ServiceDesc 动态注册——grpc.ServiceDesc
// 复合字面量的 ServiceName → grpc_service 节点（symbol:proto 标识，
// 与手写 client 合并）。
