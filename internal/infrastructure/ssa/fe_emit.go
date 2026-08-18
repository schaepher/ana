package ssa

import (
	"go/token"
	"go/types"
	"sync/atomic"

	"github.com/schaepher/codeintel/internal/domain"
	"go.uber.org/zap"
	"golang.org/x/tools/go/ssa"
)

// emitFunctionFields 发射单个函数内的字段访问节点与数据流边。
func emitFunctionFields(repo *domain.Repository, prog *ssa.Program, fn *ssa.Function,
	funcID domain.CanonicalID, idents map[token.Pos]string, assignTargets []assignTarget,
	funcData *funcData, specs map[string]summarySpec, fallbackTotal *atomic.Int64, emit domain.EmitFunc,
	pkgs []*types.Package, dispatchRegs *dispatchReg) error {
	logger := zap.L()
	logger.Debug("enter emitFunctionFields")
	defer logger.Debug("exit emitFunctionFields")
	if len(fn.Blocks) == 0 {
		return nil
	}
	ext := &fieldExtractor{
		repo:   repo,
		prog:   prog,
		pkgs:   pkgs,
		fn:     fn,
		funcID: funcID,

		currentFile:   relPath(repo.Path, prog.Fset.PositionFor(fn.Pos(), false).Filename),
		idents:        idents,
		assignTargets: assignTargets,
		emit:          emit,
		funcData:      funcData,
		specs:         specs,
		fields:        map[*ssa.FieldAddr]*fieldAccess{},
		reads:         map[*ssa.FieldAddr]*fieldAccess{},
		indexes:       map[*ssa.IndexAddr]*fieldAccess{},
		indexReads:    map[*ssa.IndexAddr]*fieldAccess{},
		values:        map[ssa.Value]domain.CanonicalID{},
		funcIDs:       map[*ssa.Function]domain.CanonicalID{},
		slotsFor:      map[domain.CanonicalID]map[string]bool{funcID: {}},
		extSummaries:  map[domain.CanonicalID]bool{},
		rets:          map[*ssa.Function][][]ssa.Value{},
		dispatchRegs:  *dispatchRegs,
		chainTables:   map[ssa.Value]string{},
		tableNames:    map[*types.Named]string{},
	}

	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			fa, ok := instr.(*ssa.FieldAddr)
			if !ok {
				ia, ok2 := instr.(*ssa.IndexAddr)
				if !ok2 {
					continue
				}
				if !isSliceLike(ia.X.Type()) {
					continue
				}

				if ext.prog.Fset.PositionFor(ia.Pos(), false).Line == 0 {
					continue
				}
				hasStore, hasDeref := faUses(ia)
				if hasStore || !hasDeref {
					if f := ext.newElementAccess(ia.X, ia.Index, ia.Pos(), "write", ""); f != nil {
						ext.indexes[ia] = f
					}
				}
				if hasDeref {
					if f := ext.newElementAccess(ia.X, ia.Index, ia.Pos(), "read", ""); f != nil {
						ext.indexReads[ia] = f
					}
				}
				continue
			}

			hasStore, hasDeref := fieldAddrUse(fa)
			if hasStore || !hasDeref {

				if f := ext.newFieldAccess(fa, "write"); f != nil {
					ext.fields[fa] = f
				}
			}
			if hasDeref {

				if f := ext.newFieldAccess(fa, "read"); f != nil {
					ext.reads[fa] = f
				}
			}
		}
	}

	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			switch v := instr.(type) {
			case *ssa.Lookup:

				if !isSliceLike(v.X.Type()) && !isMapLike(v.X.Type()) {
					continue
				}
				if f := ext.newElementAccess(v.X, v.Index, v.Pos(), "read", ""); f != nil {
					if err := f.emit(); err != nil {
						return err
					}

					if err := ext.emitFlow(f.id, v); err != nil {
						return err
					}
				}
			case *ssa.Index:
				if !isSliceLike(v.X.Type()) {
					continue
				}
				if f := ext.newElementAccess(v.X, v.Index, v.Pos(), "read", ""); f != nil {
					if err := f.emit(); err != nil {
						return err
					}
					if err := ext.emitFlow(f.id, v); err != nil {
						return err
					}
				}
			case *ssa.MapUpdate:
				if !isMapLike(v.Map.Type()) {
					continue
				}
				if f := ext.newElementAccess(v.Map, v.Key, v.Pos(), "write", ""); f != nil {
					if err := f.emit(); err != nil {
						return err
					}

					if err := ext.emitFlowValue(v.Map, f.id); err != nil {
						return err
					}
					if err := ext.emitFlowValue(v.Value, f.id); err != nil {
						return err
					}
				}
			case *ssa.Send:
				if !isChanLike(v.Chan.Type()) {
					continue
				}

				if f := ext.newElementAccess(v.Chan, nil, v.Pos(), "write", "[send]"); f != nil {
					if err := f.emit(); err != nil {
						return err
					}
					if err := ext.emitFlowValue(v.Chan, f.id); err != nil {
						return err
					}
					if err := ext.emitFlowValue(v.X, f.id); err != nil {
						return err
					}
				}
			case *ssa.Range:
				if f := ext.newElementAccess(v.X, nil, v.Pos(), "read", ""); f != nil {
					if err := f.emit(); err != nil {
						return err
					}
					if err := ext.emitFlowValue(v.X, f.id); err != nil {
						return err
					}
				}
			case *ssa.IndexAddr:
				if f := ext.indexes[v]; f != nil {
					if err := f.emit(); err != nil {
						return err
					}
					if err := ext.emitFlowValue(v.X, f.id); err != nil {
						return err
					}
				}
				if f := ext.indexReads[v]; f != nil {
					if err := f.emit(); err != nil {
						return err
					}
					if err := ext.emitFlowValue(v.X, f.id); err != nil {
						return err
					}
				}
			case *ssa.Field:
				if f := ext.newFieldAccessValue(v); f != nil {
					if err := f.emit(); err != nil {
						return err
					}

					if err := ext.emitFlow(f.id, v); err != nil {
						return err
					}

					if err := ext.emitFlowValue(v.X, f.id); err != nil {
						return err
					}
				}
			case *ssa.Store:
				if g, ok := v.Addr.(*ssa.Global); ok {

					gID, err := ext.emitValue(g)
					if err == nil && gID != "" {
						if err := ext.emitFlowValue(v.Val, gID); err != nil {
							return err
						}
					}
					continue
				}
				if fa, ok := v.Addr.(*ssa.FieldAddr); ok {
					target, ok := ext.fields[fa]
					if !ok {
						continue
					}

					if err := ext.emitFlowValue(v.Val, target.id); err != nil {
						return err
					}
					continue
				}
				if ia, ok := v.Addr.(*ssa.IndexAddr); ok {
					target, ok := ext.indexes[ia]
					if !ok {
						continue
					}

					if err := ext.emitFlowValue(v.Val, target.id); err != nil {
						return err
					}
				}
			case *ssa.FieldAddr:
				base := v.X
				if f := ext.fields[v]; f != nil {
					if err := f.emit(); err != nil {
						return err
					}

					if err := ext.emitFlowValue(base, f.id); err != nil {
						return err
					}
				}
				if f := ext.reads[v]; f != nil {
					if err := f.emit(); err != nil {
						return err
					}

					if err := ext.emitFlowValue(base, f.id); err != nil {
						return err
					}
				}
			case *ssa.UnOp:
				if v.Op == token.ARROW {

					if !isChanLike(v.X.Type()) {
						continue
					}
					if f := ext.newElementAccess(v.X, nil, v.Pos(), "read", "[recv]"); f != nil {
						if err := f.emit(); err != nil {
							return err
						}
						if err := ext.emitFlow(f.id, v); err != nil {
							return err
						}
					}
					continue
				}
				if v.Op != token.MUL {
					continue
				}
				if ia, ok := v.X.(*ssa.IndexAddr); ok {

					if f := ext.indexReads[ia]; f != nil {
						if err := ext.emitFlow(f.id, v); err != nil {
							return err
						}
					}
					continue
				}
				fa, ok := v.X.(*ssa.FieldAddr)
				if !ok {
					continue
				}
				f, ok := ext.reads[fa]
				if !ok {
					continue
				}

				if err := ext.emitFlow(f.id, v); err != nil {
					return err
				}
			}
		}
	}

	err := ext.emitCrossFlow()
	if fallbackTotal != nil {
		fallbackTotal.Add(int64(ext.fallbackCount))
	}
	return err
}
