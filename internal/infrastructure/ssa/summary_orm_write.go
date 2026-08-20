package ssa

import (
	"fmt"
	"go/constant"
	"go/types"
	"strings"

	"github.com/schaepher/codeintel/internal/domain"
	"go.uber.org/zap"
	"golang.org/x/tools/go/ssa"
)

// applyORMWrite 处理 ORM 写调用（②⑦：GORM Create/Save/Updates/Delete/
// Update 等）：
//   - 对象实参（结构体字面量/变量）：类型 → 表名（snake_case）+ 字段 →
//     列名 → 虚拟节点 表.列 + summary_io 边（字段值 → 虚拟节点）。
//     字段值不可定位（变量/调用结果/空字面量——调用点无字段级 Store）
//     时不跳过该列：仍按类型展开生成 表.列 节点，连对象值兜底
//   - 字符串列名实参（Update("col", v) 单列更新）：表名溯源链式调用
//     receiver 的 Model(&X{}) 范围对象（⑦），列名取字符串实参
func (ext *fieldExtractor) applyORMWrite(cc *ssa.CallCommon, calleeID domain.CanonicalID) error {
	logger := zap.L()
	logger.Debug("enter (fieldExtractor).applyORMWrite")
	defer logger.Debug("exit (fieldExtractor).applyORMWrite")
	if len(cc.Args) < 2 {
		return nil
	}
	arg := cc.Args[1]
	realArg := arg
	if mi, ok := arg.(*ssa.MakeInterface); ok {
		realArg = mi.X
	}
	t := derefType(realArg.Type())
	named, ok := t.(*types.Named)
	if !ok {

		if c, isConst := realArg.(*ssa.Const); isConst && c.Value != nil &&
			constant.StringVal(c.Value) != "" && len(cc.Args) >= 3 {

			table := chainTableNameValue(cc.Args[0])
			if table == "" {
				scope := chainScopeObject(cc.Args[0])
				if scope == nil {
					return nil
				}
				table = snakeCase(scope.Obj().Name())
			}
			col := constant.StringVal(c.Value)

			if cols := whereColsOf(col); len(cols) > 0 {
				col = cols[0]
			} else {
				return nil
			}

			access := "write"
			if id := string(calleeID); strings.HasSuffix(id, ".Where") ||
				strings.HasSuffix(id, ".Not") || strings.HasSuffix(id, ".Or") {
				access = "filter"
			}

			val := cc.Args[2]
			if vals := variadicElems(cc.Args[2]); len(vals) > 0 {
				val = vals[0]
			}
			return ext.emitORMColumn(cc, calleeID, table, col, val, access)
		}
		return nil
	}
	st, ok := named.Underlying().(*types.Struct)
	if !ok {
		return nil
	}

	table := ext.tableNameOf(named)
	if table == "" {
		table = snakeCase(named.Obj().Name())
	}
	line := ext.prog.Fset.PositionFor(cc.Pos(), false).Line

	objID, err := ext.emitValue(realArg)
	if err != nil {
		return err
	}
	for i := 0; i < st.NumFields(); i++ {
		field := st.Field(i)
		if !field.Exported() {
			continue
		}
		col := snakeCase(field.Name())

		fieldVal := fieldValueOf(realArg, i)
		srcID := ""
		if fieldVal != nil {
			if id, err := ext.emitValue(fieldVal); err == nil {
				srcID = string(id)
			}
		} else if objID != "" {
			srcID = string(objID)
		}
		id := domain.CanonicalID(string(ext.funcID) + "#ext.gorm." + table + "." + col + ".write@" + fmt.Sprintf("%d", line))
		if err := ext.emit(domain.Item{Node: &domain.CodeEntity{
			ID:        id,
			Kind:      domain.KindFieldAccess,
			Name:      table + "." + col,
			FilePath:  ext.currentFile,
			LineStart: line,
			Properties: map[string]any{
				"full_path":     table + "." + col,
				"instance_path": table + "." + col,
				"access_kind":   "write",
				"code_snippet":  cc.String(),
				"type_string":   "gorm",
				"is_external":   "true",
				"func_id":       string(ext.funcID),
			},
		}}); err != nil {
			return err
		}
		if srcID != "" {
			if err := ext.emitEdgeKindLine(domain.CanonicalID(srcID), id, domain.FactSummaryIO, line); err != nil {
				return err
			}
		}
	}
	return nil
}

// emitORMColumn 生成单个 表.列 虚拟节点 + summary_io 边（值实参 → 节点）。
func (ext *fieldExtractor) emitORMColumn(cc *ssa.CallCommon, calleeID domain.CanonicalID,
	table, col string, val ssa.Value, access string) error {
	realVal := val
	if mi, ok := val.(*ssa.MakeInterface); ok {
		realVal = mi.X
	}
	name := table + "." + col
	line := ext.prog.Fset.PositionFor(cc.Pos(), false).Line
	id := domain.CanonicalID(string(ext.funcID) + "#ext.gorm." + name + ".write@" + fmt.Sprintf("%d", line))
	if err := ext.emit(domain.Item{Node: &domain.CodeEntity{
		ID:        id,
		Kind:      domain.KindFieldAccess,
		Name:      name,
		FilePath:  ext.currentFile,
		LineStart: line,
		Properties: map[string]any{
			"full_path":     name,
			"instance_path": name,
			"access_kind":   access,
			"code_snippet":  cc.String(),
			"type_string":   "gorm",
			"is_external":   "true",
			"func_id":       string(ext.funcID),
		},
	}}); err != nil {
		return err
	}
	valID, err := ext.emitValue(realVal)
	if err != nil || valID == "" {
		return err
	}
	return ext.emitEdgeKindLine(valID, id, domain.FactSummaryIO, line)
}
