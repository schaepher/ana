package action

import (
	"strings"

	"github.com/schaepher/codeintel/internal/domain"
	"go.uber.org/zap"
)

// downstreamTrampoline 写锚点下游跳板（③⑤⑧）：写节点无出边——经同
// full_path 读节点接入使用链（读节点行 + dir=1 子链行）；非写锚点返回 nil。
// 读锚点的下游用单次合并 CTE 查询（GetValueTraceMulti），替代逐读节点
// 递归——读点多时累计成本大幅下降（⑧ 超时防护）。
func (a *Actions) downstreamTrampoline(anchor domain.CanonicalID) ([]*domain.TraceRow, error) {
	n, err := a.repo.GetSymbol(anchor)
	if err != nil || n.Kind != domain.KindFieldAccess || n.Property("access_kind") != "write" {
		return nil, nil
	}
	fullPath := n.Property("full_path")
	if fullPath == "" {
		return nil, nil
	}
	reads, err := a.repo.FindFieldReads(fullPath)
	if err != nil {
		return nil, nil
	}
	anchors := make([]domain.CanonicalID, 0, len(reads))
	var out []*domain.TraceRow
	for _, rn := range reads {
		if rn.ID == anchor {
			continue
		}
		anchors = append(anchors, rn.ID)

		out = append(out, &domain.TraceRow{
			ID: rn.ID, Name: rn.Name, Kind: rn.Kind, Access: "read",
			Line: rn.LineStart, Dir: 1,
			FuncID: rn.Property("func_id"),
		})
		if len(anchors) >= trampolineMaxReads {
			break
		}
	}

	sub, err := a.repo.GetValueTraceMulti(anchors, fullPath, trampolineDepth)
	if err == nil {
		out = append(out, sub...)
	}
	return out, nil
}

// Lifecycle 生命周期图行（⑤）：value-trace 全链 + 写锚点的下游跳板
// （同字段读节点的使用链），行按 ID 去重（首个保留）。供
// export graph --type lifecycle 与前端展示使用。
func (a *Actions) Lifecycle(id domain.CanonicalID) ([]*domain.TraceRow, error) {
	logger := zap.L()
	logger.Info("enter (Actions).Lifecycle", zap.String("id", string(id)))
	defer logger.Info("exit (Actions).Lifecycle")
	rows, err := a.repo.GetValueTrace(id, 8, 0, false)
	if err != nil {
		return nil, err
	}
	extra, err := a.downstreamTrampoline(id)
	if err != nil {
		return nil, err
	}
	seen := map[domain.CanonicalID]bool{}
	out := make([]*domain.TraceRow, 0, len(rows)+len(extra))
	for _, r := range append(rows, extra...) {
		if seen[r.ID] {
			continue
		}
		seen[r.ID] = true
		out = append(out, r)
	}
	return out, nil
}

// Trace 字段产生点反向追溯 / 后续使用正向追踪；返回解析后的函数符号
// 与追溯路径（符号供展示层输出函数名）。
func (a *Actions) Trace(p TraceParams) (*domain.CodeEntity, []*domain.TraceRow, error) {
	logger := zap.L()
	logger.Info("enter (Actions).Trace", zap.Any("params", p))
	defer logger.Info("exit (Actions).Trace")
	n, err := a.ResolveSymbol(p.Func)
	if err != nil {
		return nil, nil, err
	}
	if p.Forward {
		rows, err := a.repo.TraceForward(p.Field, n.ID, p.MaxDepth)
		return n, rows, err
	}
	if p.FollowIndirect {
		rows, err := a.repo.TraceBackwardIndirect(p.Field, n.ID, p.MaxDepth)
		return n, rows, err
	}
	rows, err := a.repo.TraceBackward(p.Field, n.ID, p.MaxDepth)
	return n, rows, err
}

// ValueTrace 数据值全链追踪（field_trace.md §14.2）。

func (a *Actions) ValueTrace(nodeID domain.CanonicalID, maxDepth int, minConf float64, includeContainer bool) ([]*domain.TraceRow, error) {
	logger := zap.L()
	logger.Info("enter (Actions).ValueTrace", zap.String("node_id", string(nodeID)), zap.Int("max_depth", maxDepth), zap.Float64("min_conf", minConf), zap.Bool("include_container", includeContainer))
	defer logger.Info("exit (Actions).ValueTrace")
	rows, err := a.repo.GetValueTrace(nodeID, maxDepth, minConf, includeContainer)
	if err != nil {
		return nil, err
	}
	return a.markDispatchCandidates(rows)
}

// Flows 返回函数内完整字段数据流（前端 /api/flows 用）。
func (a *Actions) Flows(funcID domain.CanonicalID, maxDepth int) ([]*domain.TraceRow, error) {
	logger := zap.L()
	logger.Info("enter (Actions).Flows", zap.String("func_id", string(funcID)), zap.Int("max_depth", maxDepth))
	defer logger.Info("exit (Actions).Flows")
	return a.repo.GetFunctionFlows(funcID, maxDepth)
}

// shortFuncNameX 从 canonical ID 取函数短名（action 层展示用）。
func shortFuncNameX(id string) string {
	if i := strings.LastIndex(id, ":"); i >= 0 {
		return id[i+1:]
	}
	return id
}

// Path 节点间最短路径（field_trace.md §17.3）：两端经 ResolveAnchor
// 解析（canonical ID / 符号名 / 字段路径）；viaCalls=true 用函数调用
// 边集，否则数据流边集。不可达返回空切片。
func (a *Actions) Path(from, to string, maxDepth int, viaCalls bool) ([]*domain.TraceRow, error) {
	logger := zap.L()
	logger.Info("enter (Actions).Path", zap.String("from", from), zap.String("to", to), zap.Int("max_depth", maxDepth), zap.Bool("via_calls", viaCalls))
	defer logger.Info("exit (Actions).Path")
	fID, err := a.ResolveAnchor(from)
	if err != nil {
		return nil, err
	}
	tID, err := a.ResolveAnchor(to)
	if err != nil {
		return nil, err
	}
	return a.repo.GetPath(fID, tID, maxDepth, viaCalls)
}
