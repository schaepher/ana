// 接口动态派发追踪（field_trace.md §15.2 Q91/Q93/Q94）：
//   - 注册点识别：SSA MakeInterface 指令（具体值 → 接口值的显式转换，
//     即注册/注入点）→ 接口类型 → 动态类型集 + 注册位置
//   - 全量实现枚举兜底：模块内实现该接口方法的类型（types.Implements）
//   - dispatch_to 边：接口类型节点 → 候选实现方法节点，metadata 携带
//     {interface_method, origin: register|enum, confidence: 0.9|0.7,
//      register_site}（Q93 三档置信度；guess 0.5 留函数值场景）
//   - 缺失信息（Q93）：匿名接口/外部包实现 → 跳过（不产边）
package ssa

import (
	"go/types"
	"strings"

	"github.com/schaepher/codeintel/internal/domain"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
	"go.uber.org/zap"
)

// dispatchReg 注册点：接口类型 → 动态类型 String → 注册行号。
type dispatchReg map[*types.Named]map[string]int

// emitDispatches 发射全部 dispatch_to 边：
//  1. 收集模块内 MakeInterface 注册点
//  2. 遍历模块内函数的所有动态接口方法调用（cc.Method != nil）
//  3. 候选 = 注册点命中（register 0.9）∪ 枚举实现者（enum 0.7）
//  4. 接口类型节点 → 候选实现方法（模块内）→ dispatch_to 边
func emitDispatches(repo *domain.Repository, prog *ssa.Program, pkgs []*types.Package, emit domain.EmitFunc) error {
	logger := zap.L()
	logger.Debug("enter emitDispatches")
	defer logger.Debug("exit emitDispatches")
	regs := collectDispatchRegistrations(prog, repo.Modules) // 可为空：枚举兜底独立

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
			continue // 匿名接口：缺失信息，跳过
		}
		// 候选：注册点（0.9）+ 枚举兜底（0.7）
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
				continue // 注册点优先
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
				continue // 接口自身不算实现者
			}
			// 指针接收者方法：值类型不实现，须检查 *T 与 T 两个方法集
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

// dispatchCandidate 单个候选实现。
type dispatchCandidate struct {
	fn         *types.Func
	origin     string // register / enum
	confidence float64
	site       int // 注册行号（register 时）
}

// collectDispatchRegistrations 收集模块内 MakeInterface 注册点：
// 具体值 → 接口值的转换指令（SSA 中注册/注入点的标准形态）。
func collectDispatchRegistrations(prog *ssa.Program, modules []string) dispatchReg {
	logger := zap.L()
	logger.Debug("enter collectDispatchRegistrations")
	defer logger.Debug("exit collectDispatchRegistrations")
	regs := dispatchReg{}
	for fn := range ssautil.AllFunctions(prog) {
		if !isModuleFunction(fn, modules) {
			continue
		}
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				mi, ok := instr.(*ssa.MakeInterface)
				if !ok {
					continue
				}
				iface := interfaceNamedOf(mi.Type())
				if iface == nil {
					continue
				}
				if regs[iface] == nil {
					regs[iface] = map[string]int{}
				}
				dyn := mi.X.Type().String()
				if _, ok := regs[iface][dyn]; !ok {
					// 注册点取动态值字面量（&Eng{}）的位置最准；
					// MakeInterface.Pos 为合成位置
					pos := mi.X.Pos()
					if pos == 0 {
						pos = mi.Pos()
					}
					regs[iface][dyn] = prog.Fset.PositionFor(pos, false).Line
				}
			}
		}
	}
	return regs
}

// interfaceNamedOf 取具名接口类型（*types.Named，Underlying 是 Interface）。
func interfaceNamedOf(t types.Type) *types.Named {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	if named, ok := t.(*types.Named); ok {
		if _, ok2 := named.Underlying().(*types.Interface); ok2 {
			return named
		}
	}
	return nil
}

// interfaceID 接口类型节点 canonical ID（symbol:go:<pkg>:<Iface>）。
func interfaceID(named *types.Named) domain.CanonicalID {
	obj := named.Obj()
	if obj == nil || obj.Pkg() == nil {
		return ""
	}
	return domain.CanonicalID("symbol:go:" + obj.Pkg().Path() + ":" + obj.Name())
}

// dynamicTypeOf 从类型字符串反查类型池中的类型（"*example.com/m.Eng"
// → *Eng）。仅匹配模块内类型；用末段标识符 Lookup 后校验完整路径。
func dynamicTypeOf(typeStr string, prog *ssa.Program) types.Type {
	ptr := strings.HasPrefix(typeStr, "*")
	full := strings.TrimPrefix(typeStr, "*")
	name := full
	if i := strings.LastIndex(full, "."); i >= 0 {
		name = full[i+1:]
	}
	for _, pkg := range prog.AllPackages() {
		obj := pkg.Pkg.Scope().Lookup(name)
		if obj == nil || obj.Type() == nil || obj.Type().String() != full {
			continue
		}
		if ptr {
			if named, ok := obj.Type().(*types.Named); ok {
				return types.NewPointer(named)
			}
		}
		return obj.Type()
	}
	return nil
}

// findMethod 查找类型的方法（值与指针方法集都查）。
func findMethod(t types.Type, name string) *types.Func {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok {
		return nil
	}
	for _, ms := range []*types.MethodSet{types.NewMethodSet(named), types.NewMethodSet(types.NewPointer(named))} {
		if sel := ms.Lookup(nil, name); sel != nil {
			if fn, ok := sel.Obj().(*types.Func); ok {
				return fn
			}
		}
	}
	return nil
}

// candidateKey 候选实现方法去重键：接收者类型 + 方法名。
func candidateKey(fn *types.Func) string {
	sig, _ := fn.Type().(*types.Signature)
	if sig == nil || sig.Recv() == nil {
		return fn.Name()
	}
	return sig.Recv().Type().String() + "." + fn.Name()
}
