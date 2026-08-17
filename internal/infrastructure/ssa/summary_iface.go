package ssa

import (
	"fmt"
	"go/constant"
	"go/types"

	"github.com/schaepher/codeintel/internal/domain"
	"go.uber.org/zap"
	"golang.org/x/tools/go/ssa"
)

// applyInterfaceSummary 处理接口摘要（Q156）：动态 invoke 无静态 callee 且
// 候选实现为空（外部框架实现，如 gof fw.Repository——底层是 GORM）时，
// 按 "iface:" + 接口全路径 + "." + 方法名 匹配 spec（内置 + field-summary.yaml）：
//   - write：对象实参字段展开 → 表.列 write 虚拟节点 + 边（值 → 节点）
//   - read：返回值对象展开 → read 虚拟节点 + 边（节点 → 调用点值）
//   - filter：where 字符串实参 → 列名（AND/OR 拆分 + 占位符剥离）→ filter 节点
//   - IDArg >= 0：主键实参 → 主键列 filter（键关联）
//
// 表名：实体类型参数 M 的 TableName() 常量优先，fallback snakeCase(类型名)。
func (ext *fieldExtractor) applyInterfaceSummary(cc *ssa.CallCommon, callVal ssa.Value) (bool, error) {
	logger := zap.L()
	logger.Debug("enter (fieldExtractor).applyInterfaceSummary")
	defer logger.Debug("exit (fieldExtractor).applyInterfaceSummary")
	iface := interfaceNamedOf(cc.Value.Type())
	if iface == nil {
		return false, nil
	}
	obj := iface.Obj()
	if obj == nil || obj.Pkg() == nil {
		return false, nil
	}
	key := "iface:" + obj.Pkg().Path() + "." + obj.Name() + "." + cc.Method.Name()
	spec, ok := ext.specs[key]
	if !ok {
		logger.Debug("iface spec 未匹配", zap.String("key", key))
		return false, nil
	}
	return ext.applySpecKind(cc, callVal, spec, key)
}

// applySpecKind 按 spec.Kind 分派的公共逻辑（Q177 修复：静态摘要
// applySummary 与接口摘要 applyInterfaceSummary 共用——XORM 真实
// *xorm.Session 具体类型调用走静态路径，同样需要 kind 分派）：
// 表名/实体解析 → 链式传播 → kind 发射（table/write/read/filter/sql）
// + WhereArg filter。
func (ext *fieldExtractor) applySpecKind(cc *ssa.CallCommon, callVal ssa.Value, spec summarySpec, key string) (bool, error) {
	logger := zap.L()
	// 表名/实体解析按形态分流：
	//  - sql/table（Q175 XORM Table）：无需实体/表名（SQL 自带 / 链式记录）
	//  - filter（Q175 XORM Where）：无需实体，表名查链式 Table（ChainTable）
	//  - write/read：实体类型 → TableName() → 链式表名兜底
	var table string
	var entity types.Type
	switch spec.Kind {
	case "filter":
		if spec.ChainTable {
			table = ext.chainTableName(cc)
		}
		if table == "" {
			return false, nil
		}
	case "write", "read":
		entity = entityTypeOf(cc, spec)
		if entity == nil {
			logger.Debug("iface entity 未解析", zap.String("key", key))
			return false, nil
		}
		table = ext.tableNameOf(entity)
		if table == "" && spec.ChainTable {

			table = ext.chainTableName(cc)
		}
		if table == "" {
			logger.Debug("iface table 为空", zap.String("key", key))
			return false, nil
		}
	}

	if spec.ChainTable && table != "" && callVal != nil {
		ext.recordChainTable(callVal, table)
	}
	line := ext.prog.Fset.PositionFor(cc.Pos(), false).Line
	switch spec.Kind {
	case "table":
		// 表名实参：遍历 Args 找字符串常量（Q177 静态调用 Args[0] 是
		// receiver——iface 时 Args[0] 即表名，遍历兼容两种形态）
		var name string
		for _, a := range cc.Args {
			// Q177 真实形态：Table(tableNameOrBean interface{}) 时字符串
			// 字面量被 MakeInterface 包装——unwrapConst 统一解包
			if c, ok := unwrapConst(a); ok {
				if s := constant.StringVal(c.Value); s != "" {
					name = s
					break
				}
			}
		}
		if name != "" {
			if callVal != nil {
				ext.recordChainTable(callVal, name)
			}
			typ := spec.Type
			if typ == "" {
				typ = "gorm"
			}
			id := domain.CanonicalID(string(ext.funcID) + "#ext." + typ + "." + name + ".write@" + fmt.Sprintf("%d", line))
			if err := ext.emit(domain.Item{Node: &domain.CodeEntity{
				ID:        id,
				Kind:      domain.KindFieldAccess,
				Name:      name,
				FilePath:  ext.currentFile,
				LineStart: line,
				Properties: map[string]any{
					"full_path":     name,
					"instance_path": name,
					"access_kind":   "write",
					"code_snippet":  cc.String(),
					"type_string":   typ,
					"is_external":   "true",
					"func_id":       string(ext.funcID),
				},
			}}); err != nil {
				return false, err
			}
		}
	case "write":
		if spec.ObjArg < 0 || spec.ObjArg >= len(cc.Args) {
			return false, nil
		}
		arg := cc.Args[spec.ObjArg]
		if mi, ok := arg.(*ssa.MakeInterface); ok {
			arg = mi.X
		}
		objID, err := ext.emitValue(arg)
		if err != nil {
			return false, err
		}
		if err := ext.emitEntityFields(entity, table, "write", line, objID, cc, spec.Type); err != nil {
			return false, err
		}
	case "read":
		// 对象读出 → read 虚拟节点 + 边（节点 → 值）。值来源：ObjArg
		// 指定的输出对象实参（orm.Orm.Get(id, &e) 读进 e）优先，否则
		// 调用点返回值
		var callID domain.CanonicalID
		if spec.ObjArg >= 0 && spec.ObjArg < len(cc.Args) {
			arg := cc.Args[spec.ObjArg]
			if mi, ok := arg.(*ssa.MakeInterface); ok {
				arg = mi.X
			}
			if id, err := ext.emitValue(arg); err == nil {
				callID = id
			}
		} else if callVal != nil {
			if id, err := ext.emitValue(callVal); err == nil {
				callID = id
			}
		}
		if err := ext.emitEntityFields(entity, table, "read", line, callID, cc, spec.Type); err != nil {
			return false, err
		}

		// IDArg > 0 才触发（Q177：默认 0 是"未设置"——Find/Iterate 等
		// 无主键参数的读不误产主键 filter；Get(id, &e) 的 id 下标显式设置）
		if spec.IDArg > 0 && spec.IDArg < len(cc.Args) {
			if err := ext.emitWhereFilterTyped(cc, []string{pkColumnOf(entity)}, spec.IDArg-1, table, line, spec.Type); err != nil {
				return false, err
			}
		}
	case "filter":

	case "sql":

		return true, ext.applySQLSummary(cc, "", spec, callVal, spec.WhereArg)
	}
	if spec.WhereArg >= 0 {
		if spec.WhereArg >= len(cc.Args) {
			return false, nil
		}
		if c, ok := unwrapConst(cc.Args[spec.WhereArg]); ok {
			// Q177 真实形态：Where(query interface{}) 常量被包装
			cols := whereColsOf(constant.StringVal(c.Value))
			if err := ext.emitWhereFilterTyped(cc, cols, spec.WhereArg, table, line, spec.Type); err != nil {
				return false, err
			}
		}
	}
	return true, nil
}

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
	for i, col := range cols {
		id := domain.CanonicalID(string(ext.funcID) + "#ext.gorm." + table + "." + col + ".filter@" + fmt.Sprintf("%d", line))
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

		idx := whereArg + 1 + i
		if idx < len(cc.Args) {
			val := cc.Args[idx]
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

// entityTypeOf 取接口摘要的实体类型：泛型接口实例化（Repository[M]）的
// 类型实参优先；fallback 按 kind 从对象实参/返回值类型取。
// tableNameOf 实体类型表名：TableName() 方法（SSA Return 常量）优先，
// fallback snakeCase(类型名)（GORM 默认命名）。
// pkColumnOf 主键列名：字段 pk:"yes" tag（gorm column 优先）→ 该字段列名；
// 无标记时 fallback "id"。
// gormColumnOf 提取 gorm:"column:x" 的列名（无则 snake_case 字段名）。
