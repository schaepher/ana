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
	"go/types"
	"os"
	"path/filepath"
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
}

// userSummaryFile 对应 field-summary.yaml。
type userSummaryFile struct {
	Summaries []struct {
		Func       string   `yaml:"func"`
		Reads      []string `yaml:"reads"`
		Writes     []string `yaml:"writes"`
		ParamIndex int      `yaml:"param_index"`
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
		}
	}
	return specs, warnings
}

// builtinSummaries 内置摘要（field_trace.md §7.3）。
// context.Context 为透明传递，无条目。
func builtinSummaries() map[string]summarySpec {
	return map[string]summarySpec{
		"encoding/json.Unmarshal": {
			Func: "encoding/json.Unmarshal", ParamIndex: 1,
			WritesAll: true, // 写入 v 的所有字段（递归）
		},
		"fmt.Printf": {
			Func: "fmt.Printf", ParamIndex: 1,
			ReadArgsAll: true, // 读取所有 args 的字段（保守策略）
		},
		"database/sql.(Rows).Scan": {
			Func: "database/sql.(Rows).Scan", ParamIndex: 0,
			WritesAll: true, // 写入 dest 的指向值（对每个 dest 参数展开）
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
func (ext *fieldExtractor) applySummary(cc *ssa.CallCommon, callee *ssa.Function) (bool, error) {
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
		// 相对字段路径（如 "Form"）补全为类型限定路径
		if !strings.Contains(f, ".") || !strings.Contains(f, "/") {
			if named := namedStructOf(realArg.Type()); named != nil {
				fullPath = named.Obj().Pkg().Path() + "." + named.Obj().Name() + "." + f
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
