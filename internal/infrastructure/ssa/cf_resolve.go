package ssa

import (
	"go/types"
	"strings"

	"go.uber.org/zap"
	"golang.org/x/tools/go/ssa"
)

// ownerTypesOf 收集实参类型及其嵌套 struct 字段的 owner 类型路径
// （Q157：OrderModel 含 Order 字段 → [pkg.OrderModel, pkg.Order]——
// 实现写 Order.FinalFee 也能经 OrderModel 实参匹配）。深度上限 3
// 防深嵌套爆炸；指针/切片解包；同类型去重。
func ownerTypesOf(t types.Type, depth int) []string {
	if depth > 3 {
		return nil
	}
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	} else if s, ok := t.(*types.Slice); ok {
		t = s.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok {
		return nil
	}
	obj := named.Obj()
	if obj == nil || obj.Pkg() == nil {
		return nil
	}
	self := obj.Pkg().Path() + "." + obj.Name()
	seen := map[string]bool{self: true}
	out := []string{self}
	if st, ok := named.Underlying().(*types.Struct); ok {
		for i := 0; i < st.NumFields(); i++ {
			for _, sub := range ownerTypesOf(st.Field(i).Type(), depth+1) {
				if !seen[sub] {
					seen[sub] = true
					out = append(out, sub)
				}
			}
		}
	}
	return out
}

// resolveStaticCallee 解析静态可确定的被调函数：静态调用 / 直接函数值 / phi 链。
func resolveStaticCallee(cc *ssa.CallCommon) *ssa.Function {
	logger := zap.L()
	logger.Debug("enter resolveStaticCallee")
	defer logger.Debug("exit resolveStaticCallee")
	if fn := cc.StaticCallee(); fn != nil {
		return fn
	}
	return resolveFuncValue(cc.Value, 0)
}
func resolveFuncValue(v ssa.Value, depth int) *ssa.Function {
	if depth > 4 {
		return nil
	}
	if fn, ok := v.(*ssa.Function); ok {
		return fn
	}
	if phi, ok := v.(*ssa.Phi); ok {
		for _, op := range phi.Edges {
			if fn := resolveFuncValue(op, depth+1); fn != nil {
				return fn
			}
		}
	}

	if call, ok := v.(*ssa.Call); ok {
		callee := resolveStaticCallee(&call.Call)
		if callee == nil {
			return nil
		}
		for _, ret := range returnOperands(callee) {
			for _, rv := range ret {
				if fn := resolveFuncValue(rv, depth+1); fn != nil {
					return fn
				}
			}
		}
	}
	return nil
}

// returnOperands 收集函数所有 Return 指令的操作数（多返回为元组）。
func returnOperands(fn *ssa.Function) [][]ssa.Value {
	logger := zap.L()
	logger.Debug("enter returnOperands")
	defer logger.Debug("exit returnOperands")
	var out [][]ssa.Value
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			if ret, ok := instr.(*ssa.Return); ok {
				out = append(out, ret.Results)
			}
		}
	}
	return out
}

// returnOperandsCached 惰性缓存函数的 Return 指令操作数（多调用点复用，
// 避免每次 emitCall 重复扫描被调函数）。
func (ext *fieldExtractor) returnOperandsCached(fn *ssa.Function) [][]ssa.Value {
	if rets, ok := ext.rets[fn]; ok {
		return rets
	}
	rets := returnOperands(fn)
	ext.rets[fn] = rets
	return rets
}

// structPathOfType 取实参类型的结构体限定路径（*T → pkg.T；非具名结构体 → 空）。
func structPathOfType(t types.Type) string {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok {
		return ""
	}
	obj := named.Obj()
	if obj == nil || obj.Pkg() == nil {
		return ""
	}
	return obj.Pkg().Path() + "." + obj.Name()
}

// structPathOf 从 full_path（pkg.T.f）提取结构体路径（pkg.T）。
func structPathOf(fullPath string) string {
	if i := strings.LastIndex(fullPath, "."); i >= 0 {
		return fullPath[:i]
	}
	return fullPath
}
