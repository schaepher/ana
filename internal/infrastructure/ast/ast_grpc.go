package ast

import (
	"go/ast"
	"go/types"
	"strconv"
	"strings"

	"github.com/schaepher/codeintel/internal/domain"
	"golang.org/x/tools/go/packages"
)

// collectRegisterServers 遍历项目内包，收集定义在 .pb.go 中、函数名匹配
// RegisterXxxServer 的注册函数（key: "pkgPath:funcName"）。
func collectRegisterServers(pkgs []*packages.Package, modules []string) map[string]bool {
	out := map[string]bool{}
	for _, pkg := range pkgs {
		if !isInModule(pkg.PkgPath, modules) {
			continue
		}
		for _, f := range pkg.Syntax {
			file := pkg.Fset.PositionFor(f.Pos(), false).Filename
			if !strings.HasSuffix(file, ".pb.go") {
				continue
			}
			for _, decl := range f.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Recv != nil {
					continue
				}
				if isRegisterServerName(fn.Name.Name) {
					out[pkg.PkgPath+":"+fn.Name.Name] = true
				}
			}
		}
	}
	return out
}

// collectNewClients 遍历 .pb.go 收集 protoc 生成的 NewXxxClient 客户端
// 构造器（field_trace.md §18.2；key: "pkgPath:funcName" → 服务名）。
func collectNewClients(pkgs []*packages.Package, modules []string) map[string]string {
	out := map[string]string{}
	for _, pkg := range pkgs {
		if !isInModule(pkg.PkgPath, modules) {
			continue
		}
		for _, f := range pkg.Syntax {
			file := pkg.Fset.PositionFor(f.Pos(), false).Filename
			if !strings.HasSuffix(file, ".pb.go") {
				continue
			}
			for _, decl := range f.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Recv != nil {
					continue
				}
				if svc, ok2 := newClientService(fn.Name.Name); ok2 {
					out[pkg.PkgPath+":"+fn.Name.Name] = svc
				}
			}
		}
	}
	return out
}

// newClientService 从 NewXxxClient 函数名提取服务名（NewGreeterClient →
// Greeter）；非客户端构造器返回 ok=false。
func newClientService(name string) (string, bool) {
	if !strings.HasPrefix(name, "New") || !strings.HasSuffix(name, "Client") ||
		len(name) <= len("NewClient") {
		return "", false
	}
	return strings.TrimSuffix(strings.TrimPrefix(name, "New"), "Client"), true
}

// clientTypeService 从客户端接口类型名提取服务名（GreeterClient → Greeter；
// 形参类型识别，§21.1——与构造器名 NewXxxClient 不同，无 New 前缀）。
func clientTypeService(name string) (string, bool) {
	if !strings.HasSuffix(name, "Client") || len(name) <= len("Client") {
		return "", false
	}
	return strings.TrimSuffix(name, "Client"), true
}

// registerService 从 RegisterXxxServer 函数名提取服务名（RegisterGreeterServer
// → Greeter）。
func registerService(name string) string {
	return strings.TrimSuffix(strings.TrimPrefix(name, "Register"), "Server")
}

// unquoteMethodPath 校验并还原字符串字面量为 gRPC 方法路径
// （/pkg.Service/Method 格式）；非路径返回空。
func unquoteMethodPath(lit string) string {
	s, err := strconv.Unquote(lit)
	if err != nil || !isGrpcMethodPath(s) {
		return ""
	}
	return s
}

// isGrpcMethodPath gRPC 方法路径格式："/<包.服务>/<方法>"。
func isGrpcMethodPath(s string) bool {
	if !strings.HasPrefix(s, "/") || strings.Count(s, "/") != 2 {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(s, "/"), "/")
	return len(parts) == 2 && parts[0] != "" && parts[1] != ""
}

// directPathIdx 方法路径在实参中的位置：方法调用 Invoke 第 2 参 /
// NewStream 第 3 参；顶层 grpc.Invoke 第 3 参。
func directPathIdx(callee *types.Func) int {
	if sig, _ := callee.Type().(*types.Signature); sig != nil && sig.Recv() == nil {
		return 2
	}
	if callee.Name() == "NewStream" {
		return 2
	}
	return 1
}

// directMethodPath 从 Invoke/NewStream 调用提取 gRPC 方法路径
// （§18.6）：字面量直接取；Ident 经一层常量传播（同函数 methodVars
// 或 types.Const）。非路径形态返回空。
func directMethodPath(pkg *packages.Package, methodVars map[string]string,
	call *ast.CallExpr, callee *types.Func) string {
	if callee == nil || (callee.Name() != "Invoke" && callee.Name() != "NewStream") {
		return ""
	}
	pathIdx := directPathIdx(callee)
	if len(call.Args) <= pathIdx {
		return ""
	}
	v := extractStringArg(pkg, methodVars, call.Args[pathIdx])
	if isGrpcMethodPath(v) {
		return v
	}
	return ""
}

// pkgOfID 从 canonical ID 提取包路径（symbol:go:<pkg>:<name>）。
func pkgOfID(id domain.CanonicalID) string {
	s := strings.TrimPrefix(string(id), "symbol:go:")
	if i := strings.LastIndex(s, ":"); i >= 0 {
		return s[:i]
	}
	return s
}

func (ctx *fileCtx) emitSelectorCall(call *ast.CallExpr, callee *types.Func, sel *ast.SelectorExpr,
	xid *ast.Ident, callerID domain.CanonicalID) {
	pkg := ctx.pkg
	// §18：gRPC 客户端方法调用 c.Method() → grpc_call 边
	// （客户端调用服务 <svc> 的 <Method>）
	if svc, okG := ctx.grpcClients[xid.Name]; okG && callee.Pkg() != nil {
		svcID := domain.CanonicalID("symbol:go:" + callee.Pkg().Path() + ":svc." + svc)
		_ = ctx.emit(domain.Item{Node: &domain.CodeEntity{
			ID:         svcID,
			Kind:       domain.KindGrpcService,
			Name:       "svc." + svc,
			Properties: map[string]any{"service_name": svc},
		}})
		_ = ctx.emit(domain.Item{Fact: &domain.Fact{
			SourceID:   callerID,
			TargetID:   svcID,
			Kind:       domain.FactGrpcCall,
			ToolSource: domain.ToolCodeGraph,
			Confidence: 1.0,
			Metadata: map[string]any{
				"method":   sel.Sel.Name,
				"line_num": pkg.Fset.PositionFor(call.Pos(), false).Line,
			},
		}})
	}
	// §18.6 手写 client：Invoke/NewStream + gRPC 方法路径
	if mp := directMethodPath(pkg, ctx.methodVars, call, callee); mp != "" {
		parts := strings.Split(strings.TrimPrefix(mp, "/"), "/")
		if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
			svcName := parts[0]
			protoPkg := svcName
			if i := strings.LastIndex(svcName, "."); i >= 0 {
				protoPkg, svcName = svcName[:i], svcName[i+1:]
			}
			svcID := domain.CanonicalID("symbol:proto:" + protoPkg + ":svc." + svcName)
			_ = ctx.emit(domain.Item{Node: &domain.CodeEntity{
				ID:         svcID,
				Kind:       domain.KindGrpcService,
				Name:       "svc." + svcName,
				Properties: map[string]any{"service_name": svcName},
			}})
			_ = ctx.emit(domain.Item{Fact: &domain.Fact{
				SourceID:   callerID,
				TargetID:   svcID,
				Kind:       domain.FactGrpcCall,
				ToolSource: domain.ToolCodeGraph,
				Confidence: 1.0,
				Metadata: map[string]any{
					"method":      parts[1],
					"method_path": mp,
					"line_num":    pkg.Fset.PositionFor(call.Pos(), false).Line,
				},
			}})
		}
	}
	// §18.7 HTTP 客户端：http.Get(url) / http.NewRequest(method, url, ...)
	// / NewRequestWithContext(ctx, method, url, ...)（P1-3 补）
	// URL 字面量+常量传播 → 路由表匹配 → http_call 边。
	// Q205d：method 按调用形态取实参（Get → GET；NewRequest → Args[0]；
	// NewRequestWithContext → Args[1]）——不得把 URL 当 method
	if url, okURL := httpURLString(pkg, ctx.methodVars, call, callee); okURL {
		ctx.emitHTTP(call, callerID, url, pkg.Fset.PositionFor(call.Pos(), false).Line,
			httpMethodOf(pkg, ctx.methodVars, call, callee))
	}
	// P1-3：client.Do(req)——req 由本函数 NewRequest 赋值（URL 已建
	// 边 → 防重复跳过；请求发出点语义仍以 NewRequest 行号为准）
	if callee != nil && callee.Name() == "Do" && len(call.Args) > 0 {
		if xid2, isID := call.Args[0].(*ast.Ident); isID {
			if url, okR := ctx.reqVars[xid2.Name]; okR && !ctx.httpURLsSeen[url] {
				m := ctx.reqMethods[xid2.Name]
				if m == "" {
					m = "GET"
				}
				ctx.emitHTTP(call, callerID, url, pkg.Fset.PositionFor(call.Pos(), false).Line, m)
			}
		}
	}
	if objID, ok := ctx.objVars[xid.Name]; ok {
		if methodID, methodKind := fnID(callee); methodID != "" {
			_ = ctx.emit(domain.Item{Node: nodeFor(ctx.repo, pkg, callee, methodID, methodKind, nil)})
			_ = ctx.emit(domain.Item{Fact: &domain.Fact{
				SourceID:   objID,
				TargetID:   methodID,
				Kind:       domain.FactUses,
				ToolSource: domain.ToolCodeGraph,
				Confidence: 0.8,
			}})
		}
	}
}
