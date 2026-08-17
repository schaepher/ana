package sqlite

import (
	"database/sql"
	"strings"

	"github.com/schaepher/codeintel/internal/domain"
	"go.uber.org/zap"
)

// GetUncalledFunctions 返回全部函数/方法（除 main/init）的调用状态
// （field_trace.md §16.2）：
//   - Called：有 calls / passes_result 入边（被调用，含嵌套调用）
//   - Referenced：有 passes_to（回调参数）/ dispatch_to（接口派发）/
//     initializes（被实例化）/ var 初始化引用（data_flows_to → var.Global）
//
// 返回全部函数，调用方按 Called/Referenced 过滤展示两档报告。
func (r *Repo) GetUncalledFunctions() ([]*domain.UnusedFunc, error) {
	logger := zap.L()
	logger.Debug("enter (Repo).GetUncalledFunctions")
	defer logger.Debug("exit (Repo).GetUncalledFunctions")

	called, err := r.edgeTargetKinds("calls", "passes_result")
	if err != nil {
		return nil, err
	}
	refed, err := r.edgeTargetKinds("passes_to", "dispatch_to", "initializes")
	if err != nil {
		return nil, err
	}
	varInit, err := r.varInitFuncs()
	if err != nil {
		return nil, err
	}
	rows, err := r.Query(`SELECT n.id, n.kind, n.name, n.file_path, n.line_start, n.line_end
	FROM nodes n
	WHERE n.kind IN ('function','method')
	  AND n.name NOT IN ('main','init')
	  AND n.file_path IS NOT NULL
	  AND n.file_path NOT LIKE '%_test.go'
	ORDER BY n.file_path, n.line_start`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.UnusedFunc
	for rows.Next() {
		var (
			u        domain.UnusedFunc
			id, kind string
			file     string
		)
		if err := rows.Scan(&id, &kind, &u.Name, &file, &u.LineStart, &u.LineEnd); err != nil {
			return nil, err
		}
		u.ID = domain.CanonicalID(id)
		u.Kind = domain.EntityKind(kind)
		u.FilePath = file
		u.Called = called[u.ID]
		u.Referenced = refed[u.ID] || varInit[u.ID]
		u.Exported = len(u.Name) > 0 && u.Name[0] >= 'A' && u.Name[0] <= 'Z'
		out = append(out, &u)
	}
	return out, rows.Err()
}

// edgeTargetKinds 一次查询返回指定 kind 边的全部 target_id 集合
// （unused 预聚合，替代逐行 EXISTS 子查询）。
func (r *Repo) edgeTargetKinds(kinds ...string) (map[domain.CanonicalID]bool, error) {
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(kinds)), ",")
	args := make([]any, len(kinds))
	for i, k := range kinds {
		args[i] = k
	}
	rows, err := r.Query(`SELECT target_id FROM edges WHERE kind IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[domain.CanonicalID]bool{}
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out[domain.CanonicalID(t)] = true
	}
	return out, rows.Err()
}

// varInitFuncs 返回被全局变量初始化表达式引用的函数集合（data_flows_to
// → var.* 节点，source 的 func_id——Q108：包初始化调用的函数不算孤立）。
func (r *Repo) varInitFuncs() (map[domain.CanonicalID]bool, error) {
	rows, err := r.Query(`SELECT DISTINCT json_extract(s.properties, '$.func_id')
	FROM edges e JOIN nodes s ON s.id = e.source_id
	WHERE e.kind = 'data_flows_to' AND e.target_id LIKE 'symbol:go:%:var.%'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[domain.CanonicalID]bool{}
	for rows.Next() {
		var fid sql.NullString
		if err := rows.Scan(&fid); err != nil {
			return nil, err
		}
		if fid.Valid && fid.String != "" {
			out[domain.CanonicalID(fid.String)] = true
		}
	}
	return out, rows.Err()
}

// GetIsolatedChains 返回孤立调用链（field_trace.md §16.3）：
// 链头 = 无 caller 的函数（非 main/init）；沿 callee 递归；
// 遇有链外 caller 的节点断开（该节点及下游不入链）；
// 互调环（无外部 caller）整环孤立。单节点（无 callee）自成链。
// main/init 参与构图（其调用使被调函数不算孤立），但永不作为链头或链成员。
func (r *Repo) GetIsolatedChains() ([][]*domain.UnusedFunc, error) {
	logger := zap.L()
	logger.Debug("enter (Repo).GetIsolatedChains")
	defer logger.Debug("exit (Repo).GetIsolatedChains")

	refed, err := r.edgeTargetKinds("passes_to", "dispatch_to", "initializes")
	if err != nil {
		return nil, err
	}
	varInit, err := r.varInitFuncs()
	if err != nil {
		return nil, err
	}
	nodeRows, err := r.Query(`SELECT n.id, n.kind, n.name, n.file_path, n.line_start, n.line_end
	FROM nodes n
	WHERE n.kind IN ('function','method')
	  AND n.file_path IS NOT NULL AND n.file_path NOT LIKE '%_test.go'`)
	if err != nil {
		return nil, err
	}
	defer nodeRows.Close()
	info := map[domain.CanonicalID]*domain.UnusedFunc{}
	var order []domain.CanonicalID
	for nodeRows.Next() {
		var (
			u        domain.UnusedFunc
			id, kind string
			file     string
		)
		if err := nodeRows.Scan(&id, &kind, &u.Name, &file, &u.LineStart, &u.LineEnd); err != nil {
			return nil, err
		}
		u.Referenced = refed[domain.CanonicalID(id)] || varInit[domain.CanonicalID(id)]
		u.ID = domain.CanonicalID(id)
		u.Kind = domain.EntityKind(kind)
		u.FilePath = file
		u.Exported = len(u.Name) > 0 && u.Name[0] >= 'A' && u.Name[0] <= 'Z'
		info[u.ID] = &u
		order = append(order, u.ID)
	}
	if err := nodeRows.Err(); err != nil {
		return nil, err
	}

	callers := map[domain.CanonicalID][]domain.CanonicalID{}
	callees := map[domain.CanonicalID][]domain.CanonicalID{}
	edgeRows, err := r.Query(`SELECT source_id, target_id FROM edges WHERE kind = 'calls'`)
	if err != nil {
		return nil, err
	}
	defer edgeRows.Close()
	for edgeRows.Next() {
		var s, t string
		if err := edgeRows.Scan(&s, &t); err != nil {
			return nil, err
		}
		si, ti := domain.CanonicalID(s), domain.CanonicalID(t)
		if _, ok := info[ti]; !ok {
			continue
		}

		callers[ti] = append(callers[ti], si)
		if _, ok := info[si]; ok {
			callees[si] = append(callees[si], ti)
		}
	}
	if err := edgeRows.Err(); err != nil {
		return nil, err
	}

	reportable := func(id domain.CanonicalID) bool {
		n := info[id]
		return n != nil && n.Name != "main" && n.Name != "init"
	}
	var heads []domain.CanonicalID
	for _, id := range order {
		if reportable(id) && len(callers[id]) == 0 && !info[id].Referenced {
			heads = append(heads, id)
		}
	}

	visited := map[domain.CanonicalID]bool{}
	var chains [][]*domain.UnusedFunc
	var dfs func(id domain.CanonicalID, chain map[domain.CanonicalID]bool, acc *[]*domain.UnusedFunc)
	dfs = func(id domain.CanonicalID, chain map[domain.CanonicalID]bool, acc *[]*domain.UnusedFunc) {
		for _, c := range callees[id] {
			if chain[c] {
				continue
			}
			outside := false
			for _, cl := range callers[c] {
				if !chain[cl] {
					outside = true
					break
				}
			}
			if outside {
				continue
			}
			chain[c] = true
			*acc = append(*acc, info[c])
			dfs(c, chain, acc)
		}
	}
	for _, h := range heads {
		if visited[h] {
			continue
		}
		chain := map[domain.CanonicalID]bool{h: true}
		acc := []*domain.UnusedFunc{info[h]}
		dfs(h, chain, &acc)
		for _, u := range acc {
			visited[u.ID] = true
		}
		chains = append(chains, acc)
	}

	for _, id := range order {
		if visited[id] || !reportable(id) || len(callers[id]) == 0 || info[id].Referenced {
			continue
		}
		path := []domain.CanonicalID{}
		onPath := map[domain.CanonicalID]bool{}
		var find func(cur domain.CanonicalID, depth int) []domain.CanonicalID
		find = func(cur domain.CanonicalID, depth int) []domain.CanonicalID {
			if depth > 200 || onPath[cur] {
				if onPath[cur] {

					for i, p := range path {
						if p == cur {
							cyc := append([]domain.CanonicalID{}, path[i:]...)
							return cyc
						}
					}
				}
				return nil
			}
			onPath[cur] = true
			path = append(path, cur)
			for _, c := range callees[cur] {
				if cyc := find(c, depth+1); cyc != nil {
					return cyc
				}
			}
			path = path[:len(path)-1]
			delete(onPath, cur)
			return nil
		}
		cyc := find(id, 0)
		if cyc == nil || len(cyc) < 2 {
			continue
		}

		cycleSet := map[domain.CanonicalID]bool{}
		for _, c := range cyc {
			cycleSet[c] = true
		}
		pure := true
		for _, c := range cyc {
			for _, cl := range callers[c] {
				if !cycleSet[cl] {
					pure = false
					break
				}
			}
			if !pure {
				break
			}
		}
		if !pure {
			continue
		}
		acc := make([]*domain.UnusedFunc, 0, len(cyc))
		for _, c := range cyc {
			visited[c] = true
			acc = append(acc, info[c])
		}
		chains = append(chains, acc)
	}
	return chains, nil
}
