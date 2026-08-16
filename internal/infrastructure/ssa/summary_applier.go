// 外部函数摘要系统（field_trace.md §7）：内置摘要 + 用户 field-summary.yaml。
// 构建器遇到带摘要的外部函数调用时：
//   - 生成虚拟 field_access 节点（is_external=1，func_id=调用者）
//   - external_summary 节点 + summary_io 边
//   - 写摘要：INDIRECT_WRITE 边（调用者 → 虚拟节点）+ data_flows_to（实参 → 虚拟节点）
//   - 读摘要：data_flows_to（虚拟节点 → 实参）
//   - 写摘要的字段进入调用者的间接写摘要表（indirectWrites）
package ssa

import (
	"fmt"
	"go/constant"
	"go/types"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/schaepher/codeintel/internal/domain"
	"golang.org/x/tools/go/ssa"
	"gopkg.in/yaml.v3"
	"go.uber.org/zap"
)

// fieldPattern 摘要字段模式："all" = 递归展开实参类型全部字段（Q16 保守策略）。
type fieldPattern string

const (
	patternAll = "all"
)

// summarySpec 单个外部函数的摘要。
type summarySpec struct {
	Func        string
	Reads       []string
	Writes      []string
	ParamIndex  int // 操作第几个参数（0 为接收者）
	ReadsAll    bool
	WritesAll   bool
	ReadArgsAll bool // fmt.Printf 风格：从 ParamIndex 起所有实参读全部字段
	SQLStmt     bool // database/sql 语句调用：SQL 字符串在第 0 实参（Q97）
	SQLWrite    bool // Exec 写 / Query 读
	TxBoundary  string // 事务边界标记（begin/commit/rollback，Q97）
	ORMWrite    bool // ORM（GORM）写对象：字段→表.列 映射（②）
	ScanOut     bool // Scan 写 out 实参：接收者值 → 实参指向变量（表关联链）
	ORMRead     bool // ORM 读：对象读出 → 表.列 read 虚拟节点（键关联链）
}

// userSummaryFile 对应 field-summary.yaml。
type userSummaryFile struct {
	Summaries []struct {
		Func       string   `yaml:"func"`
		Reads      []string `yaml:"reads"`
		Writes     []string `yaml:"writes"`
		ParamIndex int      `yaml:"param_index"`
		ORMWrite   bool     `yaml:"orm_write"` // ②：对象实参 → 表.列 映射（同内置 GORM 摘要）
		ORMRead    bool     `yaml:"orm_read"`  // 读：对象读出 → 表.列 read 节点
	} `yaml:"summaries"`
}

// loadSummaries 加载内置 + 用户摘要（用户覆盖同名内置），返回 函数全路径 → spec。
// YAML 解析失败/重复定义时跳过对应条目并输出警告（构建降级，不中止，Q59）。
func loadSummaries(repoPath string) (map[string]summarySpec, []string) {
	logger := zap.L()
	logger.Debug("enter loadSummaries")
	defer logger.Debug("exit loadSummaries")
	specs := builtinSummaries()
	var warnings []string

	data, err := os.ReadFile(filepath.Join(repoPath, "field-summary.yaml"))
	if err != nil {
		if !os.IsNotExist(err) {
			warnings = append(warnings, fmt.Sprintf("读取 field-summary.yaml: %v", err))
		}
		return specs, warnings
	}
	var uf userSummaryFile
	if err := yaml.Unmarshal(data, &uf); err != nil {
		warnings = append(warnings, fmt.Sprintf("field-summary.yaml 解析失败，已忽略: %v", err))
		return specs, warnings
	}
	// 用户条目可覆盖同名内置摘要（Q59：仅文件内重复定义报错）
	seenUser := map[string]bool{}
	for _, s := range uf.Summaries {
		if s.Func == "" {
			warnings = append(warnings, "field-summary.yaml: 存在缺少 func 的条目，已跳过")
			continue
		}
		if seenUser[s.Func] {
			warnings = append(warnings, fmt.Sprintf("field-summary.yaml: %s 重复定义，已忽略", s.Func))
			continue
		}
		seenUser[s.Func] = true
		specs[s.Func] = summarySpec{
			Func:       s.Func,
			Reads:      s.Reads,
			Writes:     s.Writes,
			ParamIndex: s.ParamIndex,
			ORMWrite:   s.ORMWrite,
			ORMRead:    s.ORMRead,
		}
	}
	return specs, warnings
}

// builtinSummaries 内置摘要（field_trace.md §7.3）。
// context.Context 为透明传递，无条目。
func builtinSummaries() map[string]summarySpec {
	specs := map[string]summarySpec{
		"encoding/json.Unmarshal": {
			Func: "encoding/json.Unmarshal", ParamIndex: 1,
			WritesAll: true, // 写入 v 的所有字段（递归）
		},
		"fmt.Printf": {
			Func: "fmt.Printf", ParamIndex: 1,
			ReadArgsAll: true, // 读取所有 args 的字段（保守策略）
		},
		"database/sql.(Rows).Scan": {
			Func: "database/sql.(Rows).Scan", ParamIndex: 1,
			WritesAll: true, ScanOut: true, // 写入 dest 的指向值 + 接收者 → out 变量（表关联）
		},
		"database/sql.(Row).Scan": {
			Func: "database/sql.(Row).Scan", ParamIndex: 1,
			WritesAll: true, ScanOut: true, // 单行版（row.Scan(&x) 常见形态）
		},
		"net/http.(Request).ParseForm": {
			Func: "net/http.(Request).ParseForm", ParamIndex: 0,
			Writes: []string{"Form"},
		},
		"net/http.(Request).FormValue": {
			Func: "net/http.(Request).FormValue", ParamIndex: 0,
			Reads: []string{"Form"},
		},
	}
	// database/sql 持久化（Q97）：SQL 语句调用 + 事务边界。
	// SSA 方法 ID 统一去指针接收者（(DB).Exec），值/指针接收者同表
	for _, fn := range []string{"Exec", "Query", "QueryRow", "Prepare"} {
		for _, recv := range []string{"(DB)", "(Tx)"} {
			specs["database/sql."+recv+"."+fn] = summarySpec{
				Func: "database/sql." + recv + "." + fn, ParamIndex: 1,
				SQLStmt: true, SQLWrite: fn == "Exec",
			}
		}
	}
	// prometheus 观测指标（Q99）：字段值/维度传入指标函数 → 读摘要
	for _, fn := range []string{
		"prometheus.(Counter).Inc", "prometheus.(Counter).Add",
		"prometheus.(CounterVec).WithLabelValues",
		"prometheus.(Histogram).Observe",
		"prometheus.(Gauge).Set", "prometheus.(Gauge).Inc", "prometheus.(Gauge).Dec",
		"prometheus.(Summary).Observe",
	} {
		specs["github.com/prometheus/client_golang/"+fn] = summarySpec{
			Func: "github.com/prometheus/client_golang/" + fn, ParamIndex: 0,
			ReadArgsAll: true, // 观测：读实参字段（指标维度/值来源）
		}
	}
	// GORM 写操作（②：ORM 更新映射字段→列）：实参对象类型→表名、
	// 字段→列名（snake_case）
	for _, fn := range []string{"Create", "Save", "Updates", "Delete", "Update"} {
		specs["gorm.io/gorm.(DB)."+fn] = summarySpec{
			Func: "gorm.io/gorm.(DB)." + fn, ParamIndex: 1, ORMWrite: true,
		}
	}
	// Where 过滤（表关联键）：Where("col = ?", v) 字符串列名 → filter 节点
	specs["gorm.io/gorm.(DB).Where"] = summarySpec{
		Func: "gorm.io/gorm.(DB).Where", ParamIndex: 1, ORMWrite: true,
	}
	// GORM 读（键关联链贯通）：Find/First/Take/Last 对象读出 → 表.列 read
	for _, fn := range []string{"Find", "First", "Take", "Last"} {
		specs["gorm.io/gorm.(DB)."+fn] = summarySpec{
			Func: "gorm.io/gorm.(DB)." + fn, ParamIndex: 1, ORMRead: true,
		}
	}
	specs["database/sql.(DB).Begin"] = summarySpec{Func: "database/sql.(DB).Begin", TxBoundary: "begin"}
	specs["database/sql.(Tx).Commit"] = summarySpec{Func: "database/sql.(Tx).Commit", TxBoundary: "commit"}
	specs["database/sql.(Tx).Rollback"] = summarySpec{Func: "database/sql.(Tx).Rollback", TxBoundary: "rollback"}
	return specs
}

// summaryApplier 在单个函数内应用摘要（emitCall 调用）。
type summaryApplier struct {
	ext      *fieldExtractor
	specs    map[string]summarySpec
	applied  map[string]bool // 已创建的 external_summary 节点（去重）
	calleeID domain.CanonicalID
	pos      int
}

// applySummary 对带摘要的外部函数调用生成虚拟节点与边。
// 返回 false 表示无摘要（或无需处理）。
func (ext *fieldExtractor) applySummary(cc *ssa.CallCommon, callee *ssa.Function, callVal ssa.Value) (bool, error) {
	logger := zap.L()
	logger.Debug("enter (fieldExtractor).applySummary")
	defer logger.Debug("exit (fieldExtractor).applySummary")
	if len(ext.specs) == 0 {
		return false, nil
	}
	key := summaryKey(callee)
	spec, ok := ext.specs[key]
	if !ok {
		return false, nil
	}
	// external_summary 节点（首个调用点创建）
	calleeID, ok := ext.funcIDOfFn(callee)
	if !ok {
		return false, nil
	}
	if !ext.extSummaries[calleeID] {
		ext.extSummaries[calleeID] = true
		specJSON := fmt.Sprintf(`{"reads":%q,"writes":%q,"param_index":%d}`,
			strings.Join(spec.Reads, ","), strings.Join(spec.Writes, ","), spec.ParamIndex)
		if err := ext.emit(domain.Item{Node: &domain.CodeEntity{
			ID:   calleeID,
			Kind: domain.KindExternalSummary,
			Name: callee.Name(),
			Properties: map[string]any{
				"summary_json": specJSON,
				"func_id":      string(ext.funcID),
			},
		}}); err != nil {
			return true, err
		}
	}
	// SQL 持久化（Q97）：SQL 字符串解析表列 + 值实参映射（写）/ 读边（P0-2）
	if spec.SQLStmt {
		return true, ext.applySQLSummary(cc, calleeID, spec, callVal)
	}
	// 事务边界（Q97）：Begin/Commit/Rollback → 事务虚拟节点
	if spec.TxBoundary != "" {
		return true, ext.applyTxBoundary(cc, calleeID, spec.TxBoundary)
	}
	// ORM 写（②）：GORM 对象写 → 字段→表.列 映射
	if spec.ORMWrite {
		return true, ext.applyORMWrite(cc, calleeID)
	}
	// ORM 读（键关联链）：对象读出 → 表.列 read 虚拟节点
	if spec.ORMRead {
		return true, ext.applyORMRead(cc, calleeID)
	}
	// Scan 写 out 实参（表关联链）：接收者值 → 实参指向的局部变量。
	// 不 return——继续逐参数应用（WritesAll 展开 dest 字段虚拟节点）
	if spec.ScanOut {
		if err := ext.applyScanOut(cc, calleeID); err != nil {
			return true, err
		}
	}
	// 逐参数应用
	start := spec.ParamIndex
	if spec.ParamIndex < 0 || spec.ParamIndex >= len(cc.Args) {
		return true, nil // param_index 越界：忽略（类型不匹配同语义）
	}
	for i := start; i < len(cc.Args); i++ {
		arg := cc.Args[i]
		if err := ext.applyArgSummary(cc, calleeID, spec, i, arg); err != nil {
			return true, err
		}
	}
	return true, nil
}

// applyArgSummary 对单个实参应用摘要（all 模式递归展开字段）。
func (ext *fieldExtractor) applyArgSummary(cc *ssa.CallCommon, calleeID domain.CanonicalID,
	spec summarySpec, argIdx int, arg ssa.Value) error {
	logger := zap.L()
	logger.Debug("enter (fieldExtractor).applyArgSummary")
	defer logger.Debug("exit (fieldExtractor).applyArgSummary")
	// variadic（...any）实参在 SSA 中被包装成 []any 的 Slice 指令，
	// 从底层 alloc 的 Store 取出元素逐个应用（fmt.Printf 等）
	elems := variadicElems(arg)
	for _, el := range elems {
		if err := ext.applyArgSummaryOne(cc, calleeID, spec, el); err != nil {
			return err
		}
	}
	return nil
}

// applyArgSummaryOne 对单个实参值应用摘要。
func (ext *fieldExtractor) applyArgSummaryOne(cc *ssa.CallCommon, calleeID domain.CanonicalID,
	spec summarySpec, arg ssa.Value) error {
	logger := zap.L()
	logger.Debug("enter (fieldExtractor).applyArgSummaryOne")
	defer logger.Debug("exit (fieldExtractor).applyArgSummaryOne")
	line := ext.prog.Fset.PositionFor(cc.Pos(), false).Line
	// any/interface 形参的实参在 SSA 中被 MakeInterface 装箱（Type()=any），
	// 解包后用真实值/真实类型展开字段
	realArg := arg
	if mi, ok := arg.(*ssa.MakeInterface); ok {
		realArg = mi.X
	}
	fields := spec.Reads
	if spec.WritesAll {
		fields = expandAllFields(realArg.Type(), 0)
	}
	if spec.ReadArgsAll {
		fields = expandAllFields(realArg.Type(), 0)
	}
	argID, err := ext.emitValue(realArg)
	if err != nil {
		return err
	}
	base := ext.instancePath(realArg)
	for _, f := range fields {
		fullPath := f
		// 相对字段路径（"Form" / "user.ID"）补全为类型限定路径：不含包路径
		// （/）即相对；实例前缀（user.）剥除只取字段名（S2：此前含点相对
		// 路径被全拼成 pkg.T.user.ID，匹配不到真实字段）
		if !strings.Contains(f, "/") {
			if named := namedStructOf(realArg.Type()); named != nil {
				seg := f
				if i := strings.LastIndex(f, "."); i >= 0 {
					seg = f[i+1:]
				}
				fullPath = named.Obj().Pkg().Path() + "." + named.Obj().Name() + "." + seg
			}
		}
		access := "read"
		if spec.WritesAll || contains(spec.Writes, f) {
			access = "write"
		}
		instance := base + "." + lastPathSeg(f)
		id := domain.CanonicalID(string(ext.funcID) + "#ext." + fullPath + "." + access + "@" + fmt.Sprintf("%d", line))
		node := &domain.CodeEntity{
			ID:        id,
			Kind:      domain.KindFieldAccess,
			Name:      instance,
			FilePath:  ext.currentFile,
			LineStart: line,
			LineEnd:   line,
			Properties: map[string]any{
				"full_path":     fullPath,
				"instance_path": instance,
				"access_kind":   access,
				"type_string":   realArg.Type().String(),
				"func_id":       string(ext.funcID),
				"is_external":   "true",
				"code_snippet":  ext.sourceLine(ext.currentFile, line),
			},
		}
		if err := ext.emit(domain.Item{Node: node}); err != nil {
			return err
		}
		// summary_io：external_summary → 虚拟字段节点
		if err := ext.emitEdgeKind(calleeID, id, domain.FactSummaryIO); err != nil {
			return err
		}
		if access == "write" {
			// INDIRECT_WRITE：调用者 → 虚拟节点；data_flows_to：实参 → 虚拟节点
			if err := ext.emitEdgeKind(ext.funcID, id, domain.FactIndirectWrite); err != nil {
				return err
			}
			if argID != "" {
				if err := ext.emitEdge(argID, id); err != nil {
					return err
				}
			}
			// 摘要表：调用者的间接写
			if ext.funcData != nil {
				ext.funcData.indirectWrites = append(ext.funcData.indirectWrites, fieldEntry{
					fieldPath:    fullPath,
					instancePath: instance,
					line:         line,
					snippet:      ext.sourceLine(ext.currentFile, line),
				})
			}
		} else if argID != "" {
			// 读：虚拟节点 → 实参（与 Field 读方向一致）
			if err := ext.emitEdge(id, argID); err != nil {
				return err
			}
		}
	}
	return nil
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
		// 嵌套字段：类型限定路径（Inner.X，声明类型路径，与 field_access.full_path 一致）
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

// summaryKey 生成函数全路径摘要键：pkg.Func / pkg.(T).Method。
func summaryKey(fn *ssa.Function) string {
	if fn == nil || fn.Pkg == nil || fn.Pkg.Pkg == nil {
		return ""
	}
	obj, ok := fn.Object().(*types.Func)
	if !ok || obj == nil {
		return ""
	}
	path := fn.Pkg.Pkg.Path()
	sig, _ := obj.Type().(*types.Signature)
	if sig != nil && sig.Recv() != nil {
		t := sig.Recv().Type()
		if p, ok := t.(*types.Pointer); ok {
			t = p.Elem()
		}
		if named, ok := t.(*types.Named); ok {
			return path + ".(" + named.Obj().Name() + ")." + fn.Name()
		}
	}
	return path + "." + fn.Name()
}

// lastPathSeg 取路径最后一段（instance_path 拼接用）。
func lastPathSeg(p string) string {
	if i := strings.LastIndex(p, "."); i >= 0 {
		return p[i+1:]
	}
	return p
}

// variadicElems 解开 ...any 变参的 Slice 包装取元素：
// alloc（数组）→ IndexAddr（元素地址）→ Store → 元素值（MakeInterface 等）。
// 非 Slice 原样返回；无法解出时返回原值（保守）。
func variadicElems(v ssa.Value) []ssa.Value {
	sl, ok := v.(*ssa.Slice)
	if !ok {
		return []ssa.Value{v}
	}
	alloc, ok := sl.X.(*ssa.Alloc)
	if !ok || alloc.Referrers() == nil {
		return []ssa.Value{v}
	}
	var out []ssa.Value
	for _, ref := range *alloc.Referrers() {
		switch r := ref.(type) {
		case *ssa.IndexAddr:
			// 元素写入：IndexAddr → Store → Val
			if r.Referrers() == nil {
				continue
			}
			for _, ref2 := range *r.Referrers() {
				st, ok := ref2.(*ssa.Store)
				if !ok || st.Addr != r {
					continue
				}
				if inner, ok := st.Val.(*ssa.Slice); ok {
					out = append(out, variadicElems(inner)...)
				} else {
					out = append(out, st.Val)
				}
			}
		case *ssa.Store:
			// 打包后的 slice 存入变量（Addr == alloc）
			if st, ok := ref.(*ssa.Store); ok && st.Addr == alloc {
				if inner, ok := st.Val.(*ssa.Slice); ok {
					out = append(out, variadicElems(inner)...)
				} else {
					out = append(out, st.Val)
				}
			}
		}
	}
	if len(out) > 0 {
		return out
	}
	return []ssa.Value{v}
}

// applySQLSummary 处理 SQL 语句调用（Q97）：SQL 字符串（第 0 实参）解析
// 表名与列名 → 虚拟节点（Name=表.列）；后续值实参按 ? 顺序映射列，
// 发 summary_io 边（字段值 → 虚拟节点）。
func (ext *fieldExtractor) applySQLSummary(cc *ssa.CallCommon, calleeID domain.CanonicalID, spec summarySpec, callVal ssa.Value) error {
	logger := zap.L()
	logger.Debug("enter (fieldExtractor).applySQLSummary")
	defer logger.Debug("exit (fieldExtractor).applySQLSummary")
	// args[0] 为接收者（db）；SQL 字符串在 args[1]，值实参（...any）在 args[2:]
	if len(cc.Args) < 2 {
		return nil
	}
	sqlStr := ""
	if c, ok := cc.Args[1].(*ssa.Const); ok && c.Value != nil {
		sqlStr = constant.StringVal(c.Value)
	}
	table, cols, whereCols := parseSQLStmt(sqlStr)
	line := ext.prog.Fset.PositionFor(cc.Pos(), false).Line
	// P0-2 读路径（Query/QueryRow/Prepare）：SELECT 列 → read 虚拟节点
	// （表.列）+ 读边（虚拟节点 → 返回的 rows/row 值）——query table
	// 读取方闭环的数据基础
	if !spec.SQLWrite {
		if table == "" {
			return nil
		}
		if len(cols) == 0 {
			cols = []string{""} // SELECT * → 表级读节点
		}
		var callID domain.CanonicalID
		if callVal != nil {
			callID, _ = ext.emitValue(callVal)
		}
		for _, col := range cols {
			name := table
			if col != "" {
				name = table + "." + col
			}
			id := domain.CanonicalID(string(ext.funcID) + "#ext.sql." + name + ".read@" + fmt.Sprintf("%d", line))
			if err := ext.emit(domain.Item{Node: &domain.CodeEntity{
				ID:        id,
				Kind:      domain.KindFieldAccess,
				Name:      name,
				FilePath:  ext.currentFile,
				LineStart: line,
				Properties: map[string]any{
					"full_path":     name,
					"instance_path": name,
					"access_kind":   "read",
					"code_snippet":  sqlStr,
					"type_string":   "sql",
					"is_external":   "true",
					"func_id":       string(ext.funcID),
				},
			}}); err != nil {
				return err
			}
			if callID != "" {
				// 方向：表列读出 → 值（rows/row 对象），与写边（值→节点）反向
				if err := ext.emitEdgeKindLine(id, domain.CanonicalID(callID), domain.FactSummaryIO, line); err != nil {
					return err
				}
			}
		}
		// 表关联（WHERE 值流）：值实参按 ? 顺序映射过滤列——产 filter
		// 虚拟节点（表.列）+ 边（值 → 节点，与写同方向）：A.X 读出的值
		// 流入 B.Y 过滤列时，数据流链贯通两表
		if len(whereCols) > 0 {
			values := []ssa.Value{}
			for i := 2; i < len(cc.Args); i++ {
				values = append(values, variadicElems(cc.Args[i])...)
			}
			for i, arg := range values {
				if i >= len(whereCols) {
					break // 超出 WHERE 列的 ?（如 LIMIT ?）跳过
				}
				name := table + "." + whereCols[i]
				id := domain.CanonicalID(string(ext.funcID) + "#ext.sql." + name + ".filter@" + fmt.Sprintf("%d", line))
				if err := ext.emit(domain.Item{Node: &domain.CodeEntity{
					ID:        id,
					Kind:      domain.KindFieldAccess,
					Name:      name,
					FilePath:  ext.currentFile,
					LineStart: line,
					Properties: map[string]any{
						"full_path":     name,
						"instance_path": name,
						"access_kind":   "filter",
						"code_snippet":  sqlStr,
						"type_string":   "sql",
						"is_external":   "true",
						"func_id":       string(ext.funcID),
					},
				}}); err != nil {
					return err
				}
				realArg := arg
				if mi, ok := arg.(*ssa.MakeInterface); ok {
					realArg = mi.X
				}
				argID, err := ext.emitValue(realArg)
				if err != nil || argID == "" {
					continue
				}
				if err := ext.emitEdgeKindLine(argID, id, domain.FactSummaryIO, line); err != nil {
					return err
				}
			}
		}
		return nil
	}
	// 写路径：虚拟节点：表.列（无列时表）；值实参（variadic 解包）按 ? 顺序映射列
	access := "write"
	values := []ssa.Value{}
	for i := 2; i < len(cc.Args); i++ {
		values = append(values, variadicElems(cc.Args[i])...)
	}
	for i, arg := range values {
		col := ""
		if i < len(cols) {
			col = cols[i]
		}
		name := table
		if col != "" {
			name = table + "." + col
		}
		if name == "" {
			continue
		}
		realArg := arg
		if mi, ok := arg.(*ssa.MakeInterface); ok {
			realArg = mi.X
		}
		argID, err := ext.emitValue(realArg)
		if err != nil || argID == "" {
			continue
		}
		id := domain.CanonicalID(string(ext.funcID) + "#ext.sql." + name + "." + access + "@" + fmt.Sprintf("%d", line))
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
				"code_snippet":  sqlStr,
				"type_string":   "sql",
				"is_external":   "true",
				"func_id":       string(ext.funcID),
			},
		}}); err != nil {
			return err
		}
		if err := ext.emitEdgeKindLine(argID, id, domain.FactSummaryIO, line); err != nil {
			return err
		}
	}
	return nil
}

// applyScanOut 处理 Scan 写 out 实参（表关联链贯通）：
// row.Scan(&x) —— 接收者（row 值）→ 实参指向的局部变量节点
// （变量名 ID find#x，与 Load 归一节点一致）。数据流链：
// table_a.x.read → row → x → table_b.y.filter。
func (ext *fieldExtractor) applyScanOut(cc *ssa.CallCommon, calleeID domain.CanonicalID) error {
	logger := zap.L()
	logger.Debug("enter (fieldExtractor).applyScanOut")
	defer logger.Debug("exit (fieldExtractor).applyScanOut")
	if len(cc.Args) < 1 {
		return nil
	}
	recvID, err := ext.emitValue(cc.Args[0]) // 接收者（row 值）
	if err != nil || recvID == "" {
		return nil
	}
	line := ext.prog.Fset.PositionFor(cc.Pos(), false).Line
	// Scan 的 dest 是 variadic（...any）：实参打包为 Slice，须解包
	for i := 1; i < len(cc.Args); i++ {
		for _, arg := range variadicElems(cc.Args[i]) {
			// out 实参是 &x（alloc，经 MakeInterface 打包）：解包后
			// 用源码变量名 ID（与 Load 归一一致）
			real := arg
			if mi, ok := arg.(*ssa.MakeInterface); ok {
				real = mi.X
			}
			name := ext.instancePath(real)
			if isSSAName(name) {
				continue // 非局部变量（如 &s.Field）跳过
			}
			fid, ok := ext.funcIDOf(real)
			if !ok {
				continue
			}
			id := domain.CanonicalID(string(fid) + "#" + name)
			if err := ext.emit(domain.Item{Node: &domain.CodeEntity{
				ID:        id,
				Kind:      domain.KindSSAValue,
				Name:      name,
				FilePath:  ext.currentFile,
				LineStart: line,
				Properties: map[string]any{
					"origin_kind": "local",
					"ssa_op":      "scan_out",
					"type_string": arg.Type().String(),
					"func_id":     string(fid),
				},
			}}); err != nil {
				return err
			}
			if err := ext.emitEdgeKindLine(recvID, id, domain.FactDataFlowsTo, line); err != nil {
				return err
			}
		}
	}
	return nil
}

// applyTxBoundary 事务边界（Q97）：Begin/Commit/Rollback → 事务虚拟节点
// （Name=sql.tx.<boundary>），标注事务边界位置。
func (ext *fieldExtractor) applyTxBoundary(cc *ssa.CallCommon, calleeID domain.CanonicalID, boundary string) error {
	logger := zap.L()
	logger.Debug("enter (fieldExtractor).applyTxBoundary")
	defer logger.Debug("exit (fieldExtractor).applyTxBoundary")
	line := ext.prog.Fset.PositionFor(cc.Pos(), false).Line
	name := "sql.tx." + boundary
	id := domain.CanonicalID(string(ext.funcID) + "#ext." + name + "@" + fmt.Sprintf("%d", line))
	return ext.emit(domain.Item{Node: &domain.CodeEntity{
		ID:        id,
		Kind:      domain.KindFieldAccess,
		Name:      name,
		FilePath:  ext.currentFile,
		LineStart: line,
		Properties: map[string]any{
			"full_path":     name,
			"instance_path": name,
			"access_kind":   "write",
			"type_string":   "tx",
			"is_external":   "true",
			"func_id":       string(ext.funcID),
		},
	}})
}

// parseSQLStmt 从 SQL 语句提取表名、列名与 WHERE 过滤列（Q97 启发式，
// 不做完整 SQL 解析）：
//   INSERT INTO t(a, b) VALUES(?, ?)  → t, [a b], nil
//   UPDATE t SET a=?, b=?             → t, [a b], nil
//   DELETE FROM t                     → t, [], nil
//   SELECT a, b FROM t                → t, [a b], nil（P0-2 读路径）
//   SELECT * FROM t                   → t, [], nil（表级）
//   ... WHERE y = ?                   → ..., [], [y]（表关联：值实参按 ? 顺序映射）
func parseSQLStmt(sql string) (table string, cols []string, whereCols []string) {
	upper := strings.ToUpper(sql)
	rest := ""
	switch {
	case strings.Contains(upper, "INSERT INTO"):
		rest = sql[strings.Index(upper, "INSERT INTO")+len("INSERT INTO"):]
	case strings.Contains(upper, "UPDATE"):
		rest = sql[strings.Index(upper, "UPDATE")+len("UPDATE"):]
	case strings.Contains(upper, "DELETE FROM"):
		rest = sql[strings.Index(upper, "DELETE FROM")+len("DELETE FROM"):]
	case strings.Contains(upper, " FROM "):
		// SELECT ... FROM t：列在 SELECT 与 FROM 之间（P0-2）
		fromIdx := strings.Index(upper, " FROM ")
		rest = sql[fromIdx+len(" FROM "):]
		if strings.Contains(upper, "SELECT ") {
			selPart := strings.TrimSpace(sql[strings.Index(upper, "SELECT ")+len("SELECT "):fromIdx])
			if selPart != "" && !strings.Contains(strings.ToUpper(selPart), "*") {
				// 逗号分隔列（可能带表前缀 a.name、别名 a.name AS n——取末段）
				for _, c := range strings.Split(selPart, ",") {
					c = strings.TrimSpace(c)
					if i := strings.Index(c, " "); i >= 0 {
						c = c[:i] // 去掉 AS 别名
					}
					if i := strings.LastIndex(c, "."); i >= 0 {
						c = c[i+1:] // 去掉表前缀
					}
					c = strings.Trim(c, "`\"[]")
					if c != "" {
						cols = append(cols, c)
					}
				}
			}
		}
	default:
		return "", nil, nil
	}
	rest = strings.TrimSpace(rest)
	// 表名：到空白 / ( / 结束
	tableEnd := len(rest)
	for i := 0; i < len(rest); i++ {
		if rest[i] == '(' || rest[i] == ' ' || rest[i] == '\t' || rest[i] == '\n' || rest[i] == ';' {
			tableEnd = i
			break
		}
	}
	table = strings.TrimSpace(rest[:tableEnd])
	table = strings.Trim(table, "`\"[]")
	if table == "" {
		return "", nil, nil
	}
	// 列：INSERT 的 (a, b) 或 UPDATE 的 SET a=?
	after := strings.TrimSpace(rest[tableEnd:])
	if strings.HasPrefix(after, "(") {
		// INSERT INTO t(a, b)
		inner := after[1:]
		if i := strings.Index(inner, ")"); i >= 0 {
			inner = inner[:i]
		}
		for _, c := range strings.Split(inner, ",") {
			c = strings.TrimSpace(c)
			c = strings.Trim(c, "`\"[]")
			if c != "" {
				cols = append(cols, c)
			}
		}
	} else if strings.Contains(upper, " SET ") {
		// UPDATE t SET a=?, b=?
		up := strings.ToUpper(rest)
		if i := strings.Index(up, " SET "); i >= 0 {
			setPart := rest[i+len(" SET "):]
			if j := strings.Index(setPart, " WHERE"); j >= 0 {
				setPart = setPart[:j]
			}
			for _, c := range strings.Split(setPart, ",") {
				c = strings.TrimSpace(c)
				if k := strings.Index(c, "="); k >= 0 {
					c = strings.TrimSpace(c[:k])
					c = strings.Trim(c, "`\"[]")
					if c != "" {
						cols = append(cols, c)
					}
				}
			}
		}
	}
	// WHERE 过滤列（表关联分析）：`列 = ?` 按 ? 顺序提取（去表前缀）
	whereCols = extractWhereCols(rest)
	return table, cols, whereCols
}

var whereColRe = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_.]*)\s*=\s*\?`)

// extractWhereCols 从 SQL 语句剩余部分提取 WHERE 子句的过滤列
// （`列 = ?` 序列，值实参按 ? 顺序映射——表关联分析的数据基础）。
// 支持 a.y = ? 表前缀（去前缀）；WHERE 缺失返回 nil。
func extractWhereCols(rest string) []string {
	up := strings.ToUpper(rest)
	wi := strings.Index(up, " WHERE ")
	if wi < 0 {
		return nil
	}
	wherePart := rest[wi+len(" WHERE "):]
	upPart := strings.ToUpper(wherePart)
	for _, stop := range []string{" ORDER BY ", " LIMIT ", " GROUP BY ", " HAVING ", " UNION "} {
		if j := strings.Index(upPart, stop); j >= 0 {
			wherePart = wherePart[:j]
			break
		}
	}
	var out []string
	for _, m := range whereColRe.FindAllStringSubmatch(wherePart, -1) {
		c := m[1]
		if i := strings.LastIndex(c, "."); i >= 0 {
			c = c[i+1:] // 去表前缀（a.y → y）
		}
		c = strings.Trim(c, "`\"[]")
		if c != "" {
			out = append(out, c)
		}
	}
	return out
}

// derefSlice 解切片（*[]Session → Session；GORM 读对象形态）。
func derefSlice(t types.Type) types.Type {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	if sl, ok := t.(*types.Slice); ok {
		t = sl.Elem()
	}
	return t
}

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
	fmt.Fprintf(os.Stderr, "ORMRead: arg=%T type=%s named=%v\n", realArg, t.String(), ok)
	if !ok {
		return nil // 非对象读（如 Count(&n) 无字段展开）
	}
	// 表名：Model(&X{}) 链式溯源优先，否则对象类型名
	table := ""
	if scope := chainScopeObject(cc.Args[0]); scope != nil {
		table = snakeCase(scope.Obj().Name())
	} else {
		table = snakeCase(named.Obj().Name())
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
		// 读边：读出值 → 对象（与写方向相反，值流入对象字段）
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
func (ext *fieldExtractor) applyORMWrite(cc *ssa.CallCommon, calleeID domain.CanonicalID) error {
	logger := zap.L()
	logger.Debug("enter (fieldExtractor).applyORMWrite")
	defer logger.Debug("exit (fieldExtractor).applyORMWrite")
	if len(cc.Args) < 2 {
		return nil
	}
	arg := cc.Args[1] // 对象实参（args[0] 为接收者 db）
	realArg := arg
	if mi, ok := arg.(*ssa.MakeInterface); ok {
		realArg = mi.X
	}
	t := derefType(realArg.Type())
	named, ok := t.(*types.Named)
	if !ok {
		// ⑦ 字符串列名形态：Update("col", v) / Where("col = ?", v)
		if c, isConst := realArg.(*ssa.Const); isConst && c.Value != nil &&
			constant.StringVal(c.Value) != "" && len(cc.Args) >= 3 {
			scope := chainScopeObject(cc.Args[0])
			if scope == nil {
				return nil
			}
			table := snakeCase(scope.Obj().Name())
			col := constant.StringVal(c.Value)
			// Where("session_id = ?", v) 条件串：剥离 " = ?" 后缀取列名
			if i := strings.Index(col, " = "); i >= 0 {
				col = col[:i]
			}
			// Where 过滤列标 filter（表关联键：值 → 过滤列）
			access := "write"
			if strings.HasSuffix(string(calleeID), ".Where") {
				access = "filter"
			}
			return ext.emitORMColumn(cc, calleeID, table, col, cc.Args[2], access)
		}
		return nil
	}
	st, ok := named.Underlying().(*types.Struct)
	if !ok {
		return nil
	}
	table := snakeCase(named.Obj().Name())
	line := ext.prog.Fset.PositionFor(cc.Pos(), false).Line
	// 对象值节点（兜底连边用；emitValue 幂等去重）
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
		// 字段值 → 虚拟节点（summary_io）；字段值不可定位时连对象值
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

// chainScopeObject 溯源链式调用的范围对象（⑦）：Update/Updates 的 receiver
// 沿定义链回溯中间调用（Where/Model 等），找到实参为结构体对象的调用
// （如 Model(&Session{ID:...})）返回其类型。链上游无结构体实参返回 nil。
func chainScopeObject(recv ssa.Value) *types.Named {
	c, ok := recv.(*ssa.Call)
	if !ok {
		return nil
	}
	for i := 1; i < len(c.Call.Args); i++ {
		arg := c.Call.Args[i]
		if mi, isMI := arg.(*ssa.MakeInterface); isMI {
			arg = mi.X
		}
		if named := namedStructOf(derefType(arg.Type())); named != nil {
			return named
		}
	}
	if len(c.Call.Args) > 0 {
		return chainScopeObject(c.Call.Args[0])
	}
	return nil
}

// fieldValueOf 按字段索引取对象值的字段读取（对象为 Alloc/寄存器时经
// FieldAddr 或 Field 指令；无法定位时返回 nil——字段值无 SSA 实体则
// 跳过该列）。
func fieldValueOf(obj ssa.Value, idx int) ssa.Value {
	refs := obj.Referrers()
	if refs == nil {
		return nil
	}
	for _, ref := range *refs {
		switch r := ref.(type) {
		case *ssa.FieldAddr:
			if r.Field == idx {
				// 写路径：Store 到该字段的值（Create 前对象字段填充）
				if r.Referrers() != nil {
					for _, ref2 := range *r.Referrers() {
						if st, ok := ref2.(*ssa.Store); ok && st.Addr == r {
							return st.Val
						}
					}
				}
				return nil
			}
		case *ssa.Field:
			if r.Field == idx {
				return r
			}
		}
	}
	return nil
}


// derefType 解指针。
func derefType(t types.Type) types.Type {
	if p, ok := t.(*types.Pointer); ok {
		return p.Elem()
	}
	return t
}

// commonInitialisms 常见缩写表（golang/lint 同款，GORM 默认命名用）——
// 先 Title 化再转小写，保证 SessionID → session_id、SourceURL → source_url。
var commonInitialisms = []string{"API", "ASCII", "CPU", "CSS", "DNS", "EOF", "GUID", "HTML", "HTTP", "HTTPS", "ID", "IP", "JSON", "LHS", "QPS", "RAM", "RHS", "RPC", "SLA", "SMTP", "SSH", "TLS", "TTL", "UID", "UI", "UUID", "URI", "URL", "UTF8", "VM", "XML", "XSRF", "XSS"}

// snakeCase 类型/字段名 → 表/列名，与 GORM 默认命名完全一致（移植
// gorm NamingStrategy.toDBName：常见缩写 Title 化 + 大小写扫描——连续
// 大写不拆，转小写前插线）。UserProfile → user_profile、SessionID →
// session_id、SourceURL → source_url、APIKey → apikey、
// SQLiteKnowledgeGraph → sq_lite_knowledge_graph（SQL 不在缩写表，
// 与 GORM 默认一致；radar 用 TableName() 定制表名时无法静态推导）。
func snakeCase(s string) string {
	value := s
	for _, in := range commonInitialisms {
		value = strings.ReplaceAll(value, in, in[:1]+strings.ToLower(in[1:]))
	}
	if value == "" {
		return ""
	}
	var sb strings.Builder
	lastCase := false
	curCase := value[0] >= 'A' && value[0] <= 'Z'
	for i := 0; i < len(value)-1; i++ {
		v := value[i]
		nextCase := value[i+1] >= 'A' && value[i+1] <= 'Z'
		nextNumber := value[i+1] >= '0' && value[i+1] <= '9'
		if curCase {
			if lastCase && (nextCase || nextNumber) {
				// 连续大写（缩写中间）：不插线
				sb.WriteByte(v + ('a' - 'A'))
			} else {
				if i > 0 && value[i-1] != '_' && value[i+1] != '_' {
					sb.WriteByte('_')
				}
				sb.WriteByte(v + ('a' - 'A'))
			}
		} else {
			sb.WriteByte(v)
		}
		lastCase = curCase
		curCase = nextCase
	}
	if curCase {
		if !lastCase && len(value) > 1 {
			sb.WriteByte('_')
		}
		sb.WriteByte(value[len(value)-1] + ('a' - 'A'))
	} else {
		sb.WriteByte(value[len(value)-1])
	}
	return sb.String()
}
