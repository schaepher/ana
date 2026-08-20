package ast

import (
	"go/ast"
	"go/types"

	"github.com/schaepher/codeintel/internal/canonicalizer"
	"github.com/schaepher/codeintel/internal/domain"
	"go.uber.org/zap"
	"golang.org/x/tools/go/packages"
)

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
