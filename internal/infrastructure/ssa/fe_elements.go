package ssa

import (
	"go/constant"
	"go/token"
	"go/types"

	"go.uber.org/zap"
	"golang.org/x/tools/go/ssa"
)

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
//
//	常量字符串 key → m["a"]；常量 int 索引 → s[0]；变量 key → [key]；Range → [*]
//	full_path 基：字段路径（容器是结构体字段）> named 容器类型 > 回退 instance
func (ext *fieldExtractor) elementPath(container, key ssa.Value) (full, instance string) {
	return ext.elementPathMark(container, elementMark(key))
}
func (ext *fieldExtractor) elementPathMark(container ssa.Value, mark string) (full, instance string) {
	instance = ext.containerInstance(container) + mark
	full = ext.containerFullPath(container)
	if full == "" {
		full = instance
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
			return info.fullPath
		}
	}
	if named := namedContainerOf(container.Type()); named != "" {
		return named
	}
	return ""
}

// elementMark 生成元素记号（Q5）："a" / 0 / [key] / [*]。
func elementMark(key ssa.Value) string {
	if key == nil {
		return "[*]"
	}
	if c, ok := key.(*ssa.Const); ok && c.Value != nil {
		switch c.Value.Kind() {
		case constant.String:
			return `["` + constant.StringVal(c.Value) + `"]`
		case constant.Int:
			return "[" + c.Value.ExactString() + "]"
		}
	}
	return "[key]"
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
