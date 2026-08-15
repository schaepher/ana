// 跨过程数据流与摘要收集（field_trace.md §6.2/§7.5）：
//   - argument 边：静态调用实参 → 被调函数形参
//   - returns 边：被调函数返回值 → 调用点结果（多返回经 tuple → Extract 拆解）
//   - phi_operand 边：Phi 每个分支输入 → Phi
//   - funcData：direct 读写条目 + 静态调用记录（间接写闭包计算用）
//
// 动态调用（接口方法/函数值/闭包）v1 不解析：接口调用由 AST 适配器 calls 边覆盖，
// 函数值/闭包参数流待摘要系统（Phase 5）。
package ssa

import (
	"go/types"
	"strings"

	"github.com/schaepher/codeintel/internal/domain"
	"golang.org/x/tools/go/ssa"
	"go.uber.org/zap"
)

// funcData 单个函数的摘要收集数据（构建期内存态）。
type funcData struct {
	directReads    []fieldEntry
	directWrites   []fieldEntry
	calls          []callInfo
	indirectWrites []fieldEntry // 外部摘要写（虚拟节点，emitSummaries 合并输出）
}

// fieldEntry 单个字段访问的摘要条目。
type fieldEntry struct {
	fieldPath    string
	instancePath string
	line         int
	snippet      string
	callLine     int    // 调用点行号（间接写回连，Q90）
	callArg      string // 调用点实参变量名（间接写回连，Q90）
}

// callInfo 静态调用记录（间接写匹配用）。
type callInfo struct {
	calleeID       domain.CanonicalID
	argStructPaths []string
	callLine       int      // 调用点行号（Q90 调用点级回连）
	argNames       []string // 非 const 实参变量名（Q90）
}

// emitCrossFlow 发射单个函数的跨过程边并记录摘要数据。
func (ext *fieldExtractor) emitCrossFlow() error {
	logger := zap.L()
	logger.Debug("enter (fieldExtractor).emitCrossFlow")
	defer logger.Debug("exit (fieldExtractor).emitCrossFlow")
	for _, b := range ext.fn.Blocks {
		for _, instr := range b.Instrs {
			switch v := instr.(type) {
			case *ssa.Phi:
				if err := ext.emitPhi(v); err != nil {
					return err
				}
			case *ssa.Call:
				if err := ext.emitCall(&v.Call, v); err != nil {
					return err
				}
			case *ssa.Go:
				if err := ext.emitCall(&v.Call, nil); err != nil {
					return err
				}
			case *ssa.Defer:
				if err := ext.emitCall(&v.Call, nil); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// emitPhi 发射 phi_operand 边（常量分支跳过）。
func (ext *fieldExtractor) emitPhi(phi *ssa.Phi) error {
	logger := zap.L()
	logger.Debug("enter (fieldExtractor).emitPhi")
	defer logger.Debug("exit (fieldExtractor).emitPhi")
	phiID, err := ext.emitValue(phi)
	if err != nil || phiID == "" {
		return err
	}
	for _, op := range phi.Edges {
		if _, isConst := op.(*ssa.Const); isConst {
			continue
		}
		opID, err := ext.emitValue(op)
		if err != nil || opID == "" {
			continue
		}
		if err := ext.emitEdgeKind(opID, phiID, domain.FactPhiOperand); err != nil {
			return err
		}
	}
	return nil
}

// emitCall 处理单个调用点：argument / returns 边 + 摘要调用记录。
// 仅处理静态可解析且属于项目内的被调函数。
func (ext *fieldExtractor) emitCall(cc *ssa.CallCommon, callVal ssa.Value) error {
	logger := zap.L()
	logger.Debug("enter (fieldExtractor).emitCall")
	defer logger.Debug("exit (fieldExtractor).emitCall")
	callee := resolveStaticCallee(cc)
	if callee == nil {
		// ⑮ 接口动态派发：无静态 callee 但为具名接口方法调用时，枚举模块
		// 内候选实现，实参 → 候选 Params 建立 argument 边——追踪进入
		// 具体实现（此前动态调用不产边，字段链路断在接口调用点）
		if cc.Method != nil {
			if iface := interfaceNamedOf(cc.Value.Type()); iface != nil {
				for _, implFn := range implMethodsFor(ext.pkgs, ext.repo.Modules, iface, cc.Method.Name()) {
					implSSA := ext.prog.FuncValue(implFn)
					if implSSA == nil {
						continue
					}
					// 动态 invoke 的 cc.Args 不含接收者（在 cc.Value）——
					// 实参对应候选方法 Params[1:]（Params[0] 是 receiver）
					for i, arg := range cc.Args {
						if i+1 >= len(implSSA.Params) {
							break
						}
						if _, isConst := arg.(*ssa.Const); isConst {
							continue
						}
						argID, err := ext.emitValue(arg)
						if err != nil || argID == "" {
							continue
						}
						paramID, err := ext.emitValue(implSSA.Params[i+1])
						if err != nil || paramID == "" {
							continue
						}
						if err := ext.emitEdgeKind(argID, paramID, domain.FactArgument); err != nil {
							return err
						}
					}
					// returns 边：候选实现 Return 值 → 调用点结果
					// （举一反三——⑮ 只建了 argument，返回值贯通缺失）
					nResults := implSSA.Signature.Results().Len()
					if nResults > 0 && callVal != nil {
						callID, err := ext.emitValue(callVal)
						if err == nil && callID != "" {
							rets := ext.returnOperandsCached(implSSA)
							if nResults == 1 {
								for _, ret := range rets {
									if len(ret) == 0 {
										continue
									}
									opID, err := ext.emitValue(ret[0])
									if err == nil && opID != "" {
										if err := ext.emitEdgeKind(opID, callID, domain.FactReturns); err != nil {
											return err
										}
									}
								}
							} else if refs := callVal.Referrers(); refs != nil && len(*refs) > 0 {
								for _, ret := range rets {
									for _, op := range ret {
										opID, err := ext.emitValue(op)
										if err == nil && opID != "" {
											if err := ext.emitEdgeKind(opID, callID, domain.FactReturns); err != nil {
												return err
											}
										}
									}
								}
								for _, u := range *refs {
									ex, ok := u.(*ssa.Extract)
									if !ok || ex.Tuple != callVal {
										continue
									}
									idx := ex.Index
									exID, err := ext.emitValue(ex)
									if err != nil || exID == "" {
										continue
									}
									if idx < len(rets[0]) {
										opID, err := ext.emitValue(rets[0][idx])
										if err == nil && opID != "" {
											if err := ext.emitEdgeKind(opID, exID, domain.FactReturns); err != nil {
												return err
											}
										}
									}
								}
							}
						}
					}
				}
			}
		}
		return nil // 函数值调用：无法解析被调方
	}
	// 摘要优先：外部函数走内置/用户摘要；本地函数经 field-summary.yaml
	// 自定义条目（如 orm_write 的本地 ORM 层）。无匹配 spec 时 applySummary
	// 快速返回 false，本地函数继续走 argument/returns 边。
	handled, err := ext.applySummary(cc, callee, callVal)
	if err != nil {
		return err
	}
	if handled {
		return nil
	}
	if !isModuleFunction(callee, ext.repo.Modules) {
		return nil // 外部函数无摘要：不产调用边
	}
	calleeID, ok := ext.funcIDOf(callee)
	if !ok {
		return nil // 闭包等无可标识命名空间
	}

	// argument 边：实参 → 形参
	for i, arg := range cc.Args {
		if i >= len(callee.Params) {
			break
		}
		if _, isConst := arg.(*ssa.Const); isConst {
			continue
		}
		argID, err := ext.emitValue(arg)
		if err != nil || argID == "" {
			continue
		}
		paramID, err := ext.emitValue(callee.Params[i])
		if err != nil || paramID == "" {
			continue
		}
		if err := ext.emitEdgeKind(argID, paramID, domain.FactArgument); err != nil {
			return err
		}
	}

	// returns 边：被调返回值 → 调用点结果
	nResults := callee.Signature.Results().Len()
	if nResults > 0 && callVal != nil {
		callID, err := ext.emitValue(callVal)
		if err == nil && callID != "" {
			rets := ext.returnOperandsCached(callee)
			if nResults == 1 {
				for _, ret := range rets {
					if len(ret) == 0 {
						continue
					}
					opID, err := ext.emitValue(ret[0])
					if err == nil && opID != "" {
						if err := ext.emitEdgeKind(opID, callID, domain.FactReturns); err != nil {
							return err
						}
					}
				}
			} else if refs := callVal.Referrers(); refs != nil && len(*refs) > 0 {
				// 多返回：RETURNS 到 tuple，Extract 经 data_flows_to 拆解
				for _, ret := range rets {
					for _, op := range ret {
						opID, err := ext.emitValue(op)
						if err == nil && opID != "" {
							if err := ext.emitEdgeKind(opID, callID, domain.FactReturns); err != nil {
								return err
							}
						}
					}
				}
				for _, u := range *refs {
					ex, ok := u.(*ssa.Extract)
					if !ok {
						continue
					}
					exID, err := ext.emitValue(ex)
					if err == nil && exID != "" {
						if err := ext.emitEdge(callID, exID); err != nil {
							return err
						}
					}
				}
			}
		}
	}

	// 摘要收集（间接写闭包计算用）。
	// 常量实参（nil、字面量）不产生实例传递，不参与类型匹配
	if ext.funcData != nil {
		var argPaths, argNames []string
		for _, arg := range cc.Args {
			if _, isConst := arg.(*ssa.Const); isConst {
				continue
			}
			if p := structPathOfType(arg.Type()); p != "" {
				argPaths = append(argPaths, p)
			}
			// 实参变量名（Q90 调用点回连展示；SSA 临时名回退原名）
			name := ext.instancePath(arg)
			if isSSAName(name) {
				name = arg.Name()
			}
			argNames = append(argNames, name)
		}
		ext.funcData.calls = append(ext.funcData.calls, callInfo{
			calleeID:       calleeID,
			argStructPaths: argPaths,
			callLine:       ext.prog.Fset.PositionFor(cc.Pos(), false).Line,
			argNames:       argNames,
		})
	}
	return nil
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
	// 函数值由被调函数返回（f := getHandler(); f(x)）：追踪被调函数的
	// Return 操作数（举一反三 B4——此前仅直接函数值/phi 链可解析）
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

// emitEdgeKindLine 带行号的边（query table 写入方定位用；SQL/ORM
// 虚拟节点 summary_io 边的 line_num 此前缺失，聚合时只能兜底节点行号）。
func (ext *fieldExtractor) emitEdgeKindLine(from, to domain.CanonicalID, kind domain.FactKind, line int) error {
	logger := zap.L()
	logger.Debug("enter (fieldExtractor).emitEdgeKindLine")
	defer logger.Debug("exit (fieldExtractor).emitEdgeKindLine")
	return ext.emit(domain.Item{Fact: &domain.Fact{
		SourceID:   from,
		TargetID:   to,
		Kind:       kind,
		ToolSource: domain.ToolSSA,
		Confidence: 1.0,
		Metadata:   map[string]any{"line_num": line},
	}})
}

// emitEdgeKind 发射指定 kind 的边（tool_source=ssa，conf 1.0，Q69）。
func (ext *fieldExtractor) emitEdgeKind(from, to domain.CanonicalID, kind domain.FactKind) error {
	logger := zap.L()
	logger.Debug("enter (fieldExtractor).emitEdgeKind")
	defer logger.Debug("exit (fieldExtractor).emitEdgeKind")
	return ext.emit(domain.Item{Fact: &domain.Fact{
		SourceID:   from,
		TargetID:   to,
		Kind:       kind,
		ToolSource: domain.ToolSSA,
		Confidence: 1.0,
	}})
}
