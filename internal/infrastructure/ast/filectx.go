package ast

import (
	"go/ast"
	"go/token"
	"go/types"
	"strconv"
	"strings"

	"github.com/schaepher/codeintel/internal/domain"
	"golang.org/x/tools/go/packages"
)

// fileCtx 单文件 AST 遍历上下文（2026-08-17 拆分自 Adapter.processFile 的
// 闭包——闭包状态打包为结构体，遍历逻辑拆分为 visit/emitCall 等方法，
// 便于后续按语义继续拆分子函数）。逻辑与原闭包逐行一致。
type fileCtx struct {
	a               *Adapter
	repo            *domain.Repository
	pkg             *packages.Package
	f               *ast.File
	emit            domain.EmitFunc
	serviceFlags    map[domain.CanonicalID]map[string]bool
	registerServers map[string]bool
	newClients      map[string]string

	// 对象流追踪：变量名 → 对象 ID（同一函数内）；表达式 Pos → 对象 ID（去重）
	stack    []ast.Node
	objVars  map[string]domain.CanonicalID
	objCache map[token.Pos]domain.CanonicalID
	// gRPC 客户端对象（§18.2）：变量名 → 服务名（NewXxxClient 返回值，函数内追踪）
	grpcClients map[string]string
	// 手写 client（§18.6）：同函数内 `method := "/pkg.Svc/M"` 一层赋值链
	methodVars map[string]string
	// HTTP req 变量（P1-3）：req 名 → URL（req := http.NewRequest(...) 赋值追踪，
	// 供 client.Do(req) 消费防重复判断）
	reqVars map[string]string
	// 本函数已 emit http_call 的 URL（NewRequest 建边后，Do(req) 不重复）
	httpURLsSeen map[string]bool
	// 函数值变量（P2-1）：f := g / f := obj.Method → f 名 → *types.Func
	// （f() 调用点 callee 解析失败时查此表，unused 误报收敛）
	varFuncs map[string]*types.Func
}

// visit ast.Inspect 回调：栈管理 + 按节点类型分派。
func (ctx *fileCtx) visit(n ast.Node) bool {
	if n == nil {
		if len(ctx.stack) > 0 {
			ctx.stack = ctx.stack[:len(ctx.stack)-1]
		}
		return true
	}
	ctx.stack = append(ctx.stack, n)
	// §21.1：形参类型是模块内 XxxClient 接口 → 函数内该参数为
	// gRPC 客户端（跨函数传递：handle(c pb.GreeterClient) 内
	// c.Method() 归属服务 Greeter）
	if fd, isFD := n.(*ast.FuncDecl); isFD && fd.Type != nil && fd.Type.Params != nil {
		ctx.trackParamClients(fd)
	}
	// §21.2：grpc.ServiceDesc{ServiceName: "..."} 动态注册
	if cl, isLit := n.(*ast.CompositeLit); isLit {
		ctx.trackServiceDesc(cl)
	}
	// 变量绑定：x := &T{} / var x = &T{} / x := new(T)
	ctx.trackBindings(n)

	call, ok := n.(*ast.CallExpr)
	if !ok {
		// 非调用的初始化表达式（如 struct 字段内嵌 &T{}）：仍创建对象
		if cl, isLit := n.(*ast.CompositeLit); isLit {
			ctx.a.createObject(ctx.pkg, cl, ctx.stack, ctx.emit, ctx.repo, ctx.objCache)
		}
		return true
	}
	ctx.emitCall(call)
	return true
}

// trackParamClients §21.1：形参为模块内 XxxClient 接口 → gRPC 客户端。
func (ctx *fileCtx) trackParamClients(fd *ast.FuncDecl) {
	for _, fp := range fd.Type.Params.List {
		t := ctx.pkg.TypesInfo.TypeOf(fp.Type)
		if t == nil {
			continue
		}
		if pt, ok := t.(*types.Pointer); ok {
			t = pt.Elem()
		}
		named, ok := t.(*types.Named)
		if !ok || named.Obj() == nil || named.Obj().Pkg() == nil {
			continue
		}
		if !isInModule(named.Obj().Pkg().Path(), ctx.repo.Modules) {
			continue
		}
		if svc, ok2 := clientTypeService(named.Obj().Name()); ok2 {
			for _, pn := range fp.Names {
				ctx.grpcClients[pn.Name] = svc
			}
		}
	}
}

// trackServiceDesc §21.2：grpc.ServiceDesc{ServiceName: "..."} 动态注册
// → svc 节点（无静态实现的服务）。
func (ctx *fileCtx) trackServiceDesc(cl *ast.CompositeLit) {
	clt := ctx.pkg.TypesInfo.TypeOf(cl)
	if clt != nil {
		if pt, ok := clt.(*types.Pointer); ok {
			clt = pt.Elem()
		}
		if named, ok := clt.(*types.Named); ok && named.Obj() != nil && named.Obj().Name() == "ServiceDesc" {
			for _, el := range cl.Elts {
				kv, ok := el.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				id, ok := kv.Key.(*ast.Ident)
				if !ok || id.Name != "ServiceName" {
					continue
				}
				bl, ok := kv.Value.(*ast.BasicLit)
				if !ok || bl.Kind != token.STRING {
					continue
				}
				svcName, err := strconv.Unquote(bl.Value)
				if err != nil || !strings.Contains(svcName, ".") {
					continue
				}
				protoPkg, name := svcName, svcName
				if i := strings.LastIndex(svcName, "."); i >= 0 {
					protoPkg, name = svcName[:i], svcName[i+1:]
				}
				svcID := domain.CanonicalID("symbol:proto:" + protoPkg + ":svc." + name)
				_ = ctx.emit(domain.Item{Node: &domain.CodeEntity{
					ID:   svcID,
					Kind: domain.KindGrpcService,
					Name: "svc." + name,
					Properties: map[string]any{
						"service_name": name,
						"service_desc": "true", // 动态注册（无静态实现）
					},
				}})
			}
		}
	}
}

// trackBindings 变量绑定段：x := &T{} / var x = &T{} / x := new(T)、
// 函数值变量（P2-1）、gRPC 客户端（§18）、req URL（P1-3）、
// method 常量（§18.6）。
func (ctx *fileCtx) trackBindings(n ast.Node) {
	if assign, isAssign := n.(*ast.AssignStmt); isAssign && assign.Tok == token.DEFINE &&
		len(assign.Lhs) == 1 && len(assign.Rhs) == 1 {
		if id, isID := assign.Lhs[0].(*ast.Ident); isID {
			if objID, ok := ctx.a.createObject(ctx.pkg, assign.Rhs[0], ctx.stack, ctx.emit, ctx.repo, ctx.objCache); ok {
				ctx.objVars[id.Name] = objID
			}
			// P2-1：f := g / f := obj.Method → 函数值变量（f() 调用解析用）
			if fnRef, isFn := funcValueRef(ctx.pkg, assign.Rhs[0]); isFn {
				ctx.varFuncs[id.Name] = fnRef
			}
			// §18：c := pb.NewXxxClient(conn) → gRPC 客户端对象（服务名）
			if call, isCall := assign.Rhs[0].(*ast.CallExpr); isCall {
				if cc, ok2 := resolveCallee(ctx.pkg.TypesInfo, call.Fun); ok2 {
					if cc.Pkg() != nil {
						if svc, ok3 := ctx.newClients[cc.Pkg().Path()+":"+cc.Name()]; ok3 {
							ctx.grpcClients[id.Name] = svc
						}
					}
					// P1-3：req := http.NewRequest(...) → req 变量 URL 追踪
					// （URL 提取含常量拼接；client.Do(req) 消费时防重复判断）
					if url, okU := httpURLString(ctx.pkg, ctx.methodVars, call, cc); okU {
						ctx.reqVars[id.Name] = url
					}
				}
			}
			// §18.6：method := "/pkg.Svc/M" 一层赋值链（常量传播）
			if bl, isLit := assign.Rhs[0].(*ast.BasicLit); isLit && bl.Kind == token.STRING {
				if mp := unquoteMethodPath(bl.Value); mp != "" {
					ctx.methodVars[id.Name] = mp
				}
			}
		}
	}
	if decl, isDecl := n.(*ast.GenDecl); isDecl && decl.Tok == token.VAR {
		for _, spec := range decl.Specs {
			vs, isVS := spec.(*ast.ValueSpec)
			if !isVS || len(vs.Names) != 1 || len(vs.Values) != 1 {
				continue
			}
			// Q108：包级 var 初始化调用（var x = NewFoo()）建 calls 边——
			// 此前跳过（callerDecl==nil），构造函数被误报"未调用"。
			// source = 包节点（保证端点存在；语义：包级初始化调用）
			if call, isCall := vs.Values[0].(*ast.CallExpr); isCall {
				if callee, ok2 := resolveCallee(ctx.pkg.TypesInfo, call.Fun); ok2 &&
					callee.Pkg() != nil && isInModule(callee.Pkg().Path(), ctx.repo.Modules) {
					calleeID, calleeKind := fnID(callee)
					if calleeID != "" {
						_ = ctx.emit(domain.Item{Node: nodeFor(ctx.repo, ctx.pkg, callee, calleeID, calleeKind, nil)})
						_ = ctx.emit(domain.Item{Fact: &domain.Fact{
							SourceID:   packageID(ctx.pkg.PkgPath),
							TargetID:   calleeID,
							Kind:       domain.FactCalls,
							ToolSource: domain.ToolCodeGraph,
							Confidence: 0.8,
							Metadata:   map[string]any{"line_num": ctx.pkg.Fset.PositionFor(call.Pos(), false).Line},
						}})
					}
				}
			}
			if objID, ok := ctx.a.createObject(ctx.pkg, vs.Values[0], ctx.stack, ctx.emit, ctx.repo, ctx.objCache); ok {
				ctx.objVars[vs.Names[0].Name] = objID
			}
		}
	}
}
