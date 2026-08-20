package ssa

import (
	"fmt"
	"go/constant"
	"go/types"
	"reflect"

	"github.com/schaepher/codeintel/internal/domain"
	"go.uber.org/zap"
	"golang.org/x/tools/go/ssa"
)

// applyORMRead 处理 ORM 读调用（Find/First/Take/Last）：对象实参
// （&sessions / &s）→ 表名（Model 链式溯源或对象类型）+ 字段展开 →
// 表.列 read 虚拟节点 + 边（读出值 → 对象，与写方向相反）——
// 读出的字段（s.ID）作为后续查询实参时，键关联链贯通。
func (ext *fieldExtractor) applyORMRead(cc *ssa.CallCommon, calleeID domain.CanonicalID) error {
	logger := zap.L()
	logger.Debug("enter (fieldExtractor).applyORMRead")
	defer logger.Debug("exit (fieldExtractor).applyORMRead")
	if len(cc.Args) < 2 {
		return nil
	}
	realArg := cc.Args[1]
	if mi, ok := realArg.(*ssa.MakeInterface); ok {
		realArg = mi.X
	}
	t := derefSlice(derefType(realArg.Type()))
	named, ok := t.(*types.Named)
	if !ok {
		return nil
	}

	table := chainTableNameValue(cc.Args[0])
	if table == "" {
		table = ext.tableNameOf(named)
	}
	if table == "" {
		if scope := chainScopeObject(cc.Args[0]); scope != nil {
			table = snakeCase(scope.Obj().Name())
		} else {
			table = snakeCase(named.Obj().Name())
		}
	}
	st, ok := named.Underlying().(*types.Struct)
	if !ok {
		return nil
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
		id := domain.CanonicalID(string(ext.funcID) + "#ext.gorm." + table + "." + col + ".read@" + fmt.Sprintf("%d", line))
		if err := ext.emit(domain.Item{Node: &domain.CodeEntity{
			ID:        id,
			Kind:      domain.KindFieldAccess,
			Name:      table + "." + col,
			FilePath:  ext.currentFile,
			LineStart: line,
			Properties: map[string]any{
				"full_path":     table + "." + col,
				"instance_path": table + "." + col,
				"access_kind":   "read",
				"code_snippet":  cc.String(),
				"type_string":   "gorm",
				"is_external":   "true",
				"func_id":       string(ext.funcID),
			},
		}}); err != nil {
			return err
		}

		if objID != "" {
			if err := ext.emitEdgeKindLine(id, objID, domain.FactSummaryIO, line); err != nil {
				return err
			}
		}
	}
	return nil
}

// applyORMWrite 处理 ORM 写调用（②⑦：GORM Create/Save/Updates/Delete/
// Update 等）：
//   - 对象实参（结构体字面量/变量）：类型 → 表名（snake_case）+ 字段 →
//     列名 → 虚拟节点 表.列 + summary_io 边（字段值 → 虚拟节点）。
//     字段值不可定位（变量/调用结果/空字面量——调用点无字段级 Store）
//     时不跳过该列：仍按类型展开生成 表.列 节点，连对象值兜底
//   - 字符串列名实参（Update("col", v) 单列更新）：表名溯源链式调用
//     receiver 的 Model(&X{}) 范围对象（⑦），列名取字符串实参

// emitORMColumn 生成单个 表.列 虚拟节点 + summary_io 边（值实参 → 节点）。

// chainScopeObject 溯源链式调用的范围对象（⑦）：Update/Updates 的 receiver
// 沿定义链回溯中间调用（Where/Model 等），找到实参为结构体对象的调用
// （如 Model(&Session{ID:...})）返回其类型。链上游无结构体实参返回 nil。

// fieldValueOf 按字段索引取对象值的字段读取（对象为 Alloc/寄存器时经
// FieldAddr 或 Field 指令；无法定位时返回 nil——字段值无 SSA 实体则
// 跳过该列）。

func (ext *fieldExtractor) tableNameOf(entity types.Type) string {
	if p, ok := entity.(*types.Pointer); ok {
		entity = p.Elem()
	}
	named, ok := entity.(*types.Named)
	if !ok {
		return ""
	}
	// Q205 缓存：TableName() 方法扫描（FuncValue + returnOperands）在无
	// spec 接口调用兜底下被高频触发（go2o 数千接口调用），同类型只算一次
	if cached, ok := ext.tableNames[named]; ok {
		return cached
	}
	name := tableNameOfSlow(ext, entity, named)
	ext.tableNames[named] = name // 缓存任何结果（含空串——该类型无 TableName）
	return name
}

// tableNameOfSlow 表名解析本体（Q205 拆出供缓存包裹）：
// TableName() 方法（SSA Return 常量）优先，fallback snakeCase(类型名)。
func tableNameOfSlow(ext *fieldExtractor, entity types.Type, named *types.Named) string {
	if m := types.NewMethodSet(types.NewPointer(entity)).Lookup(nil, "TableName"); m != nil {
		if fn, ok := m.Obj().(*types.Func); ok {
			if ssaFn := ext.prog.FuncValue(fn); ssaFn != nil {
				for _, ret := range returnOperands(ssaFn) {
					for _, rv := range ret {
						if c, ok := rv.(*ssa.Const); ok && c.Value != nil {
							if s := constant.StringVal(c.Value); s != "" {
								return s
							}
						}
					}
				}
			}
		}
	}
	// Q211：orm.Mapping 注册（实体类型→表名，go2o Mapping(ValueCoupon{},
	// "pm_coupon")）——TableName() 方法之后、snakeCase fallback 之前；
	// 链式 Table() 在调用点（chainTableNameValue）已优先
	if t, ok := ext.typeMapping[named]; ok {
		return t
	}
	return snakeCase(named.Obj().Name())
}

func pkColumnOf(entity types.Type) string {
	if p, ok := entity.(*types.Pointer); ok {
		entity = p.Elem()
	}
	if named, ok := entity.(*types.Named); ok {
		if st, ok := named.Underlying().(*types.Struct); ok {
			for i := 0; i < st.NumFields(); i++ {
				f := st.Field(i)
				tag := reflect.StructTag(st.Tag(i))
				if tag.Get("pk") == "yes" || tag.Get("pk") == "true" {
					return gormColumnOf(tag, f.Name())
				}
			}
		}
	}
	return "id"
}
