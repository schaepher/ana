// 字段提取器（field_trace.md §6.1）：遍历 SSA 指令，生成 field_access 节点、
// 参与字段访问的 ssa_value 节点与 data_flows_to 边。
//
// 映射规则（注意 x/tools v0.26 的 go/ssa 表示：字段读也经 FieldAddr 取址后
// 由 UnOp(MUL) 解引用，Field 指令仅出现在非可寻址值上。故 FieldAddr 的
// 读写由"使用方式"判定，与经典 Field/FieldAddr/Store 三指令映射等价）：
//   - FieldAddr 且被 Store 使用 → field_access（write），边：基地址 → 字段节点
//   - FieldAddr 且被 UnOp(MUL) 解引用 → field_access（read），边：字段节点 → 解引用结果
//   - 两者同时（x.a = x.a + 1）→ read/write 两个独立节点
//   - Field（经典读指令，非可寻址值）→ field_access（read）
//   - Store（写入 FieldAddr）→ 不建节点，边：写入值 → 字段节点
//   - FieldAddr 无读写用途（如 &x.a 传参）→ 按文档默认为 write
//
// ssa_value 仅保留参与字段访问的值（Q73），避免全程序 SSA 节点爆炸。
package ssa

import (
	"go/token"
	"go/types"

	"github.com/schaepher/codeintel/internal/domain"
	"go.uber.org/zap"
	"golang.org/x/tools/go/ssa"
)

// emitFunctionFields 发射单个函数内的字段访问节点与数据流边。

// faUses 扫描 FieldAddr/IndexAddr 的使用方式：是否被 Store 写入、
// 是否被 UnOp(MUL) 解引用读、是否作为调用实参（取址传参）流出。

// callArg 判断指令调用实参（含接收者）是否包含指定值。

// fieldAddrUse 判定 FieldAddr 的最终读写用途，内层 FieldAddr（仅被其他
// FieldAddr 作为取址中间层引用）的用途从外层递归传播：
//   - m.cfg.APIKey 读 → 内层 m.cfg 也是读（而非"无用途默认 write"）
//   - x.a.b = v 写 → 内层 x.a 也是写
//   - x.a = x.a + 1 → 内层同时 read+write

// fieldInfo 是字段访问的静态信息（类型限定路径等，与具体基值无关）。

// fieldAccess 是单个字段访问的构建期表示。
type fieldAccess struct {
	id       domain.CanonicalID
	access   string // read / write
	instance string // 变量访问链（如 req.Amount）
	info     fieldInfo
	ext      *fieldExtractor
}

// emit 输出 field_access 节点。
func (fa *fieldAccess) emit() error {
	logger := zap.L()
	logger.Debug("enter (fieldAccess).emit")
	defer logger.Debug("exit (fieldAccess).emit")
	n := &domain.CodeEntity{
		ID:        fa.id,
		Kind:      domain.KindFieldAccess,
		Name:      fa.instance, // 展示用（access_kind 在 properties）
		FilePath:  fa.info.filePath,
		LineStart: fa.info.line,
		LineEnd:   fa.info.line,
		Properties: map[string]any{
			"full_path":     fa.info.fullPath,
			"instance_path": fa.instance,
			"access_kind":   fa.access,
			"code_snippet":  fa.info.snippet,
			"type_string":   fa.info.typeString,
			"func_id":       string(fa.ext.funcID),
		},
	}
	return fa.ext.emit(domain.Item{Node: n})
}

// newFieldAccess 创建 FieldAddr 对应的字段节点（access 由使用方式判定：write/read）。

// recordEntry 记录 direct 读/写条目（function_field_summary 预计算用）。

// newFieldAccessValue 创建 Field 读对应的字段节点。

// fieldInfo 解析字段访问的静态信息：full_path（类型限定路径）、类型、位置。
// 静态类型不是具名结构体（匿名 struct/接口等）时返回 ok=false（Q15 限定）。

// sourceLine 读取仓库文件指定行的源码（去掉缩进，供 code_snippet 展示）。
// 文件内容按路径缓存，避免每个字段访问重复读盘。

// accessID 生成字段访问节点的 canonical ID：symbol:go:<pkg>:<func>#<instance>.<access>@<line>。
// 同一字段路径在同一函数多处访问时用行号消歧；复合读写（read/write 同位置）用
// access 消歧——各自独立节点（field_trace.md 4.1，Q68）。

// emitFlow 发射 字段节点 → ssa_value 的 data_flows_to 边（Field 读）。

// emitFlowValue 发射 ssa_value → 字段节点 的 data_flows_to 边（FieldAddr 基地址 / Store 写入值）。

// emitEdge 发射 data_flows_to 边（tool_source=ssa，conf 1.0，Q69）。

// emitValue 发射（并去重）参与字段访问或跨过程数据流的 ssa_value 节点（Q73）。
// 节点命名空间按值所属函数（funcIDOf）：跨函数（实参/形参/返回值）落在各自
// 函数的 canonical ID 下。slot = SSA 名；同名冲突（shadowing）附加 @行号 消歧。
// 值不属于可标识函数（闭包等）时返回空 ID，调用方跳过相关边。

// funcIDOf 返回值所属函数的 canonical ID（缓存）。
// 闭包（Object 非 types.Func）或无法归属的值返回 ok=false。

// funcIDOfFn 解析具名函数的 canonical ID（不落缓存，幂等）。

// instancePath 生成变量访问链（如 req.Amount 或 a.b.c），深度上限 8 防环。
// go/ssa v0.26 的 Alloc 名为 tN，源码变量名从标识符索引反查（buildIdentIndex）。
func (ext *fieldExtractor) instancePath(v ssa.Value) string {
	return ext.instancePathDepth(v, 0)
}

func (ext *fieldExtractor) instancePathDepth(v ssa.Value, depth int) string {
	if depth > 8 {
		return v.Name()
	}
	switch x := v.(type) {
	case *ssa.FieldAddr:
		if fn := fieldNameOf(x.X.Type(), x.Field); fn != "" {
			return ext.instancePathDepth(x.X, depth+1) + "." + fn
		}
		return ext.instancePathDepth(x.X, depth+1)
	case *ssa.Field:
		if fn := fieldNameOf(x.X.Type(), x.Field); fn != "" {
			return ext.instancePathDepth(x.X, depth+1) + "." + fn
		}
		return ext.instancePathDepth(x.X, depth+1)
	case *ssa.UnOp:
		if x.Op == token.MUL { // 解引用：与 Alloc 连成变量名
			return ext.instancePathDepth(x.X, depth+1)
		}
	case *ssa.Alloc:
		if name, ok := ext.idents[x.Pos()]; ok {
			return name
		}
	}
	return v.Name()
}

// fieldNameOf 取类型第 idx 个字段名（derefStruct 失败返回空）。

// derefStruct 解指针/取底层结构体；返回具名类型（可空）与结构体（可空）。
// 匿名 struct 类型：named 为 nil、st 可用（full_path 回退场景，§6.1）。

// originKind 区分 SSA 值来源（field_trace.md §4.1）。

// ssaOp 返回 SSA 指令类型名（如 field / fieldaddr / store）。

// fieldExtractor 是单个函数的字段提取状态。
type fieldExtractor struct {
	repo          *domain.Repository
	prog          *ssa.Program
	pkgs          []*types.Package // ⑮ 接口动态派发候选枚举用
	fn            *ssa.Function
	funcID        domain.CanonicalID
	idents        map[token.Pos]string // 源码标识符索引（Alloc 反查变量名）
	assignTargets []assignTarget       // 赋值表达式区间（按 start 排序）→ 目标变量名（MakeMap/MakeSlice 恢复）
	emit          domain.EmitFunc

	fields        map[*ssa.FieldAddr]*fieldAccess        // FieldAddr → write 节点（Store 解析目标）
	reads         map[*ssa.FieldAddr]*fieldAccess        // FieldAddr → read 节点（UnOp 解引用）
	indexes       map[*ssa.IndexAddr]*fieldAccess        // IndexAddr → write 节点（slice 元素）
	indexReads    map[*ssa.IndexAddr]*fieldAccess        // IndexAddr → read 节点
	values        map[ssa.Value]domain.CanonicalID       // 已发射的 ssa_value
	funcIDs       map[*ssa.Function]domain.CanonicalID   // 函数 → canonical ID 缓存
	slotsFor      map[domain.CanonicalID]map[string]bool // 每函数 slot 占用（shadowing 消歧）
	rets          map[*ssa.Function][][]ssa.Value        // 被调函数 Return 指令缓存（returns 边复用）
	lines         map[string][]string                    // 源码行缓存（filePath → 行数组）
	funcData      *funcData                              // 摘要收集（direct 读写 + 静态调用）
	specs         map[string]summarySpec                 // 外部函数摘要（内置 + 用户）
	extSummaries  map[domain.CanonicalID]bool            // 已创建 external_summary 节点
	currentFile   string                                 // 当前函数文件（虚拟节点用）
	fallbackCount int                                    // 静态类型解析失败回退数（警告汇总）
	dispatchRegs  dispatchReg                            // 接口注册点缓存（Q161 动态边候选元数据，一次扫描）
	regHits       map[string]map[string]bool             // Q168：iface.String() → candidateKey → register 命中（O(1) 判定）
	chainTables   map[ssa.Value]string                   // Q175：XORM 链式表名（Table 调用返回值 → 表名）
}

// isSSAName 判断是否为 SSA 临时名（t0、t91 等），用于决定展示名回退。

// newElementAccess 创建容器元素访问节点（map/slice/array 元素，Q83）。
// key 为 nil 表示 Range 迭代（[*]）。

// elementPath 生成元素访问的 full_path / instance_path（Q1/Q5）：
//   常量字符串 key → m["a"]；常量 int 索引 → s[0]；变量 key → [key]；Range → [*]
//   full_path 基：字段路径（容器是结构体字段）> named 容器类型 > 回退 instance

// containerInstance 容器实例路径（lifting 后 MakeMap/MakeSlice 寄存器
// 从赋值目标恢复变量名）。

// containerFullPath 容器类型限定路径（字段路径 > named 容器类型 > 空回退）。

// elementMark 生成元素记号（Q5）："a" / 0 / [key] / [*]。

// namedContainerOf 取 named map/slice/array 类型的限定路径（pkg.M）；非 named 返回空。

// isMapLike / isSliceLike / isChanLike 容器类型判定（含 named 与字面类型）。

// lookupAssignTarget 区间匹配赋值目标（MakeMap.Pos 落在字面量内部）。
// 切片按 start 排序：二分找最后一个 start <= pos 的区间，检查 end 覆盖。
// 嵌套赋值（f(x := 1)）内层 start 更大，二分自然命中内层区间。

// emitGlobalInit 全局变量初始化溯源（Q98）：遍历模块内全部函数（含隐式
// init——init 无 FuncDecl，emitFunction 不处理）的 Store→Global 指令，
// 发 data_flows_to 边（写入值 → Global 节点）。注意：go/ssa v0.26 的
// Global 无 Init 字段，纯常量标量初始化（var G = 5）不产生 Store 指令、
// 无初始化边（S4：注释曾声称"常量初始化同样发边"，实际不存在该路径）；
// var G = T{...} 结构体初始化是字段级 Store（&G.A），经 FieldAddr 分支
// 处理。Global 节点跨函数共享（symbol:go:<pkg>:var.<name>），value-trace
// 从使用处反向可达初始化表达式。
