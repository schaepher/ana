// 轻量别名分析（field_trace.md §14.8，Q80）：
//   - 过程内：值 → 指向的 alloc 集合（may 传播，SSA def-use 链）
//   - 跨函数：实参→形参、返回值→调用者，沿调用图迭代至稳定（Q11 双向）
//   - 间接写排除集：调用点实参 may 集与被调写字段 base may 集无交集
//     （或实参集为空）→ 确认不别名 → 间接写判定排除（Q4/Q5：别名优先，
//     排除"确定不别名"，其余走类型级 fallback）
//   - ALIAS 边：may 关系（值 → alloc）落库（conf 0.8，Q3/Q6 锚点式）
//   - 上限：每函数 200 alloc（Q10），超限跳过该函数（不排除不产边）
package ssa

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"

	"github.com/schaepher/codeintel/internal/domain"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
	"go.uber.org/zap"
)

// MaxAllocsPerFunc 每函数参与别名分析的 alloc 上限（Q10）。
const MaxAllocsPerFunc = 200

// aliasResult 别名分析结果。
type aliasResult struct {
	// excluded[callerID][calleeID][fieldPath]：确认无别名的间接写候选
	excluded map[domain.CanonicalID]map[domain.CanonicalID]map[string]bool
}

// callSite 静态调用点（跨函数传播单元）。
type callSite struct {
	caller *ssa.Function
	callee *ssa.Function
	cc     *ssa.CallCommon
	callVal ssa.Value // *ssa.Call 指令（returns 传播目标）；Go/Defer 为 nil
}

// aliasPass 别名分析状态。
type aliasPass struct {
	repo    *domain.Repository
	prog    *ssa.Program
	idents  map[token.Pos]string
	emit    domain.EmitFunc
	funcIDs map[*ssa.Function]domain.CanonicalID
	// may[funcID]：函数内 值 → alloc 集（含跨函数注入）
	may         map[domain.CanonicalID]map[ssa.Value]map[ssa.Value]bool
	allocIDs    map[ssa.Value]domain.CanonicalID
	paramMay    map[*ssa.Parameter]map[ssa.Value]bool
	callMay     map[ssa.Value]map[ssa.Value]bool
	excluded    map[domain.CanonicalID]map[domain.CanonicalID]map[string]bool
	fieldValues map[domain.CanonicalID]map[ssa.Value]bool // 参与字段访问的值（alias 边范围）
	slotSeen    map[domain.CanonicalID]map[string]bool
	funcData    map[domain.CanonicalID]*funcData // 元素间接写条目（Q83）
}

// computeAliases 执行轻量别名分析，返回间接写排除集（emitSummaries 消费）。
func computeAliases(repo *domain.Repository, prog *ssa.Program,
	idents map[token.Pos]string, funcData map[domain.CanonicalID]*funcData,
	emit domain.EmitFunc) (*aliasResult, error) {
	logger := zap.L()
	logger.Debug("enter computeAliases")
	defer logger.Debug("exit computeAliases")
	p := &aliasPass{
		repo:        repo,
		prog:        prog,
		idents:      idents,
		emit:        emit,
		funcIDs:     map[*ssa.Function]domain.CanonicalID{},
		may:         map[domain.CanonicalID]map[ssa.Value]map[ssa.Value]bool{},
		allocIDs:    map[ssa.Value]domain.CanonicalID{},
		paramMay:    map[*ssa.Parameter]map[ssa.Value]bool{},
		callMay:     map[ssa.Value]map[ssa.Value]bool{},
		excluded:    map[domain.CanonicalID]map[domain.CanonicalID]map[string]bool{},
		fieldValues: map[domain.CanonicalID]map[ssa.Value]bool{},
		slotSeen:    map[domain.CanonicalID]map[string]bool{},
		funcData:    funcData,
	}
	// 项目内函数（FuncDecl 过滤，与 emitFunction 一致）
	var funcs []*ssa.Function
	for fn := range ssautil.AllFunctions(prog) {
		if !isModuleFunction(fn, repo.Module) {
			continue
		}
		if _, ok := fn.Syntax().(*ast.FuncDecl); !ok {
			continue
		}
		id, ok := p.funcIDOf(fn)
		if !ok {
			continue
		}
		p.funcIDs[fn] = id
		p.may[id] = map[ssa.Value]map[ssa.Value]bool{}
		p.fieldValues[id] = map[ssa.Value]bool{}
		funcs = append(funcs, fn)
	}
	// 收集调用点 + 参与字段访问的值
	var sites []callSite
	for _, fn := range funcs {
		if !p.underLimit(fn) {
			continue // 超上限：跳过该函数（Q10）
		}
		p.collectFieldValues(fn)
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				var cc *ssa.CallCommon
				var callVal ssa.Value
				switch v := instr.(type) {
				case *ssa.Call:
					cc = &v.Call
					callVal = v
				case *ssa.Go:
					cc = &v.Call
				case *ssa.Defer:
					cc = &v.Call
				}
				if cc == nil {
					continue
				}
				callee := resolveStaticCallee(cc)
				if callee == nil {
					continue
				}
				if _, ok := p.funcIDs[callee]; !ok {
					continue
				}
				sites = append(sites, callSite{caller: fn, callee: callee, cc: cc, callVal: callVal})
			}
		}
	}
	// 跨函数传播：实参→形参、returns→调用者，迭代至稳定（上限 20 轮）
	for round := 0; round < 20; round++ {
		changed := false
		for _, s := range sites {
			for i, arg := range s.cc.Args {
				if i >= len(s.callee.Params) {
					break
				}
				param := s.callee.Params[i]
				pm := p.paramMay[param]
				if mergeMay(&pm, p.mayOf(s.caller, arg)) {
					p.paramMay[param] = pm
					changed = true
				}
			}
			if s.callVal != nil && s.callee.Signature.Results().Len() > 0 {
				for _, ret := range returnOperands(s.callee) {
					for _, op := range ret {
						cm := p.callMay[s.callVal]
						if mergeMay(&cm, p.mayOf(s.callee, op)) {
							p.callMay[s.callVal] = cm
							changed = true
						}
					}
				}
			}
		}
		if !changed {
			break
		}
	}
	// 间接写排除集（调用点级）
	for _, s := range sites {
		p.processCall(s)
	}
	// alias 边：过程内 + 跨函数注入后，所有参与字段访问的值 → alloc（Q3/Q6）
	for _, fn := range funcs {
		if !p.underLimit(fn) {
			continue
		}
		for v := range p.fieldValues[p.funcIDs[fn]] {
			p.emitAliasEdges(fn, v)
		}
	}
	return &aliasResult{excluded: p.excluded}, nil
}

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
		out[x] = true // 对象创建点：别名锚点（Q6 扩展）
	case *ssa.UnOp:
		if x.Op == token.MUL {
			if alloc, ok := x.X.(*ssa.Alloc); ok {
				// 局部变量读取：值来自对 alloc 的 Store（单次赋值近似，
				// `b := a` 的赋值别名）；无 Store（取地址本身）→ alloc 自身
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

// mergeMay 合并 src 到 dst（dst 为 nil 时创建），返回是否有新增。
func mergeMay(dst *map[ssa.Value]bool, src map[ssa.Value]bool) bool {
	if len(src) == 0 {
		return false
	}
	if *dst == nil {
		*dst = map[ssa.Value]bool{}
	}
	changed := false
	for a := range src {
		if !(*dst)[a] {
			(*dst)[a] = true
			changed = true
		}
	}
	return changed
}

// processCall 处理单个调用点：判定排除集并发射 alias 边。
func (p *aliasPass) processCall(s callSite) {
	callerID := p.funcIDs[s.caller]
	calleeID := p.funcIDs[s.callee]
	// 调用点实参 may 集（非 const 实参）
	argMay := map[ssa.Value]bool{}
	hasInstanceArg := false // 存在非 const 实参（实例传递通道）
	for _, arg := range s.cc.Args {
		if _, isConst := arg.(*ssa.Const); isConst {
			continue
		}
		hasInstanceArg = true
		mergeMay(&argMay, p.mayOf(s.caller, arg))
	}
	// 扫描被调写字段指令（fieldPath → base）
	faBase := map[string]ssa.Value{}
	for _, b := range s.callee.Blocks {
		for _, instr := range b.Instrs {
			st, ok := instr.(*ssa.Store)
			if !ok {
				continue
			}
			fa, ok := st.Addr.(*ssa.FieldAddr)
			if !ok {
				continue
			}
			info, ok := p.fieldInfoFor(fa)
			if !ok {
				continue
			}
			faBase[info.fullPath] = fa.X
		}
	}
	// 元素写（map/slice 元素间接写，Q83）：只走别名命中（Q7a-②）
	// 容器 base may ∩ 调用点实参 may ≠ ∅ → 调用者间接写条目
	for _, b := range s.callee.Blocks {
		for _, instr := range b.Instrs {
			var container, key ssa.Value
			var pos token.Pos
			switch v := instr.(type) {
			case *ssa.MapUpdate:
				if !isMapLike(v.Map.Type()) {
					continue
				}
				container, key, pos = v.Map, v.Key, v.Pos()
			case *ssa.Send:
				if !isChanLike(v.X.Type()) {
					continue
				}
				// channel 发送 = 写元素（Q83 扩展）
				if path, ok2 := p.elementWritePath(v.X, nil); ok2 {
					if len(argMay) > 0 && overlapMay(argMay, p.mayOf(s.callee, v.X)) {
						if fd := p.funcData[callerID]; fd != nil {
							fd.indirectWrites = append(fd.indirectWrites, fieldEntry{
								fieldPath:    path + "[send]",
								instancePath: path + "[send]",
								line:         p.prog.Fset.PositionFor(v.Pos(), false).Line,
							})
						}
					}
				}
				continue
			case *ssa.Store:
				ia, ok := v.Addr.(*ssa.IndexAddr)
				if !ok || !isSliceLike(ia.X.Type()) {
					continue
				}
				container, key, pos = ia.X, ia.Index, v.Pos()
			default:
				continue
			}
			path, ok := p.elementWritePath(container, key)
			if !ok {
				continue
			}
			if len(argMay) == 0 {
				continue
			}
			baseMay := p.mayOf(s.callee, container)
			if len(baseMay) == 0 || !overlapMay(argMay, baseMay) {
				continue
			}
			if fd := p.funcData[callerID]; fd != nil {
				fd.indirectWrites = append(fd.indirectWrites, fieldEntry{
					fieldPath:    path,
					instancePath: path,
					line:         p.prog.Fset.PositionFor(pos, false).Line,
				})
			}
		}
	}
	// 判定排除（确认不别名）
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
	for fieldPath, base := range faBase {
		if !hasInstanceArg {
			calExcl[fieldPath] = true // 无实例传递（无参/全 const）：写的是被调内部对象
			continue
		}
		if len(argMay) == 0 {
			continue // 实参 may 未知（参数未被调用注入）：fallback 类型级（Q5）
		}
		baseMay := p.mayOf(s.callee, base)
		if len(baseMay) == 0 {
			continue // 分析不出：fallback 类型级（Q5）
		}
		overlap := false
		for a := range baseMay {
			if argMay[a] {
				overlap = true
				break
			}
		}
		if !overlap {
			calExcl[fieldPath] = true // 确认不别名：排除
		}
	}
}

// emitAliasEdges 发射 值 → alloc 的 ALIAS 边（may，conf 0.8）。
func (p *aliasPass) emitAliasEdges(fn *ssa.Function, v ssa.Value) {
	id, ok := p.funcIDOfValue(v)
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

// objectIDOf 确保对象创建点（alloc / MakeMap / MakeSlice）的 ssa_value
// 节点发射（Q7：被别名引用的对象也发射）。
func (p *aliasPass) objectIDOf(obj ssa.Value) (domain.CanonicalID, bool) {
	if id, ok := p.allocIDs[obj]; ok {
		return id, true
	}
	fn := obj.Parent()
	if fn == nil {
		return "", false
	}
	funcID, ok := p.funcIDOf(fn)
	if !ok {
		return "", false
	}
	slots := p.slotSeen[funcID]
	if slots == nil {
		slots = map[string]bool{}
		p.slotSeen[funcID] = slots
	}
	slot := obj.Name()
	if slots[slot] {
		line := p.prog.Fset.PositionFor(obj.Pos(), false).Line
		slot = fmt.Sprintf("%s@%d", slot, line)
	} else {
		slots[slot] = true
	}
	id := domain.CanonicalID(string(funcID) + "#" + slot)
	p.allocIDs[obj] = id
	kind := "alloc"
	if _, isMap := obj.(*ssa.MakeMap); isMap {
		kind = "make"
	}
	if err := p.emit(domain.Item{Node: &domain.CodeEntity{
		ID:   id,
		Kind: domain.KindSSAValue,
		Name: slot,
		Properties: map[string]any{
			"origin_kind": kind,
			"ssa_op":      ssaOp(obj),
			"type_string": obj.Type().String(),
			"func_id":     string(funcID),
		},
	}}); err != nil {
		return "", false
	}
	return id, true
}

// fieldInfoFor 计算写字段指令的类型限定路径（复用 fieldInfo 语义）。
func (p *aliasPass) fieldInfoFor(fa *ssa.FieldAddr) (fieldInfo, bool) {
	named, st := derefStruct(fa.X.Type())
	if named == nil {
		return fieldInfo{}, false
	}
	field := st.Field(fa.Field)
	fi := fieldInfo{
		fullPath:   named.Obj().Pkg().Path() + "." + named.Obj().Name() + "." + field.Name(),
		fieldName:  field.Name(),
		typeString: field.Type().String(),
	}
	pos := p.prog.Fset.PositionFor(fa.Pos(), false)
	fi.filePath = relPath(p.repo.Path, pos.Filename)
	fi.line = pos.Line
	if fi.line > 0 {
		fi.snippet = sourceLineAt(p.repo.Path, fi.filePath, fi.line)
	}
	return fi, true
}

// funcIDOf 函数 → canonical ID（缓存）。
func (p *aliasPass) funcIDOf(fn *ssa.Function) (domain.CanonicalID, bool) {
	if id, ok := p.funcIDs[fn]; ok {
		return id, true
	}
	obj, ok := fn.Object().(*types.Func)
	if !ok || obj == nil {
		return "", false
	}
	id, _, _ := funcIdentity(obj)
	if id == "" {
		return "", false
	}
	p.funcIDs[fn] = id
	return id, true
}

// funcIDOfValue 值 → 所属函数 ID。
func (p *aliasPass) funcIDOfValue(v ssa.Value) (domain.CanonicalID, bool) {
	if fn, ok := v.(*ssa.Function); ok {
		return p.funcIDOf(fn)
	}
	parent := v.Parent()
	if parent == nil {
		return "", false
	}
	return p.funcIDOf(parent)
}

// sourceLineAt 读源码行。
func sourceLineAt(repoPath, filePath string, line int) string {
	if line <= 0 || filePath == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(repoPath, filepath.FromSlash(filePath)))
	if err != nil {
		return ""
	}
	lines := strings.Split(string(data), "\n")
	if line > len(lines) {
		return ""
	}
	return strings.TrimSpace(lines[line-1])
}

// elementWritePath 生成元素写路径（间接写条目用，Q5 记号）。
func (p *aliasPass) elementWritePath(container, key ssa.Value) (string, bool) {
	full := ""
	if un, ok := container.(*ssa.UnOp); ok && un.Op == token.MUL {
		container = un.X
	}
	if fa, ok := container.(*ssa.FieldAddr); ok {
		if info, ok2 := p.fieldInfoFor(fa); ok2 {
			full = info.fullPath
		}
	}
	if full == "" {
		full = namedContainerOf(container.Type())
	}
	if full == "" {
		return "", false // 无法标识（内联容器）：跳过
	}
	return full + elementMark(key), true
}

// overlapMay 判断两个 may 集是否有交集。
func overlapMay(a, b map[ssa.Value]bool) bool {
	for x := range a {
		if b[x] {
			return true
		}
	}
	return false
}
