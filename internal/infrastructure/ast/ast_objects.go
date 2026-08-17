package ast

import (
	"go/ast"
	"go/token"
	"go/types"

	"github.com/schaepher/codeintel/internal/canonicalizer"
	"github.com/schaepher/codeintel/internal/domain"
	"go.uber.org/zap"
	"golang.org/x/tools/go/packages"
)

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
			continue
		}
		if named.Obj().Pkg() == nil || !isInModule(named.Obj().Pkg().Path(), repo.Modules) {
			continue
		}
		recvID := canonicalizer.GoSymbolID(named.Obj().Pkg().Path(), named.Obj().Name())
		if recvID == methodID {
			continue
		}

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
			SourceID:   recvID,
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
	case *ast.UnaryExpr:
		if cl, ok := e.X.(*ast.CompositeLit); ok {
			t = pkg.TypesInfo.TypeOf(cl)
		}
	case *ast.CompositeLit:
		t = pkg.TypesInfo.TypeOf(e)
	case *ast.CallExpr:
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
		return "", false
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
		return "", false
	}
	if named.Obj().Pkg() == nil || !isInModule(named.Obj().Pkg().Path(), repo.Modules) {
		return "", false
	}
	structID := canonicalizer.GoSymbolID(named.Obj().Pkg().Path(), named.Obj().Name())
	if structID == callerID {
		return "", false
	}

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

// handleNestedArg 处理参数位置的嵌套调用：接收者持有返回参数。
// A(B(C())) → A→B、B→C（passes_result），参数位置的调用不建 calls。
// Q185：边 metadata 记录接收者实参下标/参数名——argIndex = 该嵌套调用
// 在接收者调用点的第几个实参，argName = 接收者签名的对应参数名
// （outer(inner(1)) 的 inner 是 outer 第 1 个参数 s；由调用方计算传入，
// 递归时传内层 callee 的实参名）。
func (a *Adapter) handleNestedArg(pkg *packages.Package, call *ast.CallExpr, receiverID domain.CanonicalID,
	argIndex int, argName string, emit domain.EmitFunc, repo *domain.Repository) {
	logger := zap.L()
	logger.Debug("enter (Adapter).handleNestedArg")
	defer logger.Debug("exit (Adapter).handleNestedArg")
	callee, ok := resolveCallee(pkg.TypesInfo, call.Fun)
	if !ok {
		return
	}
	if callee.Pkg() == nil {
		return
	}

	calleeID, calleeKind := fnID(callee)
	if calleeID == "" || calleeID == receiverID {
		return
	}
	_ = emit(domain.Item{Node: nodeFor(repo, pkg, callee, calleeID, calleeKind, nil)})
	_ = emit(domain.Item{Fact: &domain.Fact{
		SourceID:   receiverID,
		TargetID:   calleeID,
		Kind:       domain.FactPassesResult,
		ToolSource: domain.ToolCodeGraph,
		Confidence: 0.8,
		Metadata: map[string]any{
			"arg_index": argIndex,
			"arg_name":  argName,
		},
	}})

	for i, inner := range call.Args {
		if ic, isCall := inner.(*ast.CallExpr); isCall {
			innerName := ""
			if sig, ok := callee.Type().(*types.Signature); ok && i < sig.Params().Len() {
				innerName = sig.Params().At(i).Name()
			}
			a.handleNestedArg(pkg, ic, calleeID, i, innerName, emit, repo)
			continue
		}
		fn := argFuncRef(pkg, inner)
		if fn == nil || fn.Pkg() == nil || !isInModule(fn.Pkg().Path(), repo.Modules) {
			continue
		}
		paramID, paramKind := fnID(fn)
		if paramID == "" || paramID == calleeID {
			continue
		}
		_ = emit(domain.Item{Node: nodeFor(repo, pkg, fn, paramID, paramKind, nil)})
		_ = emit(domain.Item{Fact: &domain.Fact{
			SourceID:   calleeID,
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

		id := canonicalizer.GoSymbolID(named.Obj().Pkg().Path(), named.Obj().Name())
		return id, domain.KindInterface, &domain.CodeEntity{ID: id, Kind: domain.KindInterface, Name: named.Obj().Name()}
	}

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
		return t
	}

	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return t
	}
	fn, ok2 := resolveCallee(pkg.TypesInfo, call.Fun)
	if !ok2 || fn == nil {
		return t
	}

	defPkg := pkg
	if fn.Pkg() != nil && a.pkgsByPath != nil {
		if dp, ok := a.pkgsByPath[fn.Pkg().Path()]; ok {
			defPkg = dp
		}
	}
	decl := findFuncDecl(defPkg, fn)
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
			rt := defPkg.TypesInfo.TypeOf(re)
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
