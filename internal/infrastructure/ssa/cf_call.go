package ssa

import (
	"go/types"

	"github.com/schaepher/codeintel/internal/domain"
	"go.uber.org/zap"
	"golang.org/x/tools/go/ssa"
)

// emitCall 处理单个调用点：argument / returns 边 + 摘要调用记录。
// 仅处理静态可解析且属于项目内的被调函数。
func (ext *fieldExtractor) emitCall(cc *ssa.CallCommon, callVal ssa.Value) error {
	logger := zap.L()
	logger.Debug("enter (fieldExtractor).emitCall")
	defer logger.Debug("exit (fieldExtractor).emitCall")
	callee := resolveStaticCallee(cc)
	if callee == nil {

		if cc.Method != nil {
			if iface := interfaceNamedOf(cc.Value.Type()); iface != nil {
				impls := implMethodsFor(ext.pkgs, ext.repo.Modules, iface, cc.Method.Name())
				for _, implFn := range impls {
					implSSA := ext.prog.FuncValue(implFn)
					if implSSA == nil {
						continue
					}

					origin, conf := ext.dispatchOriginOf(iface, cc.Method.Name(), implFn)
					candMeta := map[string]any{
						"interface":        iface.String(),
						"candidate_origin": origin,
						"confidence":       conf,
					}

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
						if err := ext.emitEdgeKindMeta(argID, paramID, domain.FactArgument, candMeta); err != nil {
							return err
						}
					}

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
										if err := ext.emitEdgeKindMeta(opID, callID, domain.FactReturns, candMeta); err != nil {
											return err
										}
									}
								}
							} else if refs := callVal.Referrers(); refs != nil && len(*refs) > 0 {
								for _, ret := range rets {
									for _, op := range ret {
										opID, err := ext.emitValue(op)
										if err == nil && opID != "" {
											if err := ext.emitEdgeKindMeta(opID, callID, domain.FactReturns, candMeta); err != nil {
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

									if len(rets) > 0 && idx < len(rets[0]) {
										opID, err := ext.emitValue(rets[0][idx])
										if err == nil && opID != "" {
											if err := ext.emitEdgeKindMeta(opID, exID, domain.FactReturns, candMeta); err != nil {
												return err
											}
										}
									}
								}
							}
						}
					}

					if implID, ok := ext.funcIDOf(implSSA); ok {
						ext.recordCallInfo(cc, implID)
					}
				}

				logger.Debug("dyn interface dispatch", zap.Int("impls", len(impls)), zap.String("call", cc.String()), zap.String("iface", cc.Value.Type().String()))
				handled, err := ext.applyInterfaceSummary(cc, callVal)
				if err != nil {
					return err
				}
				if handled {
					return nil
				}
			}
		}
		return nil
	}

	handled, err := ext.applySummary(cc, callee, callVal)
	if err != nil {
		return err
	}
	if handled {
		return nil
	}
	if !isModuleFunction(callee, ext.repo.Modules) {
		return nil
	}
	calleeID, ok := ext.funcIDOf(callee)
	if !ok {
		return nil
	}

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

	ext.recordCallInfo(cc, calleeID)
	return nil
}

// dispatchOriginOf 判定候选实现的派发来源（Q161）：注册点命中
// （MakeInterface 具体值 → 接口，见 emitDispatches）→ register 0.9；
// 否则枚举兜底 enum 0.7。注册点收集一次缓存（全 prog 扫描开销大）。
// Q168：注册命中按 (iface, candidateKey) 预处理成 map——原逐调用点
// 线性扫描注册点（动态调用点多时 O(调用点×注册点)）→ O(1) 查找。
func (ext *fieldExtractor) dispatchOriginOf(iface *types.Named, method string, implFn *types.Func) (string, float64) {
	// Q221：dispatchRegs/regHits 均为 Index 级共享（Adapter 初始化一次）
	// ——原 extractor 懒构建导致每函数重复全量预处理
	if ext.regHits[iface.String()][candidateKey(implFn)] {
		return "register", 0.9
	}
	return "enum", 0.7
}

// recordCallInfo 记录调用摘要条目（间接写闭包消费：emitSummaries 沿
// fd.calls 传播被调函数写）。常量实参（nil、字面量）不产生实例传递，
// 不参与类型匹配；实参类型路径用于与被调函数写字段的声明类型匹配
// （Q36；Q157 展开嵌套字段 owner 链——OrderModel 含 Order 字段时
// 实现写 Order.FinalFee 也能匹配）。
func (ext *fieldExtractor) recordCallInfo(cc *ssa.CallCommon, calleeID domain.CanonicalID) {
	if ext.funcData == nil {
		return
	}
	var argPaths, argNames []string
	for _, arg := range cc.Args {
		if _, isConst := arg.(*ssa.Const); isConst {
			continue
		}
		for _, p := range ownerTypesOf(arg.Type(), 0) {
			argPaths = append(argPaths, p)
		}

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
