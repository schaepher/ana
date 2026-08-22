package ssa

import (
	"fmt"
	"go/constant"
	"sort"
	"strings"

	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

// Q239 动态 SQL 拼接还原（design-q239.md §3.4）：fmt.Sprintf 模板 +
// %s 实参值流追溯（常量 / 嵌套 Sprintf / 跨函数参数）→ 还原 SQL 模板
// → 走统一 parseSQLStmt。深度上限 3；部分还原（可还原的占位符还原，
// 剩余保留——不误报）；追溯不到返回 ""（保持现状不解析）。

// maxSQLResolveDepth %s 实参追溯深度上限（Q2：3 层——go2o rbac 典型
// 「调用方常量 → dao 参数」2 层覆盖）。
const maxSQLResolveDepth = 3

// resolveSQLString 解析 SQL 字符串实参（Q239）：
//   - 字符串常量 → 直接返回
//   - fmt.Sprintf 调用 → 模板 + %s 实参递归还原（部分还原：不可还原
//     的占位符保留）
//   - 函数参数 → 调用点实参追溯（静态调用点，实参按参数索引定位）
// 不可还原返回 ""。只处理 string Kind（%d 等数值实参不参与 %s 替换）。
func (ext *fieldExtractor) resolveSQLString(v ssa.Value, depth int) string {
	if depth > maxSQLResolveDepth {
		return ""
	}
	if mi, ok := v.(*ssa.MakeInterface); ok {
		v = mi.X // any 参数包装解包（Call/Parameter 在包装内）
	}
	if c, ok := unwrapConst(v); ok && c.Value != nil && c.Value.Kind() == constant.String {
		return constant.StringVal(c.Value)
	}
	call, ok := v.(*ssa.Call)
	if !ok {
		// 跨函数参数：静态调用点实参追溯（带缓存）
		if p, isParam := v.(*ssa.Parameter); isParam {
			return ext.resolveParamAtCalls(p, depth)
		}
		return ""
	}
	fn := call.Call.StaticCallee()
	if fn == nil || fn.Name() != "Sprintf" || len(call.Call.Args) < 1 {
		return ""
	}
	tmpl := ext.resolveSQLString(call.Call.Args[0], depth+1)
	if tmpl == "" {
		return ""
	}
	// %s 占位符按实参序逐个替换（%d 等数值实参不消耗 %s——字符串实参
	// 与 %s 位置对齐时正确；不齐时部分还原不误报）；变参打包的 Slice
	// 指令展开为元素（fmt.Sprintf(format, a ...any) 的 []any{...}）
	for i := 1; i < len(call.Call.Args); i++ {
		elems := []ssa.Value{call.Call.Args[i]}
		if sl, ok := call.Call.Args[i].(*ssa.Slice); ok {
			if es := sliceElemsOf(sl); len(es) > 0 {
				elems = es
			}
		}
		for _, e := range elems {
			if !strings.Contains(tmpl, "%s") {
				break
			}
			if s := ext.resolveSQLString(e, depth+1); s != "" {
				tmpl = strings.Replace(tmpl, "%s", s, 1)
			}
		}
	}
	return tmpl
}

// sliceElemsOf 提取变参打包 slice 的元素（[]any{a, b} 的 Alloc/MakeSlice
// + IndexAddr + Store 序列按索引排序）。非字面量 slice 返回 nil。
func sliceElemsOf(sl *ssa.Slice) []ssa.Value {
	type idxVal struct {
		idx int
		val ssa.Value
	}
	var pairs []idxVal
	if refs := sl.X.Referrers(); refs != nil {
		for _, u := range *refs {
			ia, ok := u.(*ssa.IndexAddr)
			if !ok || ia.X != sl.X {
				continue
			}
			if c, ok := ia.Index.(*ssa.Const); ok && c.Value != nil {
				idx, _ := constant.Int64Val(c.Value)
				for _, u2 := range *ia.Referrers() {
					if st, ok := u2.(*ssa.Store); ok && st.Addr == ia {
						pairs = append(pairs, idxVal{int(idx), st.Val})
					}
				}
			}
		}
	}
	if len(pairs) == 0 {
		return nil
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].idx < pairs[j].idx })
	out := make([]ssa.Value, 0, len(pairs))
	for _, p := range pairs {
		out = append(out, p.val)
	}
	return out
}

// paramCalls 参数所属函数的静态调用点缓存（惰性构建一次）。
type paramCalls struct {
	fn      *ssa.Function
	callers [][]ssa.Value // 每个静态调用点的实参列表
}

// resolveParamAtCalls 函数参数 → 静态调用点实参追溯（多个调用点取首个
// 可还原的；实参索引 = 参数在签名中的位置——方法调用 Args[0]=receiver
// 偏移 1）。
func (ext *fieldExtractor) resolveParamAtCalls(p *ssa.Parameter, depth int) string {
	fn := p.Parent()
	fmt.Printf("DEBUG param fn=%v\n", fn)
	if fn == nil {
		return ""
	}
	paramIdx := 0
	for i, par := range fn.Params {
		if par == p {
			paramIdx = i
			break
		}
	}
	if paramIdx >= len(fn.Params) {
		return ""
	}
	// 惰性扫描 prog 找调用 fn 的静态调用点（每函数一次，缓存）
	key := fn
	pc, ok := ext.paramCallerCache[key]
	if !ok {
		pc = &paramCalls{fn: fn}
		for _, f := range ext.allFunctions() {
			for _, b := range f.Blocks {
				for _, instr := range b.Instrs {
					call, isCall := instr.(*ssa.Call)
					if !isCall || call.Call.StaticCallee() != fn {
						continue
					}
					pc.callers = append(pc.callers, call.Call.Args)
				}
			}
		}
		fmt.Printf("DEBUG param callers=%d\n", len(pc.callers))
		if ext.paramCallerCache == nil {
			ext.paramCallerCache = map[*ssa.Function]*paramCalls{}
		}
		ext.paramCallerCache[key] = pc
	}
	for _, args := range pc.callers {
		off := 0
		if call := pc.callers[0]; false {
			_ = call
		}
		// 方法调用 Args[0]=receiver；普通函数 Args[0]=首参
		if fn.Signature.Recv() != nil {
			off = 1
		}
		if paramIdx+off >= len(args) {
			continue
		}
		if s := ext.resolveSQLString(args[paramIdx+off], depth+1); s != "" {
			return s
		}
	}
	return ""
}

// allFunctions 全部 SSA 函数（prog 缓存）。
func (ext *fieldExtractor) allFunctions() []*ssa.Function {
	if ext.funcCache != nil {
		return ext.funcCache
	}
	var out []*ssa.Function
	for f := range ssautil.AllFunctions(ext.prog) {
		out = append(out, f)
	}
	ext.funcCache = out
	return out
}
