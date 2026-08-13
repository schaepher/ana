// Package ast 实现调用图适配器（对应 TD.md 的 CodeGraph 适配器角色，置信度 0.8）。
// 基于 golang.org/x/tools/go/packages 的 AST + 类型信息，纯 Go 无外部进程：
//   - CALLS 边：调用者函数 → 被调用函数/方法（精确调用点）
//   - IMPORTS 边：包 → 直接依赖的项目内包
package ast

import (
	"context"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/packages"

	"github.com/schaepher/codeintel/internal/canonicalizer"
	"github.com/schaepher/codeintel/internal/domain"
	"github.com/schaepher/codeintel/internal/logging"
	"go.uber.org/zap"
)

// Adapter 是基于 go/packages 的调用图分析器。
type Adapter struct{}

var _ domain.IndexerPort = (*Adapter)(nil)

// Name 实现 IndexerPort。
func (a *Adapter) Name() string {
	logger := zap.L()
	logger.Debug("enter (Adapter).Name")
	defer logger.Debug("exit (Adapter).Name")
	return "codegraph"
}

// Index 加载仓库全部包并产出 CALLS / IMPORTS 边。
func (a *Adapter) Index(ctx context.Context, repo *domain.Repository, emit domain.EmitFunc) error {
	logger := logging.FromContext(ctx)
	logger.Debug("enter (Adapter).Index")
	defer logger.Debug("exit (Adapter).Index")
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax |
			packages.NeedTypes | packages.NeedTypesInfo | packages.NeedImports | packages.NeedDeps,
		Dir: repo.Path,
		// Tests 默认 false：不加载 _test.go（测试符号不入图）
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return fmt.Errorf("go/packages load: %w", err)
	}
	packages.PrintErrors(pkgs) // 诊断信息打到 stderr，不中断

	// 服务入口标记：函数若调用 net/http 或 grpc 包，标记 serves_http / serves_grpc
	serviceFlags := map[domain.CanonicalID]map[string]bool{}
	// 预收集 .pb.go 中定义的 RegisterXxxServer（gRPC 注册函数）。
	// 注意：不能用调用点所在包的 Fset 解析被调用函数的位置（跨包偏移
	// 不匹配），须在各自包内收集。
	registerServers := collectRegisterServers(pkgs, repo.Module)

	for _, pkg := range pkgs {
		if !isInModule(pkg.PkgPath, repo.Module) {
			continue // 仅处理项目内包
		}
		if err := a.processPackage(repo, pkg, emit, serviceFlags, registerServers); err != nil {
			return err
		}
	}
	return nil
}

// collectRegisterServers 遍历项目内包，收集定义在 .pb.go 中、函数名匹配
// RegisterXxxServer 的注册函数（key: "pkgPath:funcName"）。
func collectRegisterServers(pkgs []*packages.Package, module string) map[string]bool {
	out := map[string]bool{}
	for _, pkg := range pkgs {
		if !isInModule(pkg.PkgPath, module) {
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

func (a *Adapter) processPackage(repo *domain.Repository, pkg *packages.Package, emit domain.EmitFunc,
	serviceFlags map[domain.CanonicalID]map[string]bool, registerServers map[string]bool) error {
	logger := zap.L()
	logger.Debug("enter (Adapter).processPackage")
	defer logger.Debug("exit (Adapter).processPackage")
	if err := ensurePackageNode(repo, pkg, emit); err != nil {
		return err
	}

	// HTTP handler 类型级检测：实现 net/http.Handler 接口的类型作为入口
	if err := a.markHTTPHandlers(repo, pkg, emit); err != nil {
		return err
	}

	// IMPORTS 边：直接依赖的项目内包
	for importPath := range pkg.Imports {
		if !isInModule(importPath, repo.Module) {
			continue
		}
		if err := emit(domain.Item{Fact: &domain.Fact{
			SourceID:   packageID(pkg.PkgPath),
			TargetID:   packageID(importPath),
			Kind:       domain.FactImports,
			ToolSource: domain.ToolCodeGraph,
			Confidence: 0.8,
		}}); err != nil {
			return err
		}
	}

	for _, f := range pkg.Syntax {
		if err := a.processFile(repo, pkg, f, emit, serviceFlags, registerServers); err != nil {
			return err
		}
	}
	return nil
}

// processFile 遍历单个 AST：定位每个调用点，连接调用者与被调用者。
func (a *Adapter) processFile(repo *domain.Repository, pkg *packages.Package, f *ast.File, emit domain.EmitFunc,
	serviceFlags map[domain.CanonicalID]map[string]bool, registerServers map[string]bool) error {
	logger := zap.L()
	logger.Debug("enter (Adapter).processFile")
	defer logger.Debug("exit (Adapter).processFile")
	filePath := relPath(repo.Path, pkg.Fset.PositionFor(f.Pos(), false).Filename)
	if filePath == "" {
		return nil
	}

	if err := a.emitMethodReceiver(repo, pkg, f, emit); err != nil {
		return err
	}
	if err := a.emitStructFields(repo, pkg, f, emit); err != nil {
		return err
	}

	var stack []ast.Node
	// 对象流追踪：变量名 → 对象 ID（同一函数内）；表达式 Pos → 对象 ID（去重）
	objVars := map[string]domain.CanonicalID{}
	objCache := map[token.Pos]domain.CanonicalID{}

	ast.Inspect(f, func(n ast.Node) bool {
		if n == nil {
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			return true
		}
		stack = append(stack, n)

		// 变量绑定：x := &T{} / var x = &T{} / x := new(T)
		if assign, isAssign := n.(*ast.AssignStmt); isAssign && assign.Tok == token.DEFINE &&
			len(assign.Lhs) == 1 && len(assign.Rhs) == 1 {
			if id, isID := assign.Lhs[0].(*ast.Ident); isID {
				if objID, ok := a.createObject(pkg, assign.Rhs[0], stack, emit, repo, objCache); ok {
					objVars[id.Name] = objID
				}
			}
		}
		if decl, isDecl := n.(*ast.GenDecl); isDecl && decl.Tok == token.VAR {
			for _, spec := range decl.Specs {
				vs, isVS := spec.(*ast.ValueSpec)
				if !isVS || len(vs.Names) != 1 || len(vs.Values) != 1 {
					continue
				}
				if objID, ok := a.createObject(pkg, vs.Values[0], stack, emit, repo, objCache); ok {
					objVars[vs.Names[0].Name] = objID
				}
			}
		}

		call, ok := n.(*ast.CallExpr)
		if !ok {
			// 非调用的初始化表达式（如 struct 字段内嵌 &T{}）：仍创建对象
			if cl, isLit := n.(*ast.CompositeLit); isLit {
				a.createObject(pkg, cl, stack, emit, repo, objCache)
			}
			return true
		}
		// 内建 new(T)：callee 解析失败（builtin 无 Pkg）时单独处理
		callee, ok := resolveCallee(pkg.TypesInfo, call.Fun)
		if !ok {
			if id, isID := call.Fun.(*ast.Ident); isID && id.Name == "new" && len(call.Args) == 1 {
				a.createObject(pkg, call, stack, emit, repo, objCache)
			}
			return true
		}
		callerDecl := findCallerDecl(stack)
		if callerDecl == nil {
			return true // 包级初始化中的调用，MVP 不建边
		}
		caller, ok := pkg.TypesInfo.Defs[callerDecl.Name].(*types.Func)
		if !ok {
			return true
		}
		callerID, callerKind := fnID(caller)
		if callerID == "" {
			return true
		}
		// 参数位置的调用（如 A(B(C())) 里的 B(C())）：由外层调用处理为
		// "持有返回参数"链，不建 calls
		if isArgCall(stack, call) {
			return true
		}

		// 对象使用处：x.Method()（x 是初始化的对象）→ uses 边（对象 → 方法）
		if sel, isSel := call.Fun.(*ast.SelectorExpr); isSel {
			if xid, isID := sel.X.(*ast.Ident); isID {
				if objID, ok := objVars[xid.Name]; ok {
					if methodID, methodKind := fnID(callee); methodID != "" {
						_ = emit(domain.Item{Node: nodeFor(repo, pkg, callee, methodID, methodKind, nil)})
						_ = emit(domain.Item{Fact: &domain.Fact{
							SourceID:   objID,
							TargetID:   methodID,
							Kind:       domain.FactUses,
							ToolSource: domain.ToolCodeGraph,
							Confidence: 0.8,
						}})
					}
				}
			}
		}
		// 对象去处：f(x) / f(&T{}) → passes_to 边（对象 → 接收函数）
		if callee.Pkg() != nil && isInModule(callee.Pkg().Path(), repo.Module) {
			calleeID, calleeKind := fnID(callee)
			// 参数位置的嵌套调用：接收者持有返回参数（A(B(C)) → A→B、B→C）
			for _, arg := range call.Args {
				if inner, isCall := arg.(*ast.CallExpr); isCall {
					a.handleNestedArg(pkg, inner, calleeID, emit, repo)
				}
			}
			for _, arg := range call.Args {
				var objID domain.CanonicalID
				var ok2 bool
				if xid, isID := arg.(*ast.Ident); isID {
					objID, ok2 = objVars[xid.Name]
				}
				if !ok2 {
					objID, ok2 = a.createObject(pkg, arg, stack, emit, repo, objCache)
				}
				if ok2 && calleeID != "" && objID != calleeID {
					_ = emit(domain.Item{Node: nodeFor(repo, pkg, callee, calleeID, calleeKind, nil)})
					_ = emit(domain.Item{Fact: &domain.Fact{
						// 方向：接收函数 → 参数（用户确认：接收者指向参数）
						SourceID:   calleeID,
						TargetID:   objID,
						Kind:       domain.FactPassesTo,
						ToolSource: domain.ToolCodeGraph,
						Confidence: 0.8,
					}})
				}
			}
		}
		// 服务入口标记：调用 net/http / grpc 包的函数作为顶层入口。
		// 注意：外部调用点随后会直接 return（不建 CALLS 边），因此标记
		// fires 时必须立即 emit 节点，否则节点永远不会带上标记。
		if callee.Pkg() != nil {
			p := callee.Pkg().Path()
			flags := serviceFlags[callerID]
			if flags == nil {
				flags = map[string]bool{}
				serviceFlags[callerID] = flags
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
				if hf := handlerFuncNode(pkg, call, repo); hf != nil {
					if err := emit(domain.Item{Node: hf}); err != nil {
						return false
					}
				}
			}
			// gRPC 服务注册：protoc 生成的 RegisterXxxServer（定义在 .pb.go），
			// 其第二个参数是服务实现，作为顶层服务入口
			if registerServers[callee.Pkg().Path()+":"+callee.Name()] {
				if !flags["serves_grpc"] {
					flags["serves_grpc"] = true
					marked = true
				}
				if impl := serviceImplNode(pkg, call, repo); impl != nil {
					if err := emit(domain.Item{Node: impl}); err != nil {
						return false
					}
				}
			}
			if marked {
				if err := emit(domain.Item{Node: nodeFor(repo, pkg, caller, callerID, callerKind, serviceFlags[callerID])}); err != nil {
					return false
				}
			}
		}
		calleeID, calleeKind := fnID(callee)
		// 函数作为参数传入（回调）：参数函数 → 接收函数（passes_to）。
		// 接收者可为外部框架函数（如 net/http.HandleFunc），为其建轻量节点
		// （file_path 为空），使"作为谁的参数"关系可见。须在外部函数
		// 跳过逻辑之前处理。
		if calleeID != "" && calleeID != callerID {
			for _, arg := range call.Args {
				fn := argFuncRef(pkg, arg)
				if fn == nil || fn.Pkg() == nil || !isInModule(fn.Pkg().Path(), repo.Module) {
					continue // 参数函数必须是项目内符号（有节点）
				}
				paramID, paramKind := fnID(fn)
				if paramID == "" || paramID == calleeID {
					continue
				}
				_ = emit(domain.Item{Node: nodeFor(repo, pkg, fn, paramID, paramKind, nil)})
				_ = emit(domain.Item{Node: nodeFor(repo, pkg, callee, calleeID, calleeKind, nil)})
				_ = emit(domain.Item{Fact: &domain.Fact{
					// 方向：接收函数 → 参数函数（用户确认：run -参数→ callback）
					SourceID:   calleeID,
					TargetID:   paramID,
					Kind:       domain.FactPassesTo,
					ToolSource: domain.ToolCodeGraph,
					Confidence: 0.8,
				}})
			}
		}
		if callee.Pkg() == nil || !isInModule(callee.Pkg().Path(), repo.Module) {
			return true // 内建/外部函数不建边
		}
		if calleeID == "" || calleeID == callerID {
			return true
		}
		if isInterfaceMethod(callee) {
			// 接口方法不作为独立节点（用户确认）。但链式调用场景
			// （NewService().DoSth()）仍要建调用边：静态分析接收者
			// 表达式的实际类型——NewService 返回接口但 return 具体
			// 类型 → 指向具体类型的实现方法；无法确定 → 指向接口类型
			_ = emit(domain.Item{Node: nodeFor(repo, pkg, caller, callerID, callerKind, serviceFlags[callerID])})
			targetID, targetKind, targetNode := a.concreteMethodFor(pkg, call, callee, repo)
			if targetID == "" {
				return true
			}
			if targetNode != nil {
				_ = emit(domain.Item{Node: targetNode})
			}
			_ = emit(domain.Item{Fact: &domain.Fact{
				SourceID:   callerID,
				TargetID:   targetID,
				Kind:       domain.FactCalls,
				ToolSource: domain.ToolCodeGraph,
				Confidence: 0.8,
				Metadata:   map[string]any{"line_num": pkg.Fset.PositionFor(call.Pos(), false).Line},
			}})
			_ = targetKind
			return true
		}
		// 保障两端节点存在（INSERT OR IGNORE，不覆盖 SCIP 的完整节点）
		if err := emit(domain.Item{Node: nodeFor(repo, pkg, caller, callerID, callerKind, serviceFlags[callerID])}); err != nil {
			return false
		}
		if err := emit(domain.Item{Node: nodeFor(repo, pkg, callee, calleeID, calleeKind, nil)}); err != nil {
			return false
		}
		if err := emit(domain.Item{Fact: &domain.Fact{
			SourceID:   callerID,
			TargetID:   calleeID,
			Kind:       domain.FactCalls,
			ToolSource: domain.ToolCodeGraph,
			Confidence: 0.8,
			Metadata:   map[string]any{"line_num": pkg.Fset.PositionFor(call.Pos(), false).Line},
		}}); err != nil {
			return false
		}
		return true
	})
	return nil
}

// argFuncRef 将调用参数解析为函数引用（作为参数传入的回调）：
//
//	Ident（home）                          → 具名函数
//	SelectorExpr（s.PageHome / pkg.F）     → 方法/包函数引用
//	CallExpr（http.HandlerFunc(home)）     → 解包 HandlerFunc 包装
//
// 非函数引用（变量/字面量/匿名函数）返回 nil。
func argFuncRef(pkg *packages.Package, arg ast.Expr) *types.Func {
	var obj types.Object
	switch a := arg.(type) {
	case *ast.Ident:
		obj = pkg.TypesInfo.Uses[a]
	case *ast.SelectorExpr:
		obj = pkg.TypesInfo.Uses[a.Sel]
	case *ast.CallExpr:
		// http.HandlerFunc(f) 包装解包
		if sel, ok := a.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "HandlerFunc" && len(a.Args) == 1 {
			if id, ok := a.Args[0].(*ast.Ident); ok {
				obj = pkg.TypesInfo.Uses[id]
			}
		}
	}
	if obj == nil {
		return nil
	}
	fn, _ := obj.(*types.Func)
	return fn
}

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
		// 值或指针接收者实现均可（方法可能在 *T 上）
		if !types.Implements(named, handlerIface) && !types.Implements(types.NewPointer(named), handlerIface) {
			continue
		}
		obj := named.Obj()
		if obj.Pkg() == nil || !isInModule(obj.Pkg().Path(), repo.Module) {
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
				continue // 别名（type T = struct{...}）无具名类型
			}
			if named.Obj().Pkg() == nil || !isInModule(named.Obj().Pkg().Path(), repo.Module) {
				continue
			}
			fields := []map[string]any{}
			// 短类型名：本包不加前缀，其他包用包名（如 *domain.Repository）
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
					// 匿名嵌入字段：用其类型名
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

// embeddedTypeName 匿名嵌入字段的显示名（解引用指针取具名类型名）。
func embeddedTypeName(t types.Type) string {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	if n, ok := t.(*types.Named); ok {
		return n.Obj().Name()
	}
	return types.TypeString(t, nil)
}

// emitMethodReceiver 为文件内每个带 receiver 的方法声明建立 has_method 边
// （方法线：接收者类型 → 方法）。接收者类型节点如不存在则创建（与
// createObject 相同的轻量节点模式，SCIP 已建则 UPSERT 合并属性）。
// 展开接收者（struct）节点时前端即可连线到它的方法们。
func (a *Adapter) emitMethodReceiver(repo *domain.Repository, pkg *packages.Package, f *ast.File, emit domain.EmitFunc) error {
	logger := zap.L()
	logger.Debug("enter (Adapter).emitMethodReceiver")
	defer logger.Debug("exit (Adapter).emitMethodReceiver")
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || len(fn.Recv.List) == 0 {
			continue
		}
		method, ok := pkg.TypesInfo.Defs[fn.Name].(*types.Func)
		if !ok {
			continue
		}
		methodID, _ := fnID(method)
		if methodID == "" {
			continue
		}
		t := pkg.TypesInfo.TypeOf(fn.Recv.List[0].Type)
		if p, ok := t.(*types.Pointer); ok {
			t = p.Elem()
		}
		named, ok := t.(*types.Named)
		if !ok {
			continue // 匿名结构体上的方法
		}
		if named.Obj().Pkg() == nil || !isInModule(named.Obj().Pkg().Path(), repo.Module) {
			continue
		}
		recvID := canonicalizer.GoSymbolID(named.Obj().Pkg().Path(), named.Obj().Name())
		if recvID == methodID {
			continue
		}
		// 保障接收者类型节点存在
		tpos := pkg.Fset.PositionFor(named.Obj().Pos(), false)
		_ = emit(domain.Item{Node: &domain.CodeEntity{
			ID:        recvID,
			Kind:      domain.KindStruct,
			Name:      named.Obj().Name(),
			FilePath:  relPath(repo.Path, tpos.Filename),
			LineStart: tpos.Line,
			LineEnd:   tpos.Line,
		}})
		_ = emit(domain.Item{Fact: &domain.Fact{
			SourceID:   recvID, // 方法线方向：接收者 → 方法
			TargetID:   methodID,
			Kind:       domain.FactHasMethod,
			ToolSource: domain.ToolCodeGraph,
			Confidence: 0.8,
		}})
	}
	return nil
}

// createObject 将初始化表达式（&T{} / T{} / new(T)）解析为 struct 类型：
//   - initializes 边：初始化者函数 → struct 类型（对象合并到类型节点，
//     不建独立 object 节点，避免同一类型的实例在图里分开）
//
// 返回类型 ID（作为实例的代表）；非 struct 初始化 / 外部类型 / 无 caller
// 时返回 false。
func (a *Adapter) createObject(pkg *packages.Package, expr ast.Expr, stack []ast.Node, emit domain.EmitFunc,
	repo *domain.Repository, cache map[token.Pos]domain.CanonicalID) (domain.CanonicalID, bool) {
	var t types.Type
	switch e := expr.(type) {
	case *ast.UnaryExpr: // &T{}
		if cl, ok := e.X.(*ast.CompositeLit); ok {
			t = pkg.TypesInfo.TypeOf(cl)
		}
	case *ast.CompositeLit: // T{}
		t = pkg.TypesInfo.TypeOf(e)
	case *ast.CallExpr: // new(T)
		if id, ok := e.Fun.(*ast.Ident); ok && id.Name == "new" && len(e.Args) == 1 {
			t = pkg.TypesInfo.TypeOf(e.Args[0])
		}
	}
	if t == nil {
		return "", false
	}
	if cached, ok := cache[expr.Pos()]; ok {
		return cached, true
	}
	callerDecl := findCallerDecl(stack)
	if callerDecl == nil {
		return "", false // 包级初始化，MVP 不追踪
	}
	caller, ok := pkg.TypesInfo.Defs[callerDecl.Name].(*types.Func)
	if !ok {
		return "", false
	}
	callerID, _ := fnID(caller)
	if callerID == "" {
		return "", false
	}
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok {
		return "", false
	}
	if _, ok := named.Underlying().(*types.Struct); !ok {
		return "", false // 仅 struct（排除 map/slice 等复合字面量）
	}
	if named.Obj().Pkg() == nil || !isInModule(named.Obj().Pkg().Path(), repo.Module) {
		return "", false
	}
	structID := canonicalizer.GoSymbolID(named.Obj().Pkg().Path(), named.Obj().Name())
	if structID == callerID {
		return "", false
	}
	// 保障类型节点存在 + initializes 边（初始化者 → 类型）
	tpos := pkg.Fset.PositionFor(named.Obj().Pos(), false)
	_ = emit(domain.Item{Node: &domain.CodeEntity{
		ID:        structID,
		Kind:      domain.KindStruct,
		Name:      named.Obj().Name(),
		FilePath:  relPath(repo.Path, tpos.Filename),
		LineStart: tpos.Line,
		LineEnd:   tpos.Line,
	}})
	_ = emit(domain.Item{Fact: &domain.Fact{
		SourceID:   callerID,
		TargetID:   structID,
		Kind:       domain.FactInitializes,
		ToolSource: domain.ToolCodeGraph,
		Confidence: 0.8,
	}})
	cache[expr.Pos()] = structID
	return structID, true
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
	// 解包 http.HandlerFunc(f)
	if ce, ok := arg.(*ast.CallExpr); ok {
		if sel, ok2 := ce.Fun.(*ast.SelectorExpr); ok2 && sel.Sel.Name == "HandlerFunc" && len(ce.Args) > 0 {
			arg = ce.Args[0]
		}
	}
	id, ok := arg.(*ast.Ident)
	if !ok {
		return nil // 匿名函数/复合字面量等（类型级检测已覆盖 struct 实现）
	}
	obj := pkg.TypesInfo.Uses[id]
	if obj == nil {
		obj = pkg.TypesInfo.Defs[id]
	}
	fn, ok := obj.(*types.Func)
	if !ok || fn.Pkg() == nil || !isInModule(fn.Pkg().Path(), repo.Module) {
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
		return nil // 匿名/接口类型无法定位到具体实现
	}
	obj := named.Obj()
	if obj.Pkg() == nil || !isInModule(obj.Pkg().Path(), repo.Module) {
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

// resolveCallee 将调用表达式解析为被调用的 *types.Func。
func resolveCallee(info *types.Info, fun ast.Expr) (*types.Func, bool) {
	logger := zap.L()
	logger.Debug("enter resolveCallee")
	defer logger.Debug("exit resolveCallee")
	var id *ast.Ident
	switch f := fun.(type) {
	case *ast.Ident:
		id = f
	case *ast.SelectorExpr:
		id = f.Sel
	default:
		return nil, false // 函数值调用/类型转换等，MVP 不追踪
	}
	obj, ok := info.Uses[id]
	if !ok {
		return nil, false
	}
	fn, ok := obj.(*types.Func)
	return fn, ok
}

// findCallerDecl 返回调用点所属的最近函数声明。
func findCallerDecl(stack []ast.Node) *ast.FuncDecl {
	logger := zap.L()
	logger.Debug("enter findCallerDecl")
	defer logger.Debug("exit findCallerDecl")
	for i := len(stack) - 1; i >= 0; i-- {
		if fd, ok := stack[i].(*ast.FuncDecl); ok {
			return fd
		}
	}
	return nil
}

// isArgCall 判断 call 是否处于另一个调用点的参数位置（嵌套调用，
// 如 A(B(C())) 里的 B(C())）。参数位置的调用由外层处理为"持有返回
// 参数"链，不建 calls。
func isArgCall(stack []ast.Node, call *ast.CallExpr) bool {
	logger := zap.L()
	logger.Debug("enter isArgCall")
	defer logger.Debug("exit isArgCall")
	for i := len(stack) - 1; i >= 0; i-- {
		outer, ok := stack[i].(*ast.CallExpr)
		if !ok || outer == call {
			continue
		}
		for _, arg := range outer.Args {
			if arg == call {
				return true
			}
		}
		return false // 最近的调用点不含当前 call
	}
	return false
}

// handleNestedArg 处理参数位置的嵌套调用：接收者持有返回参数。
// A(B(C())) → A→B、B→C（passes_result），参数位置的调用不建 calls。
func (a *Adapter) handleNestedArg(pkg *packages.Package, call *ast.CallExpr, receiverID domain.CanonicalID,
	emit domain.EmitFunc, repo *domain.Repository) {
	logger := zap.L()
	logger.Debug("enter (Adapter).handleNestedArg")
	defer logger.Debug("exit (Adapter).handleNestedArg")
	callee, ok := resolveCallee(pkg.TypesInfo, call.Fun)
	if !ok {
		return
	}
	if callee.Pkg() == nil || !isInModule(callee.Pkg().Path(), repo.Module) {
		return
	}
	calleeID, calleeKind := fnID(callee)
	if calleeID == "" || calleeID == receiverID {
		return
	}
	_ = emit(domain.Item{Node: nodeFor(repo, pkg, callee, calleeID, calleeKind, nil)})
	_ = emit(domain.Item{Fact: &domain.Fact{
		SourceID:   receiverID, // 接收者持有返回参数
		TargetID:   calleeID,
		Kind:       domain.FactPassesResult,
		ToolSource: domain.ToolCodeGraph,
		Confidence: 0.8,
	}})
	// 递归处理 callee 的实参：调用 → 持有返回参数；函数引用 → 持有参数
	for _, inner := range call.Args {
		if ic, isCall := inner.(*ast.CallExpr); isCall {
			a.handleNestedArg(pkg, ic, calleeID, emit, repo)
			continue
		}
		fn := argFuncRef(pkg, inner)
		if fn == nil || fn.Pkg() == nil || !isInModule(fn.Pkg().Path(), repo.Module) {
			continue
		}
		paramID, paramKind := fnID(fn)
		if paramID == "" || paramID == calleeID {
			continue
		}
		_ = emit(domain.Item{Node: nodeFor(repo, pkg, fn, paramID, paramKind, nil)})
		_ = emit(domain.Item{Fact: &domain.Fact{
			SourceID:   calleeID, // 接收者持有参数
			TargetID:   paramID,
			Kind:       domain.FactPassesTo,
			ToolSource: domain.ToolCodeGraph,
			Confidence: 0.8,
		}})
	}
}

// concreteMethodFor 解析链式调用接收者表达式的实际方法目标：
//   - callee 是接口方法时，分析接收者表达式（如 NewService().DoSth() 的
//     NewService()）的实际返回类型——函数声明返回接口但 return 具体类型
//     （return impl{}）→ 解析到该具体类型的同名实现方法（main → (impl).DoSth）
//   - 无法确定（跨包/多态）→ 回退指向接口类型节点（main → Service）
//
// 返回 (targetID, targetKind, node)；targetID 为空表示放弃建边。
func (a *Adapter) concreteMethodFor(pkg *packages.Package, call *ast.CallExpr, callee *types.Func,
	repo *domain.Repository) (domain.CanonicalID, domain.EntityKind, *domain.CodeEntity) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", "", nil
	}
	t := a.concreteReturnType(pkg, sel.X)
	named, ok := derefNamed(t)
	if !ok {
		return "", "", nil
	}
	if isInterfaceType(named) {
		// 无法确定具体实现：指向接口类型节点
		id := canonicalizer.GoSymbolID(named.Obj().Pkg().Path(), named.Obj().Name())
		return id, domain.KindInterface, &domain.CodeEntity{ID: id, Kind: domain.KindInterface, Name: named.Obj().Name()}
	}
	// 具体类型：查找同名实现方法
	for i := 0; i < named.NumMethods(); i++ {
		m := named.Method(i)
		if m.Name() != callee.Name() {
			continue
		}
		mid, mkind := fnID(m)
		if mid == "" {
			continue
		}
		return mid, mkind, nodeFor(repo, pkg, m, mid, mkind, nil)
	}
	return "", "", nil
}

// concreteReturnType 解析表达式的"实际返回类型"：若声明返回类型是接口
// （如 NewService() Service），分析函数体的 return 语句找具体类型
// （return impl{} → impl）；无法确定时返回静态类型。
func (a *Adapter) concreteReturnType(pkg *packages.Package, expr ast.Expr) types.Type {
	t := pkg.TypesInfo.TypeOf(expr)
	named, ok := derefNamed(t)
	if !ok || !isInterfaceType(named) {
		return t // 非接口：静态类型即可
	}
	// 接收者是函数调用：分析其 return
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return t
	}
	fn, ok2 := resolveCallee(pkg.TypesInfo, call.Fun)
	if !ok2 || fn == nil {
		return t
	}
	decl := findFuncDecl(pkg, fn)
	if decl == nil || decl.Body == nil {
		return t
	}
	var found types.Type
	ast.Inspect(decl.Body, func(n ast.Node) bool {
		rs, isRs := n.(*ast.ReturnStmt)
		if !isRs {
			return true
		}
		for _, re := range rs.Results {
			rt := pkg.TypesInfo.TypeOf(re)
			rn, ok3 := derefNamed(rt)
			if ok3 && !isInterfaceType(rn) {
				found = rt
				return false
			}
		}
		return true
	})
	if found != nil {
		return found
	}
	return t
}

// findFuncDecl 通过位置查找 *types.Func 对应的 FuncDecl（同包内）。
func findFuncDecl(pkg *packages.Package, fn *types.Func) *ast.FuncDecl {
	pos := pkg.Fset.PositionFor(fn.Pos(), false)
	if pos.Filename == "" {
		return nil
	}
	for _, f := range pkg.Syntax {
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok {
				continue
			}
			p := pkg.Fset.PositionFor(fd.Pos(), false)
			if p.Filename == pos.Filename && p.Line == pos.Line {
				return fd
			}
		}
	}
	return nil
}

// derefNamed 解引用指针后取具名类型。
func derefNamed(t types.Type) (*types.Named, bool) {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	n, ok := t.(*types.Named)
	return n, ok
}

// isInterfaceType 判断具名类型是否为接口。
func isInterfaceType(n *types.Named) bool {
	if n == nil {
		return false
	}
	_, ok := n.Underlying().(*types.Interface)
	return ok
}

// isInterfaceMethod 判断 *types.Func 是否为接口方法（接收者类型是接口）。
// 接口方法不作为独立节点：SCIP 适配器不建、AST 适配器调用处也不建。
func isInterfaceMethod(fn *types.Func) bool {
	logger := zap.L()
	logger.Debug("enter isInterfaceMethod")
	defer logger.Debug("exit isInterfaceMethod")
	if fn == nil || fn.Pkg() == nil {
		return false
	}
	sig, _ := fn.Type().(*types.Signature)
	if sig == nil {
		return false
	}
	recv := sig.Recv()
	if recv == nil {
		return false
	}
	t := recv.Type()
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	_, ok = named.Underlying().(*types.Interface)
	return ok
}

// fnID 计算函数/方法的 canonical ID 与领域种类。
// 返回值 (id, kind, ok)。
func fnID(fn *types.Func) (domain.CanonicalID, domain.EntityKind) {
	logger := zap.L()
	logger.Debug("enter fnID")
	defer logger.Debug("exit fnID")
	if fn == nil || fn.Pkg() == nil {
		return "", ""
	}
	path := fn.Pkg().Path()
	sig, _ := fn.Type().(*types.Signature)
	if sig == nil {
		return "", ""
	}
	if recv := sig.Recv(); recv != nil {
		t := recv.Type()
		if p, ok := t.(*types.Pointer); ok {
			t = p.Elem()
		}
		named, ok := t.(*types.Named)
		if !ok {
			return "", "" // 匿名结构体上的方法，跳过
		}
		return canonicalizer.GoSymbolID(path, canonicalizer.MethodName(named.Obj().Name(), fn.Name())), domain.KindMethod
	}
	return canonicalizer.GoSymbolID(path, fn.Name()), domain.KindFunction
}

// nodeFor 为函数/方法生成轻量节点（ID 与 SCIP 一致，行号/文件来自位置信息，
// signature 由 go/types 生成，与 SCIP 节点通过 properties 合并）。
func nodeFor(repo *domain.Repository, pkg *packages.Package, fn *types.Func, id domain.CanonicalID,
	kind domain.EntityKind, extra map[string]bool) *domain.CodeEntity {
	logger := zap.L()
	logger.Debug("enter nodeFor")
	defer logger.Debug("exit nodeFor")
	n := &domain.CodeEntity{ID: id, Kind: kind}
	if fn != nil && fn.Pkg() != nil {
		pos := pkg.Fset.PositionFor(fn.Pos(), false)
		n.FilePath = relPath(repo.Path, pos.Filename)
		n.LineStart = pos.Line
		n.LineEnd = pos.Line
		// 方法/函数名：方法为 (T).m，函数为 m
		if kind == domain.KindMethod {
			sig, _ := fn.Type().(*types.Signature)
			if sig != nil && sig.Recv() != nil {
				t := sig.Recv().Type()
				if p, ok := t.(*types.Pointer); ok {
					t = p.Elem()
				}
				if named, ok := t.(*types.Named); ok {
					n.Name = canonicalizer.MethodName(named.Obj().Name(), fn.Name())
				}
			}
		} else {
			n.Name = fn.Name()
		}
		n.Properties = map[string]any{
			// ObjectString 对方法包含接收者：func (s *Service) CreatePayment(req string) error
			"signature": types.ObjectString(fn, types.RelativeTo(pkg.Types)),
		}
		for flag := range extra {
			n.Properties[flag] = "true"
		}
	}
	return n
}

// ensurePackageNode 保障包节点存在（SCIP 已建则跳过）。
// 注意：加载失败/无源码的包（pkg.Syntax 为空，如编译错误的包或测试变体）
// 仍会创建节点，只是不带 file_path，避免 index out of range。
func ensurePackageNode(repo *domain.Repository, pkg *packages.Package, emit domain.EmitFunc) error {
	logger := zap.L()
	logger.Debug("enter ensurePackageNode")
	defer logger.Debug("exit ensurePackageNode")
	n := &domain.CodeEntity{
		ID:   packageID(pkg.PkgPath),
		Kind: domain.KindPackage,
		Name: pathBase(pkg.PkgPath),
	}
	if len(pkg.Syntax) > 0 {
		n.FilePath = relPath(repo.Path, pkg.Fset.PositionFor(pkg.Syntax[0].Pos(), false).Filename)
	}
	return emit(domain.Item{Node: n})
}

func packageID(pkgPath string) domain.CanonicalID {
	logger := zap.L()
	logger.Debug("enter packageID")
	defer logger.Debug("exit packageID")
	return canonicalizer.GoSymbolID(pkgPath, pathBase(pkgPath))
}

func pathBase(p string) string {
	logger := zap.L()
	logger.Debug("enter pathBase")
	defer logger.Debug("exit pathBase")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// relPath 将绝对路径转为仓库相对路径；仓库外文件返回空串。
func relPath(repoPath, abs string) string {
	logger := zap.L()
	logger.Debug("enter relPath")
	defer logger.Debug("exit relPath")
	if abs == "" {
		return ""
	}
	rel, err := filepath.Rel(repoPath, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return ""
	}
	return filepath.ToSlash(rel)
}

func isInModule(pkgPath, module string) bool {
	logger := zap.L()
	logger.Debug("enter isInModule")
	defer logger.Debug("exit isInModule")
	return pkgPath == module || strings.HasPrefix(pkgPath, module+"/")
}
