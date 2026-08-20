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
