package ssa

import (
	"fmt"
	"go/token"

	"github.com/schaepher/codeintel/internal/domain"
	"go.uber.org/zap"
	"golang.org/x/tools/go/ssa"
)

// newFieldAccessValue 创建 Field 读对应的字段节点。
func (ext *fieldExtractor) newFieldAccessValue(f *ssa.Field) *fieldAccess {
	logger := zap.L()
	logger.Debug("enter (fieldExtractor).newFieldAccessValue")
	defer logger.Debug("exit (fieldExtractor).newFieldAccessValue")
	info, ok := ext.fieldInfo(f.X.Type(), f.Field, f.Pos())
	if !ok {
		return nil
	}
	instance := ext.instancePath(f.X) + "." + info.fieldName
	if info.fullPath == "" {
		info.fullPath = instance
		ext.fallbackCount++
	}
	ext.recordEntry("read", info, instance)
	return &fieldAccess{
		id:       ext.accessID(instance, "read", f.Pos()),
		access:   "read",
		instance: instance,
		info:     info,
		ext:      ext,
	}
}

// emitFlow 发射 字段节点 → ssa_value 的 data_flows_to 边（Field 读）。
func (ext *fieldExtractor) emitFlow(from domain.CanonicalID, v ssa.Value) error {
	logger := zap.L()
	logger.Debug("enter (fieldExtractor).emitFlow")
	defer logger.Debug("exit (fieldExtractor).emitFlow")
	to, err := ext.emitValue(v)
	if err != nil || to == "" {
		return err
	}
	return ext.emitEdge(from, to)
}

// emitFlowValue 发射 ssa_value → 字段节点 的 data_flows_to 边（FieldAddr 基地址 / Store 写入值）。
func (ext *fieldExtractor) emitFlowValue(v ssa.Value, to domain.CanonicalID) error {
	logger := zap.L()
	logger.Debug("enter (fieldExtractor).emitFlowValue")
	defer logger.Debug("exit (fieldExtractor).emitFlowValue")
	from, err := ext.emitValue(v)
	if err != nil || from == "" {
		return err
	}
	return ext.emitEdge(from, to)
}

// emitEdge 发射 data_flows_to 边（tool_source=ssa，conf 1.0，Q69）。
func (ext *fieldExtractor) emitEdge(from, to domain.CanonicalID) error {
	logger := zap.L()
	logger.Debug("enter (fieldExtractor).emitEdge")
	defer logger.Debug("exit (fieldExtractor).emitEdge")
	return ext.emitEdgeKind(from, to, domain.FactDataFlowsTo)
}

// emitValue 发射（并去重）参与字段访问或跨过程数据流的 ssa_value 节点（Q73）。
// 节点命名空间按值所属函数（funcIDOf）：跨函数（实参/形参/返回值）落在各自
// 函数的 canonical ID 下。slot = SSA 名；同名冲突（shadowing）附加 @行号 消歧。
// 值不属于可标识函数（闭包等）时返回空 ID，调用方跳过相关边。
func (ext *fieldExtractor) emitValue(v ssa.Value) (domain.CanonicalID, error) {
	logger := zap.L()
	logger.Debug("enter (fieldExtractor).emitValue")
	defer logger.Debug("exit (fieldExtractor).emitValue")
	if id, ok := ext.values[v]; ok {
		return id, nil
	}

	if ex, ok := v.(*ssa.Extract); ok {
		return ext.emitValue(ex.Tuple)
	}
	// Q178：参数直接返回签名参数节点（funcID#param.<name> / #param.recv.<name>，
	// 与 emitSignatureNodes 的 ID 规则一致，节点已发射故不再 emit）——避免
	// summary_io/argument 等边挂在 ssa_value 临时节点（#orderID）上：临时节点
	// 与参数节点无连接，value-trace 无法从 filter 经 argument 边回连调用点实参。
	if p, ok := v.(*ssa.Parameter); ok {
		funcID, ok := ext.funcIDOf(p)
		if !ok {
			return "", nil
		}
		fn := p.Parent()
		if fn == nil || fn.Signature == nil {
			return "", nil
		}
		var id domain.CanonicalID
		if recv := fn.Signature.Recv(); recv != nil && len(fn.Params) > 0 && fn.Params[0] == p {
			name := recv.Name()
			if name == "" {
				name = "recv"
			}
			id = domain.CanonicalID(string(funcID) + "#param.recv." + name)
		} else {
			name := p.Object().Name()
			if name == "" {
				idx := 0
				for i := 0; i < fn.Signature.Params().Len(); i++ {
					if fn.Signature.Params().At(i) == p.Object() {
						idx = i
						break
					}
				}
				name = fmt.Sprintf("arg%d", idx)
			}
			id = domain.CanonicalID(string(funcID) + "#param." + name)
		}
		ext.values[v] = id
		return id, nil
	}
	if g, ok := v.(*ssa.Global); ok && g.Pkg != nil && g.Pkg.Pkg != nil {
		id := domain.CanonicalID("symbol:go:" + g.Pkg.Pkg.Path() + ":var." + g.Name())
		ext.values[v] = id
		n := &domain.CodeEntity{
			ID:   id,
			Kind: domain.KindSSAValue,
			Name: g.Name(),
			Properties: map[string]any{
				"origin_kind": "global",
				"ssa_op":      "global",
				"type_string": g.Type().String(),
			},
		}
		return id, ext.emit(domain.Item{Node: n})
	}

	if fa, ok := v.(*ssa.FieldAddr); ok {
		if f := ext.fields[fa]; f != nil {
			ext.values[v] = f.id
			return f.id, nil
		}
		if f := ext.reads[fa]; f != nil {
			ext.values[v] = f.id
			return f.id, nil
		}
	}
	if ia, ok := v.(*ssa.IndexAddr); ok {
		if f := ext.indexes[ia]; f != nil {
			ext.values[v] = f.id
			return f.id, nil
		}
		if f := ext.indexReads[ia]; f != nil {
			ext.values[v] = f.id
			return f.id, nil
		}
	}

	if uo, ok := v.(*ssa.UnOp); ok && uo.Op == token.MUL {

		_, isAlloc := uo.X.(*ssa.Alloc)
		_, isIdx := uo.X.(*ssa.IndexAddr)
		_, isFld := uo.X.(*ssa.FieldAddr)
		if isAlloc || isIdx || isFld {
			if name := ext.instancePath(uo); !isSSAName(name) {
				fid, ok2 := ext.funcIDOf(uo)
				if ok2 {
					id := domain.CanonicalID(string(fid) + "#" + name)
					ext.values[v] = id
					n := &domain.CodeEntity{
						ID:   id,
						Kind: domain.KindSSAValue,
						Name: name,
						Properties: map[string]any{
							"origin_kind": "local",
							"ssa_op":      "load",
							"type_string": v.Type().String(),
							"func_id":     string(fid),
						},
					}
					return id, ext.emit(domain.Item{Node: n})
				}
			}
		}
	}
	funcID, ok := ext.funcIDOf(v)
	if !ok {
		return "", nil
	}
	slots := ext.slotsFor[funcID]
	if slots == nil {
		slots = map[string]bool{}
		ext.slotsFor[funcID] = slots
	}
	slot := v.Name()
	if slots[slot] {
		line := ext.prog.Fset.PositionFor(v.Pos(), false).Line
		slot = fmt.Sprintf("%s@%d", slot, line)
	} else {
		slots[slot] = true
	}
	id := domain.CanonicalID(string(funcID) + "#" + slot)
	ext.values[v] = id

	name := ext.instancePath(v)
	if isSSAName(name) {
		name = slot
	}
	n := &domain.CodeEntity{
		ID:   id,
		Kind: domain.KindSSAValue,
		Name: name,
		Properties: map[string]any{
			"origin_kind": originKind(v),
			"ssa_op":      ssaOp(v),
			"type_string": v.Type().String(),
			"func_id":     string(funcID),
		},
	}
	return id, ext.emit(domain.Item{Node: n})
}
