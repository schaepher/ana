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
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"

	"github.com/schaepher/codeintel/internal/domain"
	"golang.org/x/tools/go/ssa"
	"go.uber.org/zap"
)

// emitFunctionFields 发射单个函数内的字段访问节点与数据流边。
func emitFunctionFields(repo *domain.Repository, prog *ssa.Program, fn *ssa.Function,
	funcID domain.CanonicalID, idents map[token.Pos]string, emit domain.EmitFunc) error {
	logger := zap.L()
	logger.Debug("enter emitFunctionFields")
	defer logger.Debug("exit emitFunctionFields")
	if len(fn.Blocks) == 0 {
		return nil // 无函数体的 stub
	}
	ext := &fieldExtractor{
		repo:   repo,
		prog:   prog,
		fn:     fn,
		funcID: funcID,
		idents: idents,
		emit:   emit,
		fields: map[*ssa.FieldAddr]*fieldAccess{},
		reads:  map[*ssa.FieldAddr]*fieldAccess{},
		values: map[ssa.Value]domain.CanonicalID{},
		slots:  map[string]bool{},
	}

	// 第一遍：按使用方式判定 FieldAddr 的读写（go/ssa v0.26 表示，
	// 读经 FieldAddr+UnOp(MUL)，写经 FieldAddr+Store）
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			fa, ok := instr.(*ssa.FieldAddr)
			if !ok {
				continue
			}
			hasStore, hasDeref := faUses(fa)
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
				fa, ok := v.Addr.(*ssa.FieldAddr)
				if !ok {
					continue // 目标不是字段访问，忽略
				}
				target, ok := ext.fields[fa]
				if !ok {
					continue
				}
				// 写入值 ssa_value → 字段节点
				if err := ext.emitFlowValue(v.Val, target.id); err != nil {
					return err
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
				if v.Op != token.MUL {
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
	return nil
}

// faUses 扫描 FieldAddr 的使用方式：是否被 Store 写入、是否被 UnOp(MUL) 解引用读。
func faUses(fa *ssa.FieldAddr) (hasStore, hasDeref bool) {
	logger := zap.L()
	logger.Debug("enter faUses")
	defer logger.Debug("exit faUses")
	refs := fa.Referrers()
	if refs == nil {
		return false, false
	}
	for _, u := range *refs {
		if st, ok := u.(*ssa.Store); ok && st.Addr == fa {
			hasStore = true
		}
		if un, ok := u.(*ssa.UnOp); ok && un.Op == token.MUL && un.X == fa {
			hasDeref = true
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
	return &fieldAccess{
		id:       ext.accessID(instance, access, fa.Pos()),
		addr:     fa,
		access:   access,
		instance: instance,
		info:     info,
		ext:      ext,
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
	if named == nil {
		return fieldInfo{}, false // 匿名结构体等无稳定类型身份，跳过
	}
	field := st.Field(fieldIdx)
	fi := fieldInfo{
		fullPath:   named.Obj().Pkg().Path() + "." + named.Obj().Name() + "." + field.Name(),
		typeString: field.Type().String(),
		fieldName:  field.Name(),
	}
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
	if err != nil {
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
	if err != nil {
		return err
	}
	return ext.emitEdge(from, to)
}

// emitEdge 发射 data_flows_to 边（tool_source=ssa，conf 1.0，Q69）。
func (ext *fieldExtractor) emitEdge(from, to domain.CanonicalID) error {
	logger := zap.L()
	logger.Debug("enter (fieldExtractor).emitEdge")
	defer logger.Debug("exit (fieldExtractor).emitEdge")
	return ext.emit(domain.Item{Fact: &domain.Fact{
		SourceID:   from,
		TargetID:   to,
		Kind:       domain.FactDataFlowsTo,
		ToolSource: domain.ToolSSA,
		Confidence: 1.0,
	}})
}

// emitValue 发射（并去重）参与字段访问的 ssa_value 节点（Q73）。
// slot = SSA 名；shadowing 同名（多个 Alloc 同名）时附加 @行号 消歧。
func (ext *fieldExtractor) emitValue(v ssa.Value) (domain.CanonicalID, error) {
	logger := zap.L()
	logger.Debug("enter (fieldExtractor).emitValue")
	defer logger.Debug("exit (fieldExtractor).emitValue")
	if id, ok := ext.values[v]; ok {
		return id, nil
	}
	slot := v.Name()
	if ext.slots[slot] {
		line := ext.prog.Fset.PositionFor(v.Pos(), false).Line
		slot = fmt.Sprintf("%s@%d", slot, line)
	} else {
		ext.slots[slot] = true
	}
	id := domain.CanonicalID(string(ext.funcID) + "#" + slot)
	ext.values[v] = id
	n := &domain.CodeEntity{
		ID:   id,
		Kind: domain.KindSSAValue,
		Name: slot,
		Properties: map[string]any{
			"origin_kind": originKind(v),
			"ssa_op":      ssaOp(v),
			"type_string": v.Type().String(),
			"func_id":     string(ext.funcID),
		},
	}
	return id, ext.emit(domain.Item{Node: n})
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
		return ext.instancePathDepth(x.X, depth+1) + "." + fieldNameOf(x.X.Type(), x.Field)
	case *ssa.Field:
		return ext.instancePathDepth(x.X, depth+1) + "." + fieldNameOf(x.X.Type(), x.Field)
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
func derefStruct(t types.Type) (*types.Named, *types.Struct) {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok {
		return nil, nil
	}
	st, ok := named.Underlying().(*types.Struct)
	if !ok {
		return nil, nil
	}
	return named, st
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
	idents map[token.Pos]string // 源码标识符索引（Alloc 反查变量名）
	emit   domain.EmitFunc

	fields map[*ssa.FieldAddr]*fieldAccess // FieldAddr → write 节点（Store 解析目标）
	reads  map[*ssa.FieldAddr]*fieldAccess // FieldAddr → read 节点（UnOp 解引用）
	values map[ssa.Value]domain.CanonicalID // 已发射的 ssa_value
	slots  map[string]bool                  // 函数内 slot 名占用（shadowing 消歧）
	lines  map[string][]string              // 源码行缓存（filePath → 行数组）
}
