// 字段提取器（field_trace.md §6.1）：遍历 SSA 指令，生成 field_access 节点、
// 参与字段访问的 ssa_value 节点与 data_flows_to 边。
//
// 映射规则（注意 x/tools v0.26 的 go/ssa 表示：字段读也经 FieldAddr 取址后
// 由 UnOp(MUL) 解引用，Field 指令仅出现在非可寻址值上。故 FieldAddr 的
// 读写由"使用方式"判定，与经典 Field/FieldAddr/Store 三指令映射等价）：
//   - FieldAddr 且被 Store 使用 → field_access（write），边：基地址 → 字段节点
//   - FieldAddr 且被 UnOp(MUL) 解引用 → field_access（read），边：字段节点 → 解引用结果
//   - 两者同时（x.a = x.a + 1）→ read/write 两个独立节点
//   - Field（经典读指令，非可寻址值）→ field_access（read）
//   - Store（写入 FieldAddr）→ 不建节点，边：写入值 → 字段节点
//   - FieldAddr 无读写用途（如 &x.a 传参）→ 按文档默认为 write
//
// ssa_value 仅保留参与字段访问的值（Q73），避免全程序 SSA 节点爆炸。
package ssa

import (
	"fmt"
	"go/constant"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/schaepher/codeintel/internal/domain"
	"golang.org/x/tools/go/ssa"
	"go.uber.org/zap"
)

// emitFunctionFields 发射单个函数内的字段访问节点与数据流边。
func emitFunctionFields(repo *domain.Repository, prog *ssa.Program, fn *ssa.Function,
	funcID domain.CanonicalID, idents map[token.Pos]string, assignTargets []assignTarget,
	funcData *funcData, specs map[string]summarySpec, fallbackTotal *int, emit domain.EmitFunc) error {
	logger := zap.L()
	logger.Debug("enter emitFunctionFields")
	defer logger.Debug("exit emitFunctionFields")
	if len(fn.Blocks) == 0 {
		return nil // 无函数体的 stub
	}
	ext := &fieldExtractor{
		repo:     repo,
		prog:     prog,
		fn:       fn,
		funcID:   funcID,
		idents:        idents,
		assignTargets: assignTargets,
		emit:          emit,
		funcData:      funcData,
		specs:    specs,
		fields:   map[*ssa.FieldAddr]*fieldAccess{},
		reads:    map[*ssa.FieldAddr]*fieldAccess{},
		indexes:  map[*ssa.IndexAddr]*fieldAccess{},
		indexReads: map[*ssa.IndexAddr]*fieldAccess{},
		values:   map[ssa.Value]domain.CanonicalID{},
		funcIDs:  map[*ssa.Function]domain.CanonicalID{},
		slotsFor: map[domain.CanonicalID]map[string]bool{funcID: {}},
		extSummaries: map[domain.CanonicalID]bool{},
		rets:     map[*ssa.Function][][]ssa.Value{},
	}

	// 第一遍：按使用方式判定 FieldAddr/IndexAddr 的读写（go/ssa v0.26 表示，
	// 读经 FieldAddr+UnOp(MUL)，写经 FieldAddr+Store；slice 元素同构）
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
				// lifting 数组字面量（[]T{...} 内联 Alloc *[N]T）的 IndexAddr
				// 无源码位置（line=0）：字面量初始化不是元素访问，跳过
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
			// 嵌套字段链（m.cfg.APIKey）内层 FieldAddr 的用途从外层传播
			hasStore, hasDeref := fieldAddrUse(fa)
			if hasStore || !hasDeref {
				// 写（或仅取址传参，按文档默认 write）
				if f := ext.newFieldAccess(fa, "write"); f != nil {
					ext.fields[fa] = f
				}
			}
			if hasDeref {
				// 读
				if f := ext.newFieldAccess(fa, "read"); f != nil {
					ext.reads[fa] = f
				}
			}
		}
	}
	// 第二遍：emit 节点与边
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			switch v := instr.(type) {
			case *ssa.Lookup:
				// map/slice 元素读取（m[k] / s[i]；channel 语义不同，跳过）
				if !isSliceLike(v.X.Type()) && !isMapLike(v.X.Type()) {
					continue
				}
				if f := ext.newElementAccess(v.X, v.Index, v.Pos(), "read", ""); f != nil {
					if err := f.emit(); err != nil {
						return err
					}
					// 元素节点 → 读取结果
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
					// 容器 → 元素节点；写入值 → 元素节点
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
				// channel 发送（ch <- v）= 写元素（Q83 扩展，记号 [send]）
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
					// 字段节点 → 指令结果 ssa_value
					if err := ext.emitFlow(f.id, v); err != nil {
						return err
					}
				}
			case *ssa.Store:
				if fa, ok := v.Addr.(*ssa.FieldAddr); ok {
					target, ok := ext.fields[fa]
					if !ok {
						continue
					}
					// 写入值 ssa_value → 字段节点
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
					// 写入值 ssa_value → 元素节点
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
					// 基地址 ssa_value → 字段节点
					if err := ext.emitFlowValue(base, f.id); err != nil {
						return err
					}
				}
				if f := ext.reads[v]; f != nil {
					if err := f.emit(); err != nil {
						return err
					}
					// 基地址 ssa_value → 字段节点（读节点同样依赖基值）
					if err := ext.emitFlowValue(base, f.id); err != nil {
						return err
					}
				}
			case *ssa.UnOp:
				if v.Op == token.ARROW {
					// channel 接收（<-ch）= 读元素（[recv]）
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
					// 元素节点 → 解引用结果（s[i] 读）
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
				// 字段节点 → 解引用结果 ssa_value
				if err := ext.emitFlow(f.id, v); err != nil {
					return err
				}
			}
		}
	}
	// 第三遍：跨过程边（argument/returns/phi_operand）
	err := ext.emitCrossFlow()
	if fallbackTotal != nil {
		*fallbackTotal += ext.fallbackCount
	}
	return err
}

// faUses 扫描 FieldAddr/IndexAddr 的使用方式：是否被 Store 写入、
// 是否被 UnOp(MUL) 解引用读。
func faUses(addr ssa.Value) (hasStore, hasDeref bool) {
	logger := zap.L()
	logger.Debug("enter faUses")
	defer logger.Debug("exit faUses")
	refs := addr.Referrers()
	if refs == nil {
		return false, false
	}
	for _, u := range *refs {
		if st, ok := u.(*ssa.Store); ok && st.Addr == addr {
			hasStore = true
		}
		if un, ok := u.(*ssa.UnOp); ok && un.Op == token.MUL && un.X == addr {
			hasDeref = true
		}
	}
	return hasStore, hasDeref
}

// fieldAddrUse 判定 FieldAddr 的最终读写用途，内层 FieldAddr（仅被其他
// FieldAddr 作为取址中间层引用）的用途从外层递归传播：
//   - m.cfg.APIKey 读 → 内层 m.cfg 也是读（而非"无用途默认 write"）
//   - x.a.b = v 写 → 内层 x.a 也是写
//   - x.a = x.a + 1 → 内层同时 read+write
func fieldAddrUse(fa *ssa.FieldAddr) (hasStore, hasDeref bool) {
	refs := fa.Referrers()
	if refs == nil {
		return false, false
	}
	for _, u := range *refs {
		switch uu := u.(type) {
		case *ssa.Store:
			if uu.Addr == fa {
				hasStore = true
			}
		case *ssa.UnOp:
			if uu.Op == token.MUL && uu.X == fa {
				hasDeref = true
			}
		case *ssa.FieldAddr:
			if uu.X == fa {
				s, d := fieldAddrUse(uu)
				hasStore, hasDeref = hasStore || s, hasDeref || d
			}
		}
	}
	return hasStore, hasDeref
}

// fieldInfo 是字段访问的静态信息（类型限定路径等，与具体基值无关）。
type fieldInfo struct {
	fullPath   string
	typeString string
	fieldName  string
	filePath   string
	line       int
	snippet    string
}

// fieldAccess 是单个字段访问的构建期表示。
type fieldAccess struct {
	id       domain.CanonicalID
	addr     *ssa.FieldAddr // FieldAddr 指令（write 节点），Store 解析目标用
	access   string         // read / write
	instance string         // 变量访问链（如 req.Amount）
	info     fieldInfo
	ext      *fieldExtractor
}

// emit 输出 field_access 节点。
func (fa *fieldAccess) emit() error {
	logger := zap.L()
	logger.Debug("enter (fieldAccess).emit")
	defer logger.Debug("exit (fieldAccess).emit")
	n := &domain.CodeEntity{
		ID:        fa.id,
		Kind:      domain.KindFieldAccess,
		Name:      fa.instance, // 展示用（access_kind 在 properties）
		FilePath:  fa.info.filePath,
		LineStart: fa.info.line,
		LineEnd:   fa.info.line,
		Properties: map[string]any{
			"full_path":     fa.info.fullPath,
			"instance_path": fa.instance,
			"access_kind":   fa.access,
			"code_snippet":  fa.info.snippet,
			"type_string":   fa.info.typeString,
			"func_id":       string(fa.ext.funcID),
		},
	}
	return fa.ext.emit(domain.Item{Node: n})
}

// newFieldAccess 创建 FieldAddr 对应的字段节点（access 由使用方式判定：write/read）。
func (ext *fieldExtractor) newFieldAccess(fa *ssa.FieldAddr, access string) *fieldAccess {
	logger := zap.L()
	logger.Debug("enter (fieldExtractor).newFieldAccess")
	defer logger.Debug("exit (fieldExtractor).newFieldAccess")
	info, ok := ext.fieldInfo(fa.X.Type(), fa.Field, fa.Pos())
	if !ok {
		return nil
	}
	instance := ext.instancePath(fa.X) + "." + info.fieldName
	if info.fullPath == "" {
		// 静态类型解析失败：回退源码字面量路径（field_trace.md §6.1）
		info.fullPath = instance
		ext.fallbackCount++
	}
	ext.recordEntry(access, info, instance)
	return &fieldAccess{
		id:       ext.accessID(instance, access, fa.Pos()),
		addr:     fa,
		access:   access,
		instance: instance,
		info:     info,
		ext:      ext,
	}
}

// recordEntry 记录 direct 读/写条目（function_field_summary 预计算用）。
func (ext *fieldExtractor) recordEntry(access string, info fieldInfo, instance string) {
	if ext.funcData == nil {
		return
	}
	e := fieldEntry{
		fieldPath:    info.fullPath,
		instancePath: instance,
		line:         info.line,
		snippet:      info.snippet,
	}
	if access == "read" {
		ext.funcData.directReads = append(ext.funcData.directReads, e)
	} else {
		ext.funcData.directWrites = append(ext.funcData.directWrites, e)
	}
}

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

// fieldInfo 解析字段访问的静态信息：full_path（类型限定路径）、类型、位置。
// 静态类型不是具名结构体（匿名 struct/接口等）时返回 ok=false（Q15 限定）。
func (ext *fieldExtractor) fieldInfo(baseType types.Type, fieldIdx int, pos token.Pos) (fieldInfo, bool) {
	logger := zap.L()
	logger.Debug("enter (fieldExtractor).fieldInfo")
	defer logger.Debug("exit (fieldExtractor).fieldInfo")
	named, st := derefStruct(baseType)
	if st == nil {
		return fieldInfo{}, false // 非结构体类型
	}
	field := st.Field(fieldIdx)
	fi := fieldInfo{
		typeString: field.Type().String(),
		fieldName:  field.Name(),
	}
	if named == nil {
		// 匿名结构体等无稳定类型身份：full_path 留空，调用方回退源码字面量路径
		return fi, true
	}
	fi.fullPath = named.Obj().Pkg().Path() + "." + named.Obj().Name() + "." + field.Name()
	p := ext.prog.Fset.PositionFor(pos, false)
	fi.filePath = relPath(ext.repo.Path, p.Filename)
	fi.line = p.Line
	if fi.line > 0 {
		snippet := ext.sourceLine(fi.filePath, fi.line)
		if len(snippet) > 500 {
			snippet = snippet[:500]
		}
		fi.snippet = snippet
	}
	return fi, true
}

// sourceLine 读取仓库文件指定行的源码（去掉缩进，供 code_snippet 展示）。
// 文件内容按路径缓存，避免每个字段访问重复读盘。
func (ext *fieldExtractor) sourceLine(filePath string, line int) string {
	logger := zap.L()
	logger.Debug("enter (fieldExtractor).sourceLine")
	defer logger.Debug("exit (fieldExtractor).sourceLine")
	if ext.lines == nil {
		ext.lines = map[string][]string{}
	}
	lines, ok := ext.lines[filePath]
	if !ok {
		data, err := os.ReadFile(filepath.Join(ext.repo.Path, filepath.FromSlash(filePath)))
		if err != nil {
			ext.lines[filePath] = nil
			return ""
		}
		lines = strings.Split(string(data), "\n")
		ext.lines[filePath] = lines
	}
	if line < 1 || line > len(lines) {
		return ""
	}
	return strings.TrimSpace(lines[line-1])
}

// accessID 生成字段访问节点的 canonical ID：symbol:go:<pkg>:<func>#<instance>.<access>@<line>。
// 同一字段路径在同一函数多处访问时用行号消歧；复合读写（read/write 同位置）用
// access 消歧——各自独立节点（field_trace.md 4.1，Q68）。
func (ext *fieldExtractor) accessID(instance, access string, pos token.Pos) domain.CanonicalID {
	logger := zap.L()
	logger.Debug("enter (fieldExtractor).accessID")
	defer logger.Debug("exit (fieldExtractor).accessID")
	line := ext.prog.Fset.PositionFor(pos, false).Line
	return domain.CanonicalID(string(ext.funcID) + "#" + instance + "." + access + "@" + fmt.Sprintf("%d", line))
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
	// 嵌套字段/元素链中间层：FieldAddr/IndexAddr 第一遍已建字段访问节点——
	// 基值边直接连字段访问节点（#m.cfg → #m.cfg.APIKey），避免 ssa_value
	// 平行链导致追溯链断裂（value-trace 停在 #t0 无法穿层）
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
	// 展示名：能还原源码链的用 instancePath（局部变量 x、解引用 x、
	// 字段链 x.a）；纯临时值（Phi/Call/BinOp 结果）保持 SSA 名 tN——
	// 追溯链上"t91"应为实际变量名（Q68 展示层）
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

// funcIDOf 返回值所属函数的 canonical ID（缓存）。
// 闭包（Object 非 types.Func）或无法归属的值返回 ok=false。
func (ext *fieldExtractor) funcIDOf(v ssa.Value) (domain.CanonicalID, bool) {
	logger := zap.L()
	logger.Debug("enter (fieldExtractor).funcIDOf")
	defer logger.Debug("exit (fieldExtractor).funcIDOf")
	// 函数值（作为被调方出现的 *ssa.Function）：Parent() 为 nil，
	// 须按函数自身解析（否则会落到当前函数命名空间）
	if fn, ok := v.(*ssa.Function); ok {
		return ext.funcIDOfFn(fn)
	}
	parent := v.Parent()
	if parent == nil {
		return ext.funcID, true // 程序级值（Const 等）：归当前函数命名空间
	}
	if id, ok := ext.funcIDs[parent]; ok {
		return id, true
	}
	obj, ok := parent.Object().(*types.Func)
	if !ok || obj == nil {
		return "", false // 闭包等：无函数节点命名空间
	}
	id, _, _ := funcIdentity(obj)
	if id == "" {
		return "", false
	}
	ext.funcIDs[parent] = id
	return id, true
}

// funcIDOfFn 解析具名函数的 canonical ID（不落缓存，幂等）。
func (ext *fieldExtractor) funcIDOfFn(fn *ssa.Function) (domain.CanonicalID, bool) {
	logger := zap.L()
	logger.Debug("enter (fieldExtractor).funcIDOfFn")
	defer logger.Debug("exit (fieldExtractor).funcIDOfFn")
	obj, ok := fn.Object().(*types.Func)
	if !ok || obj == nil {
		return "", false // 闭包等
	}
	id, _, _ := funcIdentity(obj)
	if id == "" {
		return "", false
	}
	return id, true
}

// instancePath 生成变量访问链（如 req.Amount 或 a.b.c），深度上限 8 防环。
// go/ssa v0.26 的 Alloc 名为 tN，源码变量名从标识符索引反查（buildIdentIndex）。
func (ext *fieldExtractor) instancePath(v ssa.Value) string {
	return ext.instancePathDepth(v, 0)
}

func (ext *fieldExtractor) instancePathDepth(v ssa.Value, depth int) string {
	if depth > 8 {
		return v.Name()
	}
	switch x := v.(type) {
	case *ssa.FieldAddr:
		if fn := fieldNameOf(x.X.Type(), x.Field); fn != "" {
			return ext.instancePathDepth(x.X, depth+1) + "." + fn
		}
		return ext.instancePathDepth(x.X, depth+1)
	case *ssa.Field:
		if fn := fieldNameOf(x.X.Type(), x.Field); fn != "" {
			return ext.instancePathDepth(x.X, depth+1) + "." + fn
		}
		return ext.instancePathDepth(x.X, depth+1)
	case *ssa.UnOp:
		if x.Op == token.MUL { // 解引用：与 Alloc 连成变量名
			return ext.instancePathDepth(x.X, depth+1)
		}
	case *ssa.Alloc:
		if name, ok := ext.idents[x.Pos()]; ok {
			return name
		}
	}
	return v.Name()
}

// fieldNameOf 取类型第 idx 个字段名（derefStruct 失败返回空）。
func fieldNameOf(t types.Type, idx int) string {
	_, st := derefStruct(t)
	if st == nil {
		return ""
	}
	return st.Field(idx).Name()
}

// derefStruct 解指针/取底层结构体；返回具名类型（可空）与结构体（可空）。
// 匿名 struct 类型：named 为 nil、st 可用（full_path 回退场景，§6.1）。
func derefStruct(t types.Type) (*types.Named, *types.Struct) {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	if named, ok := t.(*types.Named); ok {
		if st, ok2 := named.Underlying().(*types.Struct); ok2 {
			return named, st
		}
		return nil, nil
	}
	if st, ok := t.(*types.Struct); ok {
		return nil, st // 匿名 struct：无类型身份，字段列表可用
	}
	return nil, nil
}

// originKind 区分 SSA 值来源（field_trace.md §4.1）。
func originKind(v ssa.Value) string {
	switch x := v.(type) {
	case *ssa.Parameter:
		parent := x.Parent()
		if parent != nil && parent.Signature.Recv() != nil {
			if params := parent.Params; len(params) > 0 && params[0] == x {
				return "receiver"
			}
		}
		return "param"
	case *ssa.Alloc:
		if x.Heap {
			return "alloc"
		}
		return "local"
	case *ssa.FreeVar:
		return "local"
	case *ssa.Global:
		return "global"
	case *ssa.Const:
		return "literal"
	case *ssa.Phi:
		return "phi"
	case *ssa.Call:
		return "call_result"
	}
	return "local"
}

// ssaOp 返回 SSA 指令类型名（如 field / fieldaddr / store）。
func ssaOp(v ssa.Value) string {
	return strings.TrimPrefix(fmt.Sprintf("%T", v), "*ssa.")
}

// fieldExtractor 是单个函数的字段提取状态。
type fieldExtractor struct {
	repo   *domain.Repository
	prog   *ssa.Program
	fn     *ssa.Function
	funcID domain.CanonicalID
	idents        map[token.Pos]string // 源码标识符索引（Alloc 反查变量名）
	assignTargets []assignTarget       // 赋值表达式区间（按 start 排序）→ 目标变量名（MakeMap/MakeSlice 恢复）
	emit          domain.EmitFunc

	fields   map[*ssa.FieldAddr]*fieldAccess // FieldAddr → write 节点（Store 解析目标）
	reads    map[*ssa.FieldAddr]*fieldAccess // FieldAddr → read 节点（UnOp 解引用）
	indexes  map[*ssa.IndexAddr]*fieldAccess // IndexAddr → write 节点（slice 元素）
	indexReads map[*ssa.IndexAddr]*fieldAccess // IndexAddr → read 节点
	values   map[ssa.Value]domain.CanonicalID // 已发射的 ssa_value
	funcIDs  map[*ssa.Function]domain.CanonicalID // 函数 → canonical ID 缓存
	slotsFor map[domain.CanonicalID]map[string]bool // 每函数 slot 占用（shadowing 消歧）
	rets     map[*ssa.Function][][]ssa.Value // 被调函数 Return 指令缓存（returns 边复用）
	lines    map[string][]string             // 源码行缓存（filePath → 行数组）
	funcData *funcData                       // 摘要收集（direct 读写 + 静态调用）
	specs    map[string]summarySpec          // 外部函数摘要（内置 + 用户）
	extSummaries map[domain.CanonicalID]bool // 已创建 external_summary 节点
	currentFile string                       // 当前函数文件（虚拟节点用）
	fallbackCount int                        // 静态类型解析失败回退数（警告汇总）
}

// isSSAName 判断是否为 SSA 临时名（t0、t91 等），用于决定展示名回退。
func isSSAName(name string) bool {
	if len(name) < 2 || name[0] != 't' {
		return false
	}
	for _, c := range name[1:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// newElementAccess 创建容器元素访问节点（map/slice/array 元素，Q83）。
// key 为 nil 表示 Range 迭代（[*]）。
func (ext *fieldExtractor) newElementAccess(container, key ssa.Value, pos token.Pos, access, mark string) *fieldAccess {
	logger := zap.L()
	logger.Debug("enter (fieldExtractor).newElementAccess")
	defer logger.Debug("exit (fieldExtractor).newElementAccess")
	if mark == "" {
		mark = elementMark(key)
	}
	full, instance := ext.elementPathMark(container, mark)
	line := ext.prog.Fset.PositionFor(pos, false).Line
	fi := fieldInfo{
		fullPath:   full,
		fieldName:  "",
		typeString: container.Type().String(),
		filePath:   ext.currentFile,
		line:       line,
		snippet:    ext.sourceLine(ext.currentFile, line),
	}
	ext.recordEntry(access, fi, instance)
	return &fieldAccess{
		id:       ext.accessID(instance, access, pos),
		access:   access,
		instance: instance,
		info:     fi,
		ext:      ext,
	}
}

// elementPath 生成元素访问的 full_path / instance_path（Q1/Q5）：
//   常量字符串 key → m["a"]；常量 int 索引 → s[0]；变量 key → [key]；Range → [*]
//   full_path 基：字段路径（容器是结构体字段）> named 容器类型 > 回退 instance
func (ext *fieldExtractor) elementPath(container, key ssa.Value) (full, instance string) {
	return ext.elementPathMark(container, elementMark(key))
}

func (ext *fieldExtractor) elementPathMark(container ssa.Value, mark string) (full, instance string) {
	instance = ext.containerInstance(container) + mark
	full = ext.containerFullPath(container)
	if full == "" {
		full = instance // 回退（Q5，instance 已含 mark）
	} else {
		full += mark
	}
	return full, instance
}

// containerInstance 容器实例路径（lifting 后 MakeMap/MakeSlice 寄存器
// 从赋值目标恢复变量名）。
func (ext *fieldExtractor) containerInstance(container ssa.Value) string {
	if un, ok := container.(*ssa.UnOp); ok && un.Op == token.MUL {
		container = un.X
	}
	// 容器创建/包装指令（MakeMap/MakeSlice/Slice 等，lifting 后寄存器无源码名）：
	// 从赋值目标区间恢复容器名
	if name := ext.lookupAssignTarget(container.Pos()); name != "" {
		return name
	}
	return ext.instancePath(container)
}

// containerFullPath 容器类型限定路径（字段路径 > named 容器类型 > 空回退）。
func (ext *fieldExtractor) containerFullPath(container ssa.Value) string {
	if un, ok := container.(*ssa.UnOp); ok && un.Op == token.MUL {
		container = un.X
	}
	if fa, ok := container.(*ssa.FieldAddr); ok {
		if info, ok2 := ext.fieldInfo(fa.X.Type(), fa.Field, fa.Pos()); ok2 {
			return info.fullPath // pkg.T.m（容器是字段）
		}
	}
	if named := namedContainerOf(container.Type()); named != "" {
		return named // pkg.M（named map/slice 类型）
	}
	return ""
}

// elementMark 生成元素记号（Q5）："a" / 0 / [key] / [*]。
func elementMark(key ssa.Value) string {
	if key == nil {
		return "[*]" // Range 迭代（全部元素）
	}
	if c, ok := key.(*ssa.Const); ok && c.Value != nil {
		switch c.Value.Kind() {
		case constant.String:
			return `["` + constant.StringVal(c.Value) + `"]`
		case constant.Int:
			return "[" + c.Value.ExactString() + "]"
		}
	}
	return "[key]" // 变量 key：回退容器级
}

// namedContainerOf 取 named map/slice/array 类型的限定路径（pkg.M）；非 named 返回空。
func namedContainerOf(t types.Type) string {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok {
		return ""
	}
	switch named.Underlying().(type) {
	case *types.Map, *types.Slice, *types.Array:
		obj := named.Obj()
		if obj == nil || obj.Pkg() == nil {
			return ""
		}
		return obj.Pkg().Path() + "." + obj.Name()
	}
	return ""
}

// isMapLike / isSliceLike / isChanLike 容器类型判定（含 named 与字面类型）。
func isMapLike(t types.Type) bool {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	if named, ok := t.(*types.Named); ok {
		t = named.Underlying()
	}
	_, ok := t.(*types.Map)
	return ok
}

func isSliceLike(t types.Type) bool {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	if named, ok := t.(*types.Named); ok {
		t = named.Underlying()
	}
	switch t.(type) {
	case *types.Slice, *types.Array:
		return true
	}
	return false
}

func isChanLike(t types.Type) bool {
	if named, ok := t.(*types.Named); ok {
		t = named.Underlying()
	}
	_, ok := t.(*types.Chan)
	return ok
}

// lookupAssignTarget 区间匹配赋值目标（MakeMap.Pos 落在字面量内部）。
// 切片按 start 排序：二分找最后一个 start <= pos 的区间，检查 end 覆盖。
// 嵌套赋值（f(x := 1)）内层 start 更大，二分自然命中内层区间。
func (ext *fieldExtractor) lookupAssignTarget(pos token.Pos) string {
	i := sort.Search(len(ext.assignTargets), func(i int) bool {
		return ext.assignTargets[i].start > pos
	}) - 1
	if i < 0 || pos > ext.assignTargets[i].end {
		return ""
	}
	return ext.assignTargets[i].name
}
