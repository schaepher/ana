package ssa

import (
	"fmt"
	"go/types"

	"github.com/schaepher/codeintel/internal/domain"
	"golang.org/x/tools/go/ssa"
)

// emitEntityFields 为实体类型展开 表.列 虚拟节点（write/read）+ summary_io 边。
// valID：write=对象值（边 值→节点）；read=调用点返回值（边 节点→值）。
func (ext *fieldExtractor) emitEntityFields(entity types.Type, table, access string,
	line int, valID domain.CanonicalID, cc *ssa.CallCommon, vtype string) error {
	if vtype == "" {
		vtype = "gorm"
	}
	if p, ok := entity.(*types.Pointer); ok {
		entity = p.Elem()
	}
	named, ok := entity.(*types.Named)
	if !ok {
		return nil
	}
	st, ok := named.Underlying().(*types.Struct)
	if !ok {
		return nil
	}
	for i := 0; i < st.NumFields(); i++ {
		field := st.Field(i)
		if !field.Exported() {
			continue
		}
		col := snakeCase(field.Name())
		id := domain.CanonicalID(string(ext.funcID) + "#ext.gorm." + table + "." + col + "." + access + "@" + fmt.Sprintf("%d", line))
		if err := ext.emit(domain.Item{Node: &domain.CodeEntity{
			ID:        id,
			Kind:      domain.KindFieldAccess,
			Name:      table + "." + col,
			FilePath:  ext.currentFile,
			LineStart: line,
			Properties: map[string]any{
				"full_path":     table + "." + col,
				"instance_path": table + "." + col,
				"access_kind":   access,
				"code_snippet":  cc.String(),
				"type_string":   vtype,
				"is_external":   "true",
				"func_id":       string(ext.funcID),
			},
		}}); err != nil {
			return err
		}
		if valID != "" {
			if access == "write" {

				if err := ext.emitEdgeKindLine(valID, id, domain.FactSummaryIO, line); err != nil {
					return err
				}
			} else {

				if err := ext.emitEdgeKindLine(id, valID, domain.FactSummaryIO, line); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// emitWhereFilter 为 where 列发射 filter 虚拟节点 + 值实参 → 节点边。
// whereArg 是 where 字符串实参下标；值实参在其后（variadic 解包）。
func (ext *fieldExtractor) emitWhereFilter(cc *ssa.CallCommon, cols []string,
	whereArg int, table string, line int) error {
	return ext.emitWhereFilterTyped(cc, cols, whereArg, table, line, "")
}

// emitWhereFilterTyped 同 emitWhereFilter，type 参数指定虚拟节点
// type_string（Q175：xorm；空默认 gorm）。
func (ext *fieldExtractor) emitWhereFilterTyped(cc *ssa.CallCommon, cols []string,
	whereArg int, table string, line int, vtype string) error {
	if vtype == "" {
		vtype = "gorm"
	}
	// Q177：先展平 whereArg 之后的全部实参（args ...interface{} 被变参
	// 打包成 ssa.Slice——展平所有元素），按 where 列顺序连 summary_io 边
	var flatVals []ssa.Value
	for i := whereArg + 1; i < len(cc.Args); i++ {
		a := cc.Args[i]
		if mi, ok := a.(*ssa.MakeInterface); ok {
			a = mi.X
		}
		if vals := variadicElems(a); len(vals) > 0 {
			flatVals = append(flatVals, vals...)
		} else {
			flatVals = append(flatVals, a)
		}
	}
	for i, col := range cols {
		id := domain.CanonicalID(string(ext.funcID) + "#ext." + vtype + "." + table + "." + col + ".filter@" + fmt.Sprintf("%d", line))
		if err := ext.emit(domain.Item{Node: &domain.CodeEntity{
			ID:        id,
			Kind:      domain.KindFieldAccess,
			Name:      table + "." + col,
			FilePath:  ext.currentFile,
			LineStart: line,
			Properties: map[string]any{
				"full_path":     table + "." + col,
				"instance_path": table + "." + col,
				"access_kind":   "filter",
				"code_snippet":  cc.String(),
				"type_string":   vtype,
				"is_external":   "true",
				"func_id":       string(ext.funcID),
			},
		}}); err != nil {
			return err
		}

		if i < len(flatVals) {
			val := flatVals[i]

			if mi, ok := val.(*ssa.MakeInterface); ok {
				val = mi.X
			}
			if _, isConst := val.(*ssa.Const); isConst {
				continue
			}
			valID, err := ext.emitValue(val)
			if err != nil || valID == "" {
				continue
			}
			if err := ext.emitEdgeKindLine(valID, id, domain.FactSummaryIO, line); err != nil {
				return err
			}
		}
	}
	return nil
}
