package ast

import (
	"go/ast"
	"go/token"
	"go/types"
	"strings"

	"github.com/schaepher/codeintel/internal/canonicalizer"
	"github.com/schaepher/codeintel/internal/domain"
	"go.uber.org/zap"
	"golang.org/x/tools/go/packages"
)

// markHTTPHandlers 标记实现 net/http.Handler 接口（ServeHTTP 方法）的项目内
// 类型，作为 HTTP 服务入口（serves_http）。
func (a *Adapter) markHTTPHandlers(repo *domain.Repository, pkg *packages.Package, emit domain.EmitFunc) error {
	// 从导入中找 net/http.Handler 接口
	var handlerIface *types.Interface
	for _, imp := range pkg.Imports {
		if imp.PkgPath != "net/http" && !strings.HasPrefix(imp.PkgPath, "net/http/") {
			continue
		}
		if obj := imp.Types.Scope().Lookup("Handler"); obj != nil {
			if iface, ok := obj.Type().Underlying().(*types.Interface); ok {
				handlerIface = iface
				break
			}
		}
	}
	if handlerIface == nil {
		return nil
	}
	scope := pkg.Types.Scope()
	for _, name := range scope.Names() {
		tn, ok := scope.Lookup(name).(*types.TypeName)
		if !ok {
			continue
		}
		named, ok := tn.Type().(*types.Named)
		if !ok {
			continue
		}

		if !types.Implements(named, handlerIface) && !types.Implements(types.NewPointer(named), handlerIface) {
			continue
		}
		obj := named.Obj()
		if obj.Pkg() == nil || !isInModule(obj.Pkg().Path(), repo.Modules) {
			continue
		}
		pos := pkg.Fset.PositionFor(obj.Pos(), false)
		if err := emit(domain.Item{Node: &domain.CodeEntity{
			ID:        canonicalizer.GoSymbolID(obj.Pkg().Path(), obj.Name()),
			Kind:      domain.KindStruct,
			Name:      obj.Name(),
			FilePath:  relPath(repo.Path, pos.Filename),
			LineStart: pos.Line,
			LineEnd:   pos.Line,
			Properties: map[string]any{
				"serves_http": "true",
			},
		}}); err != nil {
			return err
		}
	}
	return nil
}

// emitStructFields 为文件内每个 struct 类型声明写入字段列表
// （properties.fields = [{"name","type"}...]，类型用 go/types 相对路径
// 字符串如 *domain.BuildMeta）。信息栏据此以表格展示字段。
func (a *Adapter) emitStructFields(repo *domain.Repository, pkg *packages.Package, f *ast.File, emit domain.EmitFunc) error {
	logger := zap.L()
	logger.Debug("enter (Adapter).emitStructFields")
	defer logger.Debug("exit (Adapter).emitStructFields")
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				continue
			}
			named, ok := pkg.TypesInfo.TypeOf(ts.Name).(*types.Named)
			if !ok {
				continue
			}
			if named.Obj().Pkg() == nil || !isInModule(named.Obj().Pkg().Path(), repo.Modules) {
				continue
			}
			fields := []map[string]any{}

			qual := func(p *types.Package) string {
				if p == nil || p.Path() == pkg.PkgPath {
					return ""
				}
				return p.Name()
			}
			for _, fld := range st.Fields.List {
				ft := pkg.TypesInfo.TypeOf(fld.Type)
				if ft == nil {
					continue
				}
				ts := types.TypeString(ft, qual)
				if len(fld.Names) == 0 {

					fields = append(fields, map[string]any{"name": embeddedTypeName(ft), "type": ts})
					continue
				}
				for _, n := range fld.Names {
					fields = append(fields, map[string]any{"name": n.Name, "type": ts})
				}
			}
			if len(fields) == 0 {
				continue
			}
			pos := pkg.Fset.PositionFor(ts.Pos(), false)
			_ = emit(domain.Item{Node: &domain.CodeEntity{
				ID:        canonicalizer.GoSymbolID(named.Obj().Pkg().Path(), named.Obj().Name()),
				Kind:      domain.KindStruct,
				Name:      named.Obj().Name(),
				FilePath:  relPath(repo.Path, pos.Filename),
				LineStart: pos.Line,
				LineEnd:   pos.Line,
				Properties: map[string]any{
					"fields": fields,
				},
			}})
		}
	}
	return nil
}

// isRegisterServerName 判断函数名是否匹配 protoc 生成惯例 RegisterXxxServer。
func isRegisterServerName(name string) bool {
	return len(name) > len("RegisterServer") &&
		strings.HasPrefix(name, "Register") &&
		strings.HasSuffix(name, "Server")
}

// handlerFuncNode 提取 http.Handle/HandleFunc 的 handler 参数（第二个参数），
// 支持形态：
//
//	http.Handle("/", myHandler)          // 变量（具名函数）
//	http.Handle("/", http.HandlerFunc(f)) // HandlerFunc 包装
//	http.HandleFunc("/", home)            // 具名函数
//
// 返回标记 serves_http 的节点；匿名函数（FuncLit）与外部函数返回 nil。
func handlerFuncNode(pkg *packages.Package, call *ast.CallExpr, repo *domain.Repository) *domain.CodeEntity {
	if len(call.Args) < 2 {
		return nil
	}
	arg := call.Args[1]

	if ce, ok := arg.(*ast.CallExpr); ok {
		if sel, ok2 := ce.Fun.(*ast.SelectorExpr); ok2 && sel.Sel.Name == "HandlerFunc" && len(ce.Args) > 0 {
			arg = ce.Args[0]
		}
	}
	id, ok := arg.(*ast.Ident)
	if !ok {
		return nil
	}
	obj := pkg.TypesInfo.Uses[id]
	if obj == nil {
		obj = pkg.TypesInfo.Defs[id]
	}
	fn, ok := obj.(*types.Func)
	if !ok || fn.Pkg() == nil || !isInModule(fn.Pkg().Path(), repo.Modules) {
		return nil
	}
	fnID, fnKind := fnID(fn)
	if fnID == "" {
		return nil
	}
	return nodeFor(repo, pkg, fn, fnID, fnKind, map[string]bool{"serves_http": true})
}

// serviceImplNode 提取 RegisterXxxServer 调用的第二个参数（服务实现），
// 生成标记 serves_grpc 的节点（作为顶层服务入口）。参数形态支持：
//
//	pb.RegisterGreeterServer(s, &greeterImpl{})   // 复合字面量
//	pb.RegisterGreeterServer(s, newGreeterServer()) // 构造函数
//	pb.RegisterGreeterServer(s, impl)               // 变量
//
// 返回 nil 表示无法解析为项目内类型。
func serviceImplNode(pkg *packages.Package, call *ast.CallExpr, repo *domain.Repository) *domain.CodeEntity {
	if len(call.Args) < 2 {
		return nil
	}
	t := pkg.TypesInfo.TypeOf(call.Args[1])
	if t == nil {
		return nil
	}
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok {
		return nil
	}
	obj := named.Obj()
	if obj.Pkg() == nil || !isInModule(obj.Pkg().Path(), repo.Modules) {
		return nil
	}
	pos := pkg.Fset.PositionFor(obj.Pos(), false)
	return &domain.CodeEntity{
		ID:        canonicalizer.GoSymbolID(obj.Pkg().Path(), obj.Name()),
		Kind:      domain.KindStruct,
		Name:      obj.Name(),
		FilePath:  relPath(repo.Path, pos.Filename),
		LineStart: pos.Line,
		LineEnd:   pos.Line,
		Properties: map[string]any{
			"serves_grpc": "true",
		},
	}
}

func (ctx *fileCtx) markServiceEntry(call *ast.CallExpr, callee *types.Func,
	caller *types.Func, callerID domain.CanonicalID, callerKind domain.EntityKind) {
	pkg := ctx.pkg
	_ = pkg
	p := callee.Pkg().Path()
	flags := ctx.serviceFlags[callerID]
	if flags == nil {
		flags = map[string]bool{}
		ctx.serviceFlags[callerID] = flags
	}
	marked := false
	if p == "net/http" || strings.HasPrefix(p, "net/http/") {
		if !flags["serves_http"] {
			flags["serves_http"] = true
			marked = true
		}
	}
	if p == "google.golang.org/grpc" || strings.HasPrefix(p, "google.golang.org/grpc/") {
		if !flags["serves_grpc"] {
			flags["serves_grpc"] = true
			marked = true
		}
	}
	// HTTP handler 函数：http.Handle / http.HandleFunc / mux.Handle 的
	// handler 参数（具名函数或 http.HandlerFunc(f) 包装），作为入口
	if p == "net/http" && (callee.Name() == "Handle" || callee.Name() == "HandleFunc") {
		if hf := handlerFuncNode(pkg, call, ctx.repo); hf != nil {
			if err := ctx.emit(domain.Item{Node: hf}); err != nil {
				return
			}
		}
	}
	// gRPC 服务注册：protoc 生成的 RegisterXxxServer（定义在 .pb.go），
	// 其第二个参数是服务实现，作为顶层服务入口
	if ctx.registerServers[callee.Pkg().Path()+":"+callee.Name()] {
		if !flags["serves_grpc"] {
			flags["serves_grpc"] = true
			marked = true
		}
		// §18：grpc_service 节点 + grpc_impl 边（实现类型 → 服务）
		svcID := domain.CanonicalID("symbol:go:" + callee.Pkg().Path() + ":svc." + registerService(callee.Name()))
		_ = ctx.emit(domain.Item{Node: &domain.CodeEntity{
			ID:         svcID,
			Kind:       domain.KindGrpcService,
			Name:       "svc." + registerService(callee.Name()),
			Properties: map[string]any{"service_name": registerService(callee.Name())},
		}})
		if impl := serviceImplNode(pkg, call, ctx.repo); impl != nil {
			if err := ctx.emit(domain.Item{Node: impl}); err != nil {
				return
			}
			_ = ctx.emit(domain.Item{Fact: &domain.Fact{
				SourceID:   impl.ID,
				TargetID:   svcID,
				Kind:       domain.FactGrpcImpl,
				ToolSource: domain.ToolCodeGraph,
				Confidence: 1.0,
			}})
		}
	}
	if marked {
		if err := ctx.emit(domain.Item{Node: nodeFor(ctx.repo, pkg, caller, callerID, callerKind, ctx.serviceFlags[callerID])}); err != nil {
			return
		}
	}
}
