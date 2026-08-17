package ssa

import (
	"go/constant"
	"go/types"
	"sort"

	"github.com/schaepher/codeintel/internal/domain"
	"go.uber.org/zap"
	"golang.org/x/tools/go/ssa"
)

// fieldPattern 摘要字段模式："all" = 递归展开实参类型全部字段（Q16 保守策略）。
type fieldPattern string

// summarySpec 单个外部函数的摘要。
type summarySpec struct {
	Func        string
	Interface   string // Q156 接口摘要：接口全路径（动态 invoke 无静态 callee 时匹配）
	Method      string // 接口方法名
	Kind        string // 接口摘要类型：write（对象实参写）/ read（返回值读出）/ filter（where 过滤列）
	WhereArg    int    // 接口摘要：where 字符串实参下标（-1 无）
	ObjArg      int    // 接口摘要 write：对象实参下标
	IDArg       int    // 接口摘要 read：主键实参下标（-1 无；映射主键列 filter）
	Reads       []string
	Writes      []string
	ParamIndex  int // 操作第几个参数（0 为接收者）
	ReadsAll    bool
	WritesAll   bool
	ReadArgsAll bool   // fmt.Printf 风格：从 ParamIndex 起所有实参读全部字段
	SQLStmt     bool   // database/sql 语句调用：SQL 字符串在第 0 实参（Q97）
	SQLWrite    bool   // Exec 写 / Query 读
	TxBoundary  string // 事务边界标记（begin/commit/rollback，Q97）
	ORMWrite    bool   // ORM（GORM）写对象：字段→表.列 映射（②）
	ScanOut     bool   // Scan 写 out 实参：接收者值 → 实参指向变量（表关联链）
	ORMRead     bool   // ORM 读：对象读出 → 表.列 read 虚拟节点（键关联链）
	Type        string // 虚拟节点 type_string（Q175：gorm/xorm，默认 gorm）
	ChainTable  bool   // Q175：XORM 链式表名——表名来自链上 Table 调用
}

// userSummaryFile 对应 field-summary.yaml。
type userSummaryFile struct {
	Summaries []struct {
		Func       string   `yaml:"func"`
		Iface      string   `yaml:"iface"`       // Q156 接口摘要：接口全路径
		Method     string   `yaml:"method"`      // 接口方法名
		Kind       string   `yaml:"kind"`        // write/read/filter
		WhereArg   int      `yaml:"where_arg"`   // where 字符串实参下标
		ObjArg     int      `yaml:"obj_arg"`     // write 对象实参下标
		IDArg      int      `yaml:"id_arg"`      // read 主键实参下标
		SQLWrite   bool     `yaml:"sql_write"`   // kind=sql 时为写（ExecNonQuery）
		Type       string   `yaml:"type"`        // 虚拟节点 type_string（gorm/xorm）
		ChainTable bool     `yaml:"chain_table"` // XORM 链式表名
		Reads      []string `yaml:"reads"`
		Writes     []string `yaml:"writes"`
		ParamIndex int      `yaml:"param_index"`
		ORMWrite   bool     `yaml:"orm_write"` // ②：对象实参 → 表.列 映射（同内置 GORM 摘要）
		ORMRead    bool     `yaml:"orm_read"`  // 读：对象读出 → 表.列 read 节点
	} `yaml:"summaries"`
}

// summaryApplier 在单个函数内应用摘要（emitCall 调用）。
type summaryApplier struct {
	ext      *fieldExtractor
	specs    map[string]summarySpec
	applied  map[string]bool // 已创建的 external_summary 节点（去重）
	calleeID domain.CanonicalID
	pos      int
}

// expandAllFields 递归展开具名结构体的全部字段路径（深度 ≤ 4，
// 指针字段解一层，防递归类型爆炸）。
func expandAllFields(t types.Type, depth int) []string {
	logger := zap.L()
	logger.Debug("enter expandAllFields")
	defer logger.Debug("exit expandAllFields")
	if depth > 4 {
		return nil
	}
	named := namedStructOf(t)
	if named == nil {
		return nil
	}
	st := named.Underlying().(*types.Struct)
	prefix := named.Obj().Pkg().Path() + "." + named.Obj().Name()
	var out []string
	for i := 0; i < st.NumFields(); i++ {
		f := st.Field(i)
		path := prefix + "." + f.Name()
		out = append(out, path)

		if sub := namedStructOf(f.Type()); sub != nil {
			out = append(out, expandAllFields(f.Type(), depth+1)...)
		}
	}
	return out
}

// namedStructOf 解指针/取具名结构体（非结构体返回 nil）。
func namedStructOf(t types.Type) *types.Named {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok {
		return nil
	}
	if _, ok := named.Underlying().(*types.Struct); !ok {
		return nil
	}
	return named
}

// variadicElems 解开 ...any 变参的 Slice 包装取元素（Q177 重写）：
// alloc（数组）→ IndexAddr（元素地址）→ Store → 元素值。
// 不依赖 Referrers()（SSA 优化后可能为 nil）——直接扫描 alloc 所属
// 函数（含外层，闭包捕获场景）全部 Block 的 Store，匹配
// IndexAddr.X == alloc，按常量 index 排序。非 Slice 原样返回；
// 无法解出时返回原值（保守）。
func variadicElems(v ssa.Value) []ssa.Value {
	sl, ok := v.(*ssa.Slice)
	if !ok {
		return []ssa.Value{v}
	}
	alloc, ok := sl.X.(*ssa.Alloc)
	if !ok {
		return []ssa.Value{v}
	}
	// 收集 alloc 的元素写入：alloc → IndexAddr → Store（按 index 排序）
	type storeT struct {
		idx int
		val ssa.Value
	}
	var stores []storeT
	scanFn := func(fn *ssa.Function) {
		if fn == nil {
			return
		}
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				st, ok := instr.(*ssa.Store)
				if !ok {
					continue
				}
				if st.Addr == alloc {
					stores = append(stores, storeT{idx: -1, val: st.Val})
					continue
				}
				ia, ok := st.Addr.(*ssa.IndexAddr)
				if !ok || ia.X != alloc {
					continue
				}
				idx := -1
				if c, ok := ia.Index.(*ssa.Const); ok && c.Value != nil {
					if i, ok2 := constant.Int64Val(c.Value); ok2 {
						idx = int(i)
					}
				}
				stores = append(stores, storeT{idx: idx, val: st.Val})
			}
		}
	}
	scanFn(alloc.Parent())
	scanFn(alloc.Parent().Parent()) // 闭包捕获：alloc 在包装器，外层函数有 Store
	if len(stores) == 0 {
		return []ssa.Value{v}
	}
	sort.SliceStable(stores, func(i, j int) bool {
		// -1（整体 Store）排最前；其余按 index
		if stores[i].idx == -1 || stores[j].idx == -1 {
			return stores[i].idx < stores[j].idx
		}
		return stores[i].idx < stores[j].idx
	})
	var out []ssa.Value
	for _, st := range stores {
		if inner, ok := st.val.(*ssa.Slice); ok {
			out = append(out, variadicElems(inner)...)
		} else {
			out = append(out, st.val)
		}
	}
	if len(out) > 0 {
		return out
	}
	return []ssa.Value{v}
}
