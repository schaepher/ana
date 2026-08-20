package ssa

import (
	"fmt"
	"go/token"

	"github.com/schaepher/codeintel/internal/domain"
	"go.uber.org/zap"
	"golang.org/x/tools/go/ssa"
)

// emitSignatureNodes 发射函数/方法签名的参数与返回节点（parameter / result）
// 及 has_param / has_result 边——签名结构展示，前端展开函数节点时可见。
// slot：参数 #param.<name>（接收者 #param.recv.<name> 防重名），
// 返回 #result（多返回 #result.<idx>）。

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
		// Q186：返回节点名 = 签名参数名（reply 等）；匿名返回 fallback 类型
		// ——信息栏"返回"分组显示"名称 · 类型"
		name := r.Name()
		if name == "" {
			name = r.Type().String()
		}
		if err := emit(domain.Item{Node: &domain.CodeEntity{
			ID:        id,
			Kind:      domain.KindResult,
			Name:      name,
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
