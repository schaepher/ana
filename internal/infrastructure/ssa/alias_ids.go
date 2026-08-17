package ssa

import (
	"fmt"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"

	"github.com/schaepher/codeintel/internal/domain"
	"golang.org/x/tools/go/ssa"
)

func (p *aliasPass) valueNodeID(v ssa.Value) (domain.CanonicalID, bool) {
	// Q178：参数/接收者统一用签名参数节点（与 emitValue 一致）——此前
	// alias 对参数生成 ssa_value 副本（funcID#m），与签名参数节点
	// （#param.recv.m）双节点分裂，数据边挂两处。参数节点已由
	// emitSignatureNodes 发射，这里只返回 ID 不再发射。
	if prm, ok := v.(*ssa.Parameter); ok {
		fn := prm.Parent()
		if fn == nil || fn.Signature == nil {
			return "", false
		}
		funcID, ok := p.funcIDOf(fn)
		if !ok {
			return "", false
		}
		if recv := fn.Signature.Recv(); recv != nil && len(fn.Params) > 0 && fn.Params[0] == prm {
			name := recv.Name()
			if name == "" {
				name = "recv"
			}
			return domain.CanonicalID(string(funcID) + "#param.recv." + name), true
		}
		name := prm.Object().Name()
		if name == "" {
			idx := 0
			for i := 0; i < fn.Signature.Params().Len(); i++ {
				if fn.Signature.Params().At(i) == prm.Object() {
					idx = i
					break
				}
			}
			name = fmt.Sprintf("arg%d", idx)
		}
		return domain.CanonicalID(string(funcID) + "#param." + name), true
	}
	fn := v.Parent()
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
	slot := v.Name()
	if slots[slot] {
		line := p.prog.Fset.PositionFor(v.Pos(), false).Line
		slot = fmt.Sprintf("%s@%d", slot, line)
	} else {
		slots[slot] = true
	}
	id := domain.CanonicalID(string(funcID) + "#" + slot)
	if err := p.emit(domain.Item{Node: &domain.CodeEntity{
		ID:   id,
		Kind: domain.KindSSAValue,
		Name: slot,
		Properties: map[string]any{
			"origin_kind": originKind(v),
			"ssa_op":      ssaOp(v),
			"type_string": v.Type().String(),
			"func_id":     string(funcID),
		},
	}}); err != nil {
		return "", false
	}
	return id, true
}

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
		fi.snippet = p.sourceLine(fi.filePath, fi.line)
	}
	return fi, true
}

func (p *aliasPass) sourceLine(filePath string, line int) string {
	if line <= 0 || filePath == "" {
		return ""
	}
	lines, ok := p.lines[filePath]
	if !ok {
		data, err := os.ReadFile(filepath.Join(p.repo.Path, filepath.FromSlash(filePath)))
		if err != nil {
			p.lines[filePath] = nil
			return ""
		}
		lines = strings.Split(string(data), "\n")
		p.lines[filePath] = lines
	}
	if line > len(lines) {
		return ""
	}
	return strings.TrimSpace(lines[line-1])
}

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
		return "", false
	}
	return full + elementMark(key), true
}
