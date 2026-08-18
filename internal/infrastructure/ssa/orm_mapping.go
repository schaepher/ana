package ssa

import (
	"go/constant"
	"go/types"

	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
	"go.uber.org/zap"
)

// gofOrmIfacePath gof（github.com/ixre/gof）db/orm.Orm 接口全路径——
// orm.Mapping(v interface{}, table string) 是其方法（Q211）。
const gofOrmIfacePath = "github.com/ixre/gof/db/orm.Orm"

// collectOrmMappings Q211：全量扫描模块内动态 invoke 调用，收集
// `orm.Mapping(entity{}, "table")` 的"实体类型→表名"注册（go2o
// repo_v1.go OrmMapping 形态——orm 是 orm.Orm 接口值，动态派发）。
// 收集独立于发射（Index 开头一次，emitFunction 按包并发期间只读）：
// Mapping 可能在包 A 注册、包 B 使用，按包顺序发射会漏。
func collectOrmMappings(prog *ssa.Program, modules []string) map[*types.Named]string {
	logger := zap.L()
	logger.Debug("enter collectOrmMappings")
	defer logger.Debug("exit collectOrmMappings")
	m := map[*types.Named]string{}
	for fn := range ssautil.AllFunctions(prog) {
		if !isModuleFunction(fn, modules) {
			continue
		}
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				call, ok := instr.(*ssa.Call)
				if !ok || !call.Common().IsInvoke() {
					continue
				}
				cc := call.Common()
				if cc.Method == nil || cc.Method.Name() != "Mapping" {
					continue
				}
				// 接收者须是 gof orm.Orm 接口（其他框架同名方法不误收）
				if iface := interfaceNamedOf(cc.Value.Type()); iface == nil ||
					iface.String() != gofOrmIfacePath {
					continue
				}
				if len(cc.Args) < 2 {
					continue
				}
				named := ormMappingEntityType(cc.Args[0])
				if named == nil {
					continue
				}
				if c, ok := unwrapConst(cc.Args[1]); ok && c.Value != nil &&
					c.Value.Kind() == constant.String {
					if t := constant.StringVal(c.Value); t != "" {
						m[named] = t
					}
				}
			}
		}
	}
	return m
}

// ormMappingEntityType 解 Mapping 第一参的实体类型：`ValueCoupon{}`
// 复合字面量是 Alloc，经 MakeInterface 装箱（Mapping(v interface{})）
// ——解包装 + 解引用取 *types.Named（匹配键：类型标识精确匹配）。
func ormMappingEntityType(v ssa.Value) *types.Named {
	if mi, ok := v.(*ssa.MakeInterface); ok {
		v = mi.X
	}
	t := derefType(v.Type())
	if named, ok := t.(*types.Named); ok {
		return named
	}
	return nil
}
