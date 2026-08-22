package ssa

import (
	"fmt"
	"go/token"
	"strings"

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

// valueIDByInstance instancePath 名字的节点 ID（Q235-7）：类型短名
// （含 * / [] / . 特征——匿名分配回退）同函数内同名附加 @行号消歧
// （phi 两分支同类型匿名分配等，防 Q155 去重误合并）；源码变量名
// （arr）保持合并（shadowing 语义，与 Q205 一致）。
func (ext *fieldExtractor) valueIDByInstance(funcID domain.CanonicalID, v ssa.Value, name string) (domain.CanonicalID, string) {
	display := name
	if strings.ContainsAny(name, "*[].") {
		slots := ext.slotsFor[funcID]
		if slots == nil {
			slots = map[string]bool{}
			ext.slotsFor[funcID] = slots
		}
		if slots[name] {
			line := ext.prog.Fset.PositionFor(v.Pos(), false).Line
			display = fmt.Sprintf("%s@%d", name, line)
		} else {
			slots[name] = true
		}
	}
	return domain.CanonicalID(string(funcID) + "#" + display), display
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
		var paramName string
		if recv := fn.Signature.Recv(); recv != nil && len(fn.Params) > 0 && fn.Params[0] == p {
			paramName = recv.Name()
			if paramName == "" {
				paramName = "recv"
			}
			id = domain.CanonicalID(string(funcID) + "#param.recv." + paramName)
		} else {
			paramName = p.Object().Name()
			if paramName == "" {
				idx := 0
				for i := 0; i < fn.Signature.Params().Len(); i++ {
					if fn.Signature.Params().At(i) == p.Object() {
						idx = i
						break
					}
				}
				paramName = fmt.Sprintf("arg%d", idx)
			}
			id = domain.CanonicalID(string(funcID) + "#param." + paramName)
		}
		ext.values[v] = id
		// Q223：闭包参数（FuncLit 形参）无签名节点（emitSignatureNodes 只对
		// 顶层函数发射，闭包归外层函数处理）——返回未落库 ID 会使
		// summary_io/argument 等边端点缺失（Q222 同款漏报：read→对象边 FK
		// 失败、filter 值链断）。此处自行发射 ssa_value 节点（ID 与签名节点
		// 规则一致；外层函数恰好有同名参数时共享签名节点，与 shadowing 合并
		// 语义一致）。
		if !ext.sigEmitted {
			n := &domain.CodeEntity{
				ID:        id,
				Kind:      domain.KindSSAValue,
				Name:      paramName,
				LineStart: lineOf(ext, v),
				Properties: map[string]any{
					"origin_kind": "param",
					"ssa_op":      "parameter",
					"type_string": p.Type().String(),
					"func_id":     string(funcID),
				},
			}
			return id, ext.emit(domain.Item{Node: n})
		}
		return id, nil
	}
	if g, ok := v.(*ssa.Global); ok && g.Pkg != nil && g.Pkg.Pkg != nil {
		id := domain.CanonicalID("symbol:go:" + g.Pkg.Pkg.Path() + ":var." + g.Name())
		ext.values[v] = id
		n := &domain.CodeEntity{
			ID:        id,
			Kind:      domain.KindSSAValue,
			Name:      g.Name(),
			LineStart: lineOf(ext, v),
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
		if isAlloc {
			// Q205 双发射修复：*alloc（读整个 slice/对象变量）与 Alloc
			// 是同一逻辑值——统一用变量名 ID（applyScanOut/load 分支的
			// instancePath 规则），Alloc 直接 emit 时也走变量名（见通用
			// 分支的 Alloc 特判），读边/返回边/Scan 边同节点（go2o
			// SelectAttr 的 `return list` 连 #list（load）而读边连 #t0
			// （Alloc），链断）
			if id, ok := ext.values[uo.X]; ok {
				ext.values[v] = id
				return id, nil
			}
			if fid, ok := ext.funcIDOf(uo.X); ok {
				if name := ext.instancePath(uo); !isSSAName(name) {
					id, _ := ext.valueIDByInstance(fid, uo, name)
					ext.values[uo.X] = id
					ext.values[v] = id
					// Q221：不能提前 return——节点发射在下方统一分支。
					// 此前提前 return：变量被 range 解引用（*orders）时
					// 只设缓存不发射节点，后续 ORM 读分支（Find(&orders)
					// 的 emitValue(Alloc)）命中缓存返回未落库的 ID →
					// read → 对象边 FK 失败 → 真实键关联漏报
				}
			}
		}
		if isAlloc || isIdx || isFld {
			if name := ext.instancePath(uo); !isSSAName(name) {
				fid, ok2 := ext.funcIDOf(uo)
				if ok2 {
					id, display := ext.valueIDByInstance(fid, uo, name)
					ext.values[v] = id
					n := &domain.CodeEntity{
						ID:        id,
						Kind:      domain.KindSSAValue,
						Name:      display,
						LineStart: lineOf(ext, v),
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
	// Q205 双发射修复：Alloc（变量地址）直接 emit 时用变量名 ID——与
	// applyScanOut（#x）/load 分支（*alloc → #x）统一（go2o SelectAttr
	// 的 &list 读边连 #list 而 `return list` 连 #t0，链断；统一变量名后
	// 同节点）。shadowing 同名变量会合并（与 load/scan 既有规则一致）
	if _, isAlloc := v.(*ssa.Alloc); isAlloc {
		if name := ext.instancePath(v); !isSSAName(name) {
			id, display := ext.valueIDByInstance(funcID, v, name)
			ext.values[v] = id
			n := &domain.CodeEntity{
				ID:        id,
				Kind:      domain.KindSSAValue,
				Name:      display,
				LineStart: lineOf(ext, v),
				Properties: map[string]any{
					"origin_kind": "local",
					"ssa_op":      "alloc",
					"type_string": v.Type().String(),
					"func_id":     string(funcID),
				},
			}
			return id, ext.emit(domain.Item{Node: n})
		}
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

	// Q179：instancePath 已对叶子临时寄存器做 recoverVarName（tN → 变量名），
	// 仍为 SSA 临时名（phi/合成值无 Pos 匹配失败）时回退 slot
	name := ext.instancePath(v)
	if isSSAName(name) {
		// Q235-6/8：SSA 临时名回退类型短名（保留 * / [] 与末段包名：
		// *proto.String / []*tndemo.Brand / orm.Orm）——用户视角 tN
		// 不可读，链上显示类型可把前后值联系起来（GetBrands 内部的
		// 切片构造 ↔ 调用方 brands）。Alloc（Q235-6 匿名分配）扩展到
		// 全部指令（Call/MakeSlice/Convert 等）；**phi 保留**（分支
		// 汇合语义，无源码对应，恢复需数据流分析）
		if _, isPhi := v.(*ssa.Phi); !isPhi {
			if tn := allocTypeShort(v.Type().String()); tn != "" {
				name = tn
			} else {
				name = slot
			}
		} else {
			name = slot
		}
	}
	n := &domain.CodeEntity{
		ID:        id,
		Kind:      domain.KindSSAValue,
		Name:      name,
		LineStart: lineOf(ext, v),
		Properties: map[string]any{
			"origin_kind": originKind(v),
			"ssa_op":      ssaOp(v),
			"type_string": v.Type().String(),
			"func_id":     string(funcID),
		},
	}
	return id, ext.emit(domain.Item{Node: n})
}
