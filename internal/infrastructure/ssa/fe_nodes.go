package ssa

import (
	"fmt"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"

	"github.com/schaepher/codeintel/internal/domain"
	"go.uber.org/zap"
	"golang.org/x/tools/go/ssa"
)

// fieldInfo 是字段访问的静态信息（类型限定路径等，与具体基值无关）。
type fieldInfo struct {
	fullPath   string
	typeString string
	fieldName  string
	filePath   string
	line       int
	snippet    string
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

		info.fullPath = instance
		ext.fallbackCount++
	}
	ext.recordEntry(access, info, instance)
	return &fieldAccess{
		id:       ext.accessID(instance, access, fa.Pos()),
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

// fieldInfo 解析字段访问的静态信息：full_path（类型限定路径）、类型、位置。
// 静态类型不是具名结构体（匿名 struct/接口等）时返回 ok=false（Q15 限定）。
func (ext *fieldExtractor) fieldInfo(baseType types.Type, fieldIdx int, pos token.Pos) (fieldInfo, bool) {
	logger := zap.L()
	logger.Debug("enter (fieldExtractor).fieldInfo")
	defer logger.Debug("exit (fieldExtractor).fieldInfo")
	named, st := derefStruct(baseType)
	if st == nil {
		return fieldInfo{}, false
	}
	field := st.Field(fieldIdx)
	fi := fieldInfo{
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
	if named == nil {

		return fi, true
	}
	fi.fullPath = named.Obj().Pkg().Path() + "." + named.Obj().Name() + "." + field.Name()
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

// funcIDOf 返回值所属函数的 canonical ID（缓存）。
// 闭包（Object 非 types.Func）或无法归属的值返回 ok=false。
func (ext *fieldExtractor) funcIDOf(v ssa.Value) (domain.CanonicalID, bool) {
	logger := zap.L()
	logger.Debug("enter (fieldExtractor).funcIDOf")
	defer logger.Debug("exit (fieldExtractor).funcIDOf")

	if fn, ok := v.(*ssa.Function); ok {
		return ext.funcIDOfFn(fn)
	}
	parent := v.Parent()

	for parent != nil && parent.Object() == nil {
		parent = parent.Parent()
	}
	if parent == nil {
		return ext.funcID, true
	}
	if id, ok := ext.funcIDs[parent]; ok {
		return id, true
	}
	obj, ok := parent.Object().(*types.Func)
	if !ok || obj == nil {
		return "", false
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
		return "", false
	}
	id, _, _ := funcIdentity(obj)
	if id == "" {
		return "", false
	}
	return id, true
}

// lineOf 值的源码行号（无 Pos / 0 行返回 0——合成值 phi、Const 等）。
// Q231：从 fe_value.go 移入（文件行数收敛）。
func lineOf(ext *fieldExtractor, v ssa.Value) int {
	line := ext.prog.Fset.PositionFor(v.Pos(), false).Line
	if line < 0 {
		return 0
	}
	return line
}

// emitElementOp 容器元素类指令发射（Q231 拆分自 emitFunctionFields）：
// Lookup/Index（读）、MapUpdate/Send（写）、Range（迭代读）——生成
// 元素访问节点 + 值流边。非容器类型直接返回（原 case 的 continue）。
func (ext *fieldExtractor) emitElementOp(v ssa.Instruction) error {
	logger := zap.L()
	logger.Debug("enter (fieldExtractor).emitElementOp")
	defer logger.Debug("exit (fieldExtractor).emitElementOp")
	switch ins := v.(type) {
	case *ssa.Lookup:
		if !isSliceLike(ins.X.Type()) && !isMapLike(ins.X.Type()) {
			return nil
		}
		if f := ext.newElementAccess(ins.X, ins.Index, ins.Pos(), "read", ""); f != nil {
			if err := f.emit(); err != nil {
				return err
			}
			return ext.emitFlow(f.id, ins)
		}
	case *ssa.Index:
		if !isSliceLike(ins.X.Type()) {
			return nil
		}
		if f := ext.newElementAccess(ins.X, ins.Index, ins.Pos(), "read", ""); f != nil {
			if err := f.emit(); err != nil {
				return err
			}
			return ext.emitFlow(f.id, ins)
		}
	case *ssa.MapUpdate:
		if !isMapLike(ins.Map.Type()) {
			return nil
		}
		if f := ext.newElementAccess(ins.Map, ins.Key, ins.Pos(), "write", ""); f != nil {
			if err := f.emit(); err != nil {
				return err
			}
			if err := ext.emitFlowValue(ins.Map, f.id); err != nil {
				return err
			}
			return ext.emitFlowValue(ins.Value, f.id)
		}
	case *ssa.Send:
		if !isChanLike(ins.Chan.Type()) {
			return nil
		}
		if f := ext.newElementAccess(ins.Chan, nil, ins.Pos(), "write", "[send]"); f != nil {
			if err := f.emit(); err != nil {
				return err
			}
			if err := ext.emitFlowValue(ins.Chan, f.id); err != nil {
				return err
			}
			return ext.emitFlowValue(ins.X, f.id)
		}
	case *ssa.Range:
		if f := ext.newElementAccess(ins.X, nil, ins.Pos(), "read", ""); f != nil {
			if err := f.emit(); err != nil {
				return err
			}
			return ext.emitFlowValue(ins.X, f.id)
		}
	}
	return nil
}
