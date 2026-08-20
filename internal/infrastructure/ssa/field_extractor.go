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
	// Q179：叶子为临时寄存器（tN）时恢复源码变量名——字段实例路径
	// t0.A → t.A（u := f() 后 u.A 的 FieldAddr.X 是 lifting 寄存器 t0）
	return ext.recoverVarName(v)
}

// recoverVarName 临时寄存器（tN）→ 源码变量名：lifting 后变量提升为
// 寄存器、变量名在 IR 中丢失；tN 的 Pos 精确指向其定义表达式
// （u := f() 的 f()），assignTargets 区间匹配 RHS → 目标变量 u。
// Q193：仅当 tN 的 Pos == RHS 起始（非调用表达式）或 == RHS 直接调用的
// '(' 位置（go/ssa 的 Call.Pos 语义）才恢复——嵌套子表达式
// （err := g(f()) 中的 f()）的 Pos 不匹配，不恢复，避免误配外层目标 err
// （此前区间匹配曾把 db.DB() 的返回值误配为外层 err 变量）。
// 非临时名 / 无 Pos（phi、合成值）→ 原样返回。
func (ext *fieldExtractor) recoverVarName(v ssa.Value) string {
	name := v.Name()
	if !isSSAName(name) {
		return name
	}
	orig, start, callPos := ext.lookupAssignTargetStart(v.Pos())
	if orig != "" && !isSSAName(orig) {
		p := v.Pos()
		if p == start || (callPos != 0 && p == callPos) {
			return orig
		}
	}
	return name
}

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
	// Q223：本次处理的函数签名节点是否已发射（仅顶层函数 FuncDecl 调
	// emitSignatureNodes）。闭包（FuncLit）不发射签名节点——emitValue
	// 的 Parameter 分支据此前置「节点已发射」直接返回缓存 ID；闭包参数
	// 无对应签名节点，须自行发射，否则返回未落库 ID → 边端点缺失
	// （Q222 同款漏报）
	sigEmitted bool

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
	tableNames    map[*types.Named]string                // Q205：tableNameOf 结果缓存（无 spec 接口调用兜底高频触发）
	typeMapping   map[*types.Named]string                // Q211：orm.Mapping 实体类型→表名（Index 级收集共享）
}
