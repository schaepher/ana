package ssa

import (
	"go/token"
	"sort"
	"strings"

	"github.com/schaepher/codeintel/internal/domain"
	"go.uber.org/zap"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

// lookupAssignTarget 区间匹配赋值目标（MakeMap.Pos 落在字面量内部）。
// 切片按 start 排序：二分找最后一个 start <= pos 的区间，检查 end 覆盖。
// 嵌套赋值（f(x := 1)）内层 start 更大，二分自然命中内层区间。
func (ext *fieldExtractor) lookupAssignTarget(pos token.Pos) string {
	name, _, _ := ext.lookupAssignTargetStart(pos)
	return name
}

// lookupAssignTargetStart 同 lookupAssignTarget，额外返回命中区间的 start
// （Q193：Q179 变量名恢复校验"值定义位置 == 赋值 RHS 起始"——嵌套子
// 表达式（err := g(f()) 中的 f()）不恢复，避免误配外层目标 err）。
func (ext *fieldExtractor) lookupAssignTargetStart(pos token.Pos) (string, token.Pos, token.Pos) {
	i := sort.Search(len(ext.assignTargets), func(i int) bool {
		return ext.assignTargets[i].start > pos
	}) - 1
	if i < 0 || pos > ext.assignTargets[i].end {
		return "", 0, 0
	}
	return ext.assignTargets[i].name, ext.assignTargets[i].start, ext.assignTargets[i].topCallPos
}

// emitGlobalInit 全局变量初始化溯源（Q98）：遍历模块内全部函数（含隐式
// init——init 无 FuncDecl，emitFunction 不处理）的 Store→Global 指令，
// 发 data_flows_to 边（写入值 → Global 节点）。注意：go/ssa v0.26 的
// Global 无 Init 字段，纯常量标量初始化（var G = 5）不产生 Store 指令、
// 无初始化边（S4：注释曾声称"常量初始化同样发边"，实际不存在该路径）；
// var G = T{...} 结构体初始化是字段级 Store（&G.A），经 FieldAddr 分支
// 处理。Global 节点跨函数共享（symbol:go:<pkg>:var.<name>），value-trace
// 从使用处反向可达初始化表达式。
func emitGlobalInit(repo *domain.Repository, prog *ssa.Program, emit domain.EmitFunc) error {
	logger := zap.L()
	logger.Debug("enter emitGlobalInit")
	defer logger.Debug("exit emitGlobalInit")
	ext := &fieldExtractor{
		repo:     repo,
		prog:     prog,
		emit:     emit,
		values:   map[ssa.Value]domain.CanonicalID{},
		funcIDs:  map[*ssa.Function]domain.CanonicalID{},
		slotsFor: map[domain.CanonicalID]map[string]bool{},
	}

	for fn := range ssautil.AllFunctions(prog) {
		if !isModuleFunction(fn, repo.Modules) {
			continue
		}

		ext.funcID = domain.CanonicalID("symbol:go:" + fn.Pkg.Pkg.Path() + ":init")
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				st, ok := instr.(*ssa.Store)
				if !ok {
					continue
				}
				var g *ssa.Global
				if gg, ok := st.Addr.(*ssa.Global); ok {
					g = gg
				} else if fa, ok := st.Addr.(*ssa.FieldAddr); ok {

					if gg, ok2 := fa.X.(*ssa.Global); ok2 {
						g = gg
					}
				}
				if g == nil || strings.Contains(g.Name(), "$") {
					continue
				}
				gID, err := ext.emitValue(g)
				if err != nil || gID == "" {
					continue
				}
				if err := ext.emitFlowValue(st.Val, gID); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
