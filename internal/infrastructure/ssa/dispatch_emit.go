package ssa

import (
	"go/types"

	"github.com/schaepher/codeintel/internal/domain"
	"go.uber.org/zap"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

// emitDispatches 发射全部 dispatch_to 边：
//  1. 收集模块内 MakeInterface 注册点
//  2. 遍历模块内函数的所有动态接口方法调用（cc.Method != nil）
//  3. 候选 = 注册点命中（register 0.9）∪ 枚举实现者（enum 0.7）
//  4. 接口类型节点 → 候选实现方法（模块内）→ dispatch_to 边
func emitDispatches(repo *domain.Repository, prog *ssa.Program, pkgs []*types.Package, emit domain.EmitFunc) error {
	logger := zap.L()
	logger.Debug("enter emitDispatches")
	defer logger.Debug("exit emitDispatches")
	regs := collectDispatchRegistrations(prog, repo.Modules)

	// 接口方法调用集合：接口类型 → 方法名（map 去重；UNIQUE 边合并）
	type callKey struct {
		iface  *types.Named
		method string
	}
	calls := map[callKey]bool{}
	for fn := range ssautil.AllFunctions(prog) {
		if !isModuleFunction(fn, repo.Modules) {
			continue
		}
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				var cc *ssa.CallCommon
				switch v := instr.(type) {
				case *ssa.Call:
					cc = &v.Call
				case *ssa.Go:
					cc = &v.Call
				case *ssa.Defer:
					cc = &v.Call
				}
				if cc == nil || cc.Method == nil || cc.StaticCallee() != nil {
					continue
				}
				if named := interfaceNamedOf(cc.Value.Type()); named != nil {
					calls[callKey{iface: named, method: cc.Method.Name()}] = true
				}
			}
		}
	}
	if len(calls) == 0 {
		return nil
	}

	for ck := range calls {
		ifaceID := interfaceID(ck.iface)
		if ifaceID == "" {
			continue
		}

		candidates := map[string]dispatchCandidate{}
		for dyn, site := range regs[ck.iface] {
			t := dynamicTypeOf(dyn, prog)
			if t == nil {
				continue
			}
			if fn := findMethod(t, ck.method); fn != nil {
				candidates[candidateKey(fn)] = dispatchCandidate{fn: fn, origin: "register", confidence: 0.9, site: site}
			}
		}
		for _, fn := range implMethodsFor(pkgs, repo.Modules, ck.iface, ck.method) {
			key := candidateKey(fn)
			if _, ok := candidates[key]; ok {
				continue
			}
			candidates[key] = dispatchCandidate{fn: fn, origin: "enum", confidence: 0.7}
		}
		for _, c := range candidates {
			id, _, _ := funcIdentity(c.fn)
			if id == "" {
				continue
			}
			meta := map[string]any{
				"interface_method": ck.method,
				"origin":           c.origin,
				"confidence":       c.confidence,
			}
			if c.site > 0 {
				meta["register_site"] = c.site
			}
			if err := emit(domain.Item{Fact: &domain.Fact{
				SourceID:   ifaceID,
				TargetID:   id,
				Kind:       domain.FactDispatchTo,
				ToolSource: domain.ToolSSA,
				Confidence: c.confidence,
				Metadata:   meta,
			}}); err != nil {
				return err
			}
		}
	}
	return nil
}

// implMethodsFor 枚举模块内实现接口方法的具名类型方法（值与指针方法集
// 都查）；接口自身（Implements 自反）排除。⑮ 动态派发追踪复用。
func implMethodsFor(pkgs []*types.Package, modules []string, iface *types.Named, method string) []*types.Func {
	var out []*types.Func
	for _, pkg := range pkgs {
		if !isInModule(pkg.Path(), modules) {
			continue
		}
		scope := pkg.Scope()
		for _, name := range scope.Names() {
			tn, ok := scope.Lookup(name).(*types.TypeName)
			if !ok {
				continue
			}
			named, ok := tn.Type().(*types.Named)
			if !ok {
				continue
			}
			if named == iface {
				continue
			}

			if !types.Implements(named, iface.Underlying().(*types.Interface)) &&
				!types.Implements(types.NewPointer(named), iface.Underlying().(*types.Interface)) {
				continue
			}
			if fn := findMethod(named, method); fn != nil {
				out = append(out, fn)
			}
		}
	}
	return out
}
