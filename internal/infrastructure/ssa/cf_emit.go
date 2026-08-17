package ssa

import (
	"github.com/schaepher/codeintel/internal/domain"
	"go.uber.org/zap"
	"golang.org/x/tools/go/ssa"
)

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
