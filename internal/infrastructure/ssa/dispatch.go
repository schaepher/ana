// 接口动态派发追踪（field_trace.md §15.2 Q91/Q93/Q94）：
//   - 注册点识别：SSA MakeInterface 指令（具体值 → 接口值的显式转换，
//     即注册/注入点）→ 接口类型 → 动态类型集 + 注册位置
//   - 全量实现枚举兜底：模块内实现该接口方法的类型（types.Implements）
//   - dispatch_to 边：接口类型节点 → 候选实现方法节点，metadata 携带
//     {interface_method, origin: register|enum, confidence: 0.9|0.7,
//     register_site}（Q93 三档置信度；guess 0.5 留函数值场景）
//   - 缺失信息（Q93）：匿名接口/外部包实现 → 跳过（不产边）
package ssa

import (
	"go/types"
	"strings"

	"github.com/schaepher/codeintel/internal/domain"
	"go.uber.org/zap"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

// dispatchReg 注册点：接口类型 → 动态类型 String → 注册行号。
type dispatchReg map[*types.Named]map[string]int

// regHits Q221：注册命中预处理（iface.String() → candidateKey → true）——
// dispatchOriginOf 的 O(1) 判定表。原实现挂在 extractor（每函数新建）
// 上懒构建：12875 函数 × 遍历全部注册点方法 ≈ 重复预处理。Index 级
// 构建一次，extractor 共享只读。
type regHits map[string]map[string]bool

// buildRegHits Q221：构建注册命中预处理表（Index 级一次，原 extractor
// 懒构建每函数重复全量遍历注册点方法）。
func buildRegHits(regs dispatchReg, prog *ssa.Program) regHits {
	out := regHits{}
	for ifc, ifcRegs := range regs {
		hits := map[string]bool{}
		for dyn := range ifcRegs {
			t := dynamicTypeOf(dyn, prog)
			if ptr, ok := t.(*types.Pointer); ok {
				t = ptr.Elem()
			}
			named, ok := t.(*types.Named)
			if !ok {
				continue
			}
			for i := 0; i < named.NumMethods(); i++ {
				hits[candidateKey(named.Method(i))] = true
			}
		}
		out[ifc.String()] = hits
	}
	return out
}

// emitDispatches 发射全部 dispatch_to 边：
//  1. 收集模块内 MakeInterface 注册点
//  2. 遍历模块内函数的所有动态接口方法调用（cc.Method != nil）
//  3. 候选 = 注册点命中（register 0.9）∪ 枚举实现者（enum 0.7）
//  4. 接口类型节点 → 候选实现方法（模块内）→ dispatch_to 边

// implMethodsFor 枚举模块内实现接口方法的具名类型方法（值与指针方法集
// 都查）；接口自身（Implements 自反）排除。⑮ 动态派发追踪复用。

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
