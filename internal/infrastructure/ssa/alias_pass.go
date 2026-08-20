package ssa

import (
	"go/token"

	"github.com/schaepher/codeintel/internal/domain"
	"golang.org/x/tools/go/ssa"
)

// underLimit 每函数 alloc 上限检查（Q10）。
func (p *aliasPass) underLimit(fn *ssa.Function) bool {
	n := 0
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			if _, ok := instr.(*ssa.Alloc); ok {
				n++
				if n > MaxAllocsPerFunc {
					return false
				}
			}
		}
	}
	return true
}

// collectFieldValues 收集参与字段访问的值（alias 边范围，Q53 精神）。
func (p *aliasPass) collectFieldValues(fn *ssa.Function) {
	id := p.funcIDs[fn]
	vals := p.fieldValues[id]
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			switch v := instr.(type) {
			case *ssa.FieldAddr:
				vals[v.X] = true
			case *ssa.Field:
				vals[v.X] = true
			case *ssa.Store:
				if _, ok := v.Addr.(*ssa.FieldAddr); ok {
					vals[v.Val] = true
				}
			}
		}
	}
}

// mayOf 过程内值 → alloc 集（跨函数注入经 paramMay/callMay）。
func (p *aliasPass) mayOf(fn *ssa.Function, v ssa.Value) map[ssa.Value]bool {
	return p.mayOfDepth(fn, v, map[ssa.Value]bool{}, 0)
}

// mayOfDepth 递归实现，visiting 防环（phi 环 / 互赋值 `a:=b;b:=a`），
// 深度限制防栈溢出；环截断返回空（保守：excluded 判定对空集走 fallback）。
func (p *aliasPass) mayOfDepth(fn *ssa.Function, v ssa.Value,
	visiting map[ssa.Value]bool, depth int) map[ssa.Value]bool {
	if depth > 64 || visiting[v] {
		return nil
	}
	id := p.funcIDs[fn]
	if m, ok := p.may[id][v]; ok {
		return m
	}
	visiting[v] = true
	defer delete(visiting, v)
	out := map[ssa.Value]bool{}
	switch x := v.(type) {
	case *ssa.Alloc, *ssa.MakeMap, *ssa.MakeSlice, *ssa.MakeChan:
		out[x] = true
	case *ssa.UnOp:
		if x.Op == token.MUL {
			if alloc, ok := x.X.(*ssa.Alloc); ok {

				if alloc.Referrers() != nil {
					for _, ref := range *alloc.Referrers() {
						if st, ok := ref.(*ssa.Store); ok && st.Addr == alloc {
							mergeMay(&out, p.mayOfDepth(fn, st.Val, visiting, depth+1))
						}
					}
				}
				if len(out) == 0 {
					out[alloc] = true
				}
				break
			}
			out = p.mayOfDepth(fn, x.X, visiting, depth+1)
		}
	case *ssa.FieldAddr:
		out = p.mayOfDepth(fn, x.X, visiting, depth+1)
	case *ssa.Field:
		out = p.mayOfDepth(fn, x.X, visiting, depth+1)
	case *ssa.Phi:
		for _, op := range x.Edges {
			mergeMay(&out, p.mayOfDepth(fn, op, visiting, depth+1))
		}
	case *ssa.Parameter:
		out = p.paramMay[x]
	case *ssa.Call:
		out = p.callMay[x]
	}
	p.may[id][v] = out
	return out
}

// processCall 处理单个调用点：判定排除集并发射 alias 边。
func (p *aliasPass) processCall(s callSite) {
	callerID := p.funcIDs[s.caller]
	calleeID := p.funcIDs[s.callee]

	argMay := map[ssa.Value]bool{}
	hasInstanceArg := false
	for _, arg := range s.cc.Args {
		if _, isConst := arg.(*ssa.Const); isConst {
			continue
		}
		hasInstanceArg = true
		mergeMay(&argMay, p.mayOf(s.caller, arg))
	}

	info := p.writeInfoOf(s.callee)

	for _, w := range info.writes {
		if len(argMay) == 0 {
			continue
		}
		baseMay := p.mayOf(s.callee, w.container)
		if len(baseMay) == 0 || !overlapMay(argMay, baseMay) {
			continue
		}
		if fd := p.funcData[callerID]; fd != nil {
			fd.indirectWrites = append(fd.indirectWrites, fieldEntry{
				fieldPath:    w.path,
				instancePath: w.path,
				line:         p.prog.Fset.PositionFor(w.pos, false).Line,
				callLine:     p.prog.Fset.PositionFor(s.cc.Pos(), false).Line,
				callArg:      callArgNames(p, s.cc),
			})
		}
	}

	excl := p.excluded[callerID]
	if excl == nil {
		excl = map[domain.CanonicalID]map[string]bool{}
		p.excluded[callerID] = excl
	}
	calExcl := excl[calleeID]
	if calExcl == nil {
		calExcl = map[string]bool{}
		excl[calleeID] = calExcl
	}
	for fieldPath, base := range info.faBase {
		if !hasInstanceArg {
			calExcl[fieldPath] = true
			continue
		}
		if len(argMay) == 0 {
			continue
		}
		baseMay := p.mayOf(s.callee, base)
		if len(baseMay) == 0 {
			continue
		}
		overlap := false
		for a := range baseMay {
			if argMay[a] {
				overlap = true
				break
			}
		}
		if !overlap {
			calExcl[fieldPath] = true
		}
	}
}

// writeInfoOf 惰性构建被调函数的写指令静态信息（字段写 faBase + 元素写列表）。
// 语义与原 processCall 内联扫描一致；每个被调函数只扫描一次，
// 多个调用点共享缓存（避免 O(调用点×被调函数大小) 的重复遍历）。
func (p *aliasPass) writeInfoOf(fn *ssa.Function) *calleeWritesInfo {
	if info, ok := p.calleeInfo[fn]; ok {
		return info
	}
	info := &calleeWritesInfo{faBase: map[string]ssa.Value{}}
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			switch v := instr.(type) {
			case *ssa.Store:
				fa, ok := v.Addr.(*ssa.FieldAddr)
				if !ok {
					continue
				}
				fi, ok := p.fieldInfoFor(fa)
				if !ok {
					continue
				}
				info.faBase[fi.fullPath] = fa.X
			case *ssa.MapUpdate:
				if !isMapLike(v.Map.Type()) {
					continue
				}
				if path, ok2 := p.elementWritePath(v.Map, v.Key); ok2 {
					info.writes = append(info.writes, writeInstr{container: v.Map, path: path, pos: v.Pos()})
				}
			case *ssa.Send:
				if !isChanLike(v.X.Type()) {
					continue
				}

				if path, ok2 := p.elementWritePath(v.X, nil); ok2 {
					info.writes = append(info.writes, writeInstr{container: v.X, path: path + "[send]", pos: v.Pos()})
				}
			case *ssa.IndexAddr:

				if !isSliceLike(v.X.Type()) || v.Referrers() == nil {
					continue
				}

				if p.prog.Fset.PositionFor(v.Pos(), false).Line == 0 {
					continue
				}
				for _, ref := range *v.Referrers() {
					if st, ok2 := ref.(*ssa.Store); ok2 && st.Addr == v {
						if path, ok3 := p.elementWritePath(v.X, v.Index); ok3 {
							info.writes = append(info.writes, writeInstr{container: v.X, path: path, pos: st.Pos()})
						}
					}
				}
			}
		}
	}
	p.calleeInfo[fn] = info
	return info
}

// returnOperandsCached 惰性缓存函数的 Return 指令操作数（returns 传播复用）。
func (p *aliasPass) returnOperandsCached(fn *ssa.Function) [][]ssa.Value {
	if rets, ok := p.rets[fn]; ok {
		return rets
	}
	rets := returnOperands(fn)
	p.rets[fn] = rets
	return rets
}

// emitAliasEdges 发射 值 → alloc 的 ALIAS 边（may，conf 0.8）。
func (p *aliasPass) emitAliasEdges(fn *ssa.Function, v ssa.Value) {
	id, ok := p.valueNodeID(v)
	if !ok {
		return
	}
	for obj := range p.mayOf(fn, v) {
		allocID, ok := p.objectIDOf(obj)
		if !ok {
			continue
		}
		if err := p.emit(domain.Item{Fact: &domain.Fact{
			SourceID:   id,
			TargetID:   allocID,
			Kind:       domain.FactAlias,
			ToolSource: domain.ToolSSA,
			Confidence: 0.8,
		}}); err != nil {
			return
		}
	}
}
