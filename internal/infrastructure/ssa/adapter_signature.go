package ssa

import (
	"fmt"
	"go/token"

	"github.com/schaepher/codeintel/internal/domain"
	"go.uber.org/zap"
	"golang.org/x/tools/go/ssa"
)

func emitSignatureNodes(fn *ssa.Function, funcID domain.CanonicalID, pos token.Position,
	filePath string, emit domain.EmitFunc) error {
	logger := zap.L()
	logger.Debug("enter emitSignatureNodes")
	defer logger.Debug("exit emitSignatureNodes")
	sig := fn.Signature
	if sig == nil {
		return nil
	}

	if recvVar := sig.Recv(); recvVar != nil {
		name := recvVar.Name()
		if name == "" {
			name = "recv"
		}
		id := domain.CanonicalID(string(funcID) + "#param.recv." + name)
		if err := emit(domain.Item{Node: &domain.CodeEntity{
			ID:        id,
			Kind:      domain.KindReceiver,
			Name:      name,
			FilePath:  filePath,
			LineStart: pos.Line,
			LineEnd:   pos.Line,
			Properties: map[string]any{
				"type_string": recvVar.Type().String(),
				"index":       -1,
				"receiver":    "true",
				"func_id":     string(funcID),
			},
		}}); err != nil {
			return err
		}
		if err := emit(domain.Item{Fact: &domain.Fact{
			SourceID:   funcID,
			TargetID:   id,
			Kind:       domain.FactHasParam,
			ToolSource: domain.ToolSSA,
			Confidence: 1.0,
		}}); err != nil {
			return err
		}
	}

	n := sig.Params().Len()
	for i := 0; i < n; i++ {
		p := sig.Params().At(i)
		name := p.Name()
		if name == "" {
			name = fmt.Sprintf("arg%d", i)
		}
		id := domain.CanonicalID(string(funcID) + "#param." + name)
		if err := emit(domain.Item{Node: &domain.CodeEntity{
			ID:        id,
			Kind:      domain.KindParameter,
			Name:      name,
			FilePath:  filePath,
			LineStart: pos.Line,
			LineEnd:   pos.Line,
			Properties: map[string]any{
				"type_string": p.Type().String(),
				"index":       i,
				"func_id":     string(funcID),
			},
		}}); err != nil {
			return err
		}
		if err := emit(domain.Item{Fact: &domain.Fact{
			SourceID:   funcID,
			TargetID:   id,
			Kind:       domain.FactHasParam,
			ToolSource: domain.ToolSSA,
			Confidence: 1.0,
		}}); err != nil {
			return err
		}
	}

	nr := sig.Results().Len()
	for i := 0; i < nr; i++ {
		r := sig.Results().At(i)
		slot := "result"
		if nr > 1 {
			slot = fmt.Sprintf("result.%d", i)
		}
		id := domain.CanonicalID(string(funcID) + "#" + slot)
		if err := emit(domain.Item{Node: &domain.CodeEntity{
			ID:        id,
			Kind:      domain.KindResult,
			Name:      r.Type().String(),
			FilePath:  filePath,
			LineStart: pos.Line,
			LineEnd:   pos.Line,
			Properties: map[string]any{
				"type_string": r.Type().String(),
				"index":       i,
				"func_id":     string(funcID),
			},
		}}); err != nil {
			return err
		}
		if err := emit(domain.Item{Fact: &domain.Fact{
			SourceID:   funcID,
			TargetID:   id,
			Kind:       domain.FactHasResult,
			ToolSource: domain.ToolSSA,
			Confidence: 1.0,
		}}); err != nil {
			return err
		}
	}
	return nil
}
