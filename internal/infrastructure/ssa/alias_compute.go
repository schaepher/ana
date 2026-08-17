package ssa

import (
	"go/ast"
	"go/token"
	"strings"
	"time"

	"github.com/schaepher/codeintel/internal/domain"
	"go.uber.org/zap"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

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
		lines:       map[string][]string{},
		calleeInfo:  map[*ssa.Function]*calleeWritesInfo{},
		rets:        map[*ssa.Function][][]ssa.Value{},
	}
	// 项目内函数（FuncDecl 过滤，与 emitFunction 一致）
	var funcs []*ssa.Function
	for fn := range ssautil.AllFunctions(prog) {
		if !isModuleFunction(fn, repo.Modules) {
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
	// 收集调用点 + 参与字段访问的值（Q166：每 500 函数进度打点）
	var sites []callSite
	aliasStart := time.Now()
	aliasDone := 0
	aliasTick := func() {
		aliasDone++
		if aliasDone%500 == 0 || aliasDone == len(funcs) {
			logger.Info("alias progress",
				zap.Int("funcs", aliasDone), zap.Int("total", len(funcs)),
				zap.Int("percent", aliasDone*100/len(funcs)),
				zap.Duration("elapsed", time.Since(aliasStart)))
		}
	}
	for _, fn := range funcs {
		if !p.underLimit(fn) {
			continue
		}
		p.collectFieldValues(fn)
		aliasTick()
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
					clearMayCache(p.may[p.funcIDs[s.callee]])
					changed = true
				}
			}
			if s.callVal != nil && s.callee.Signature.Results().Len() > 0 {
				for _, ret := range p.returnOperandsCached(s.callee) {
					for _, op := range ret {
						cm := p.callMay[s.callVal]
						if mergeMay(&cm, p.mayOf(s.callee, op)) {
							p.callMay[s.callVal] = cm
							clearMayCache(p.may[p.funcIDs[s.caller]])
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

	for _, s := range sites {
		p.processCall(s)
	}

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

// clearMayCache 清空函数的 may 缓存（保留顶层条目：mayOfDepth 直接对
// p.may[id][v] 赋值，删条目会让 p.may[id] 变 nil 导致 panic——S1 修复
// 踩过）。nil 安全。
func clearMayCache(m map[ssa.Value]map[ssa.Value]bool) {
	if m == nil {
		return
	}
	clear(m)
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

// callArgNames 提取调用点非 const 实参的源码变量名（Q90 回连展示；
// Alloc 从标识符索引恢复，其余回退 SSA 名）。
func callArgNames(p *aliasPass, cc *ssa.CallCommon) string {
	var names []string
	for _, arg := range cc.Args {
		if _, isConst := arg.(*ssa.Const); isConst {
			continue
		}
		name := arg.Name()
		if alloc, ok := arg.(*ssa.Alloc); ok {
			if n, ok2 := p.idents[alloc.Pos()]; ok2 {
				name = n
			}
		}
		names = append(names, name)
	}
	return strings.Join(names, ", ")
}
