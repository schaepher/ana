package action

import (
	"fmt"
	"sort"

	"go.uber.org/zap"
)

// Q241 表间通路（table-path）：表 A → 表 B 数据通路——relations 全量
// 建表级邻接（双向，跨 mapping 表），BFS 最短跳数；同跳数多路径按
// 边类型优先级（fk > query > write > read）取最优一条，--json 全列候选。

// TablePathStep 通路一步（表A.列 → [类型] → 表B.列）。
type TablePathStep struct {
	FromTable string `json:"from_table"`
	FromCol   string `json:"from_col"`
	Type      string `json:"type"`
	ToTable   string `json:"to_table"`
	ToCol     string `json:"to_col"`
}

// TablePathResult 通路查询结果。
type TablePathResult struct {
	Path       []TablePathStep   `json:"path"`                // 最优路径（类型序列最优先）
	Candidates [][]TablePathStep `json:"candidates,omitempty"` // 全部同跳数最短路径
	Hops       int               `json:"hops"`                // 跳数（边数）
	Reachable  bool              `json:"reachable"`
}

// relTypeRank 边类型优先级（fk > query > write > read）。
var relTypeRank = map[string]int{"fk": 0, "query": 1, "write": 2, "read": 3}

// tableEdge 表级邻接边。
type tableEdge struct {
	to       string
	fromCol  string
	toCol    string
	rtype    string
}

// TablePath 表 A → 表 B 最短通路（Q241）。
func (a *Actions) TablePath(from, to string, maxHops int) (*TablePathResult, error) {
	logger := zap.L()
	logger.Info("enter (Actions).TablePath", zap.String("from", from), zap.String("to", to), zap.Int("max_hops", maxHops))
	defer logger.Info("exit (Actions).TablePath")
	rels, err := a.repo.GetAllTableRelations("")
	if err != nil {
		return nil, err
	}
	// 表级邻接（无向——表通路不限定方向；同表对多条边取类型最优）
	adj := map[string][]tableEdge{}
	for _, r := range rels {
		rank := relTypeRank[r.Type]
		// from → to
		if es, ok := adj[r.FromTable]; ok {
			best := true
			for _, e := range es {
				if e.to == r.ToTable && relTypeRank[e.rtype] <= rank {
					best = false
					break
				}
			}
			if best {
				adj[r.FromTable] = append(es, tableEdge{r.ToTable, r.FromCol, r.ToCol, r.Type})
			}
		} else {
			adj[r.FromTable] = []tableEdge{{r.ToTable, r.FromCol, r.ToCol, r.Type}}
		}
		// to → from（反向同类型）
		if es, ok := adj[r.ToTable]; ok {
			best := true
			for _, e := range es {
				if e.to == r.FromTable && relTypeRank[e.rtype] <= rank {
					best = false
					break
				}
			}
			if best {
				adj[r.ToTable] = append(es, tableEdge{r.FromTable, r.ToCol, r.FromCol, r.Type})
			}
		} else {
			adj[r.ToTable] = []tableEdge{{r.FromTable, r.ToCol, r.FromCol, r.Type}}
		}
	}
	if from == to {
		return &TablePathResult{Path: nil, Hops: 0, Reachable: true}, nil
	}
	// BFS 最短跳数：层扩展 + 前驱边集合（同层多前驱 → 多路径）
	type predEntry struct {
		prev string
		edge tableEdge
	}
	level := map[string]int{from: 0}
	preds := map[string][]predEntry{}
	queue := []string{from}
	reached := false
	for len(queue) > 0 && !reached {
		cur := queue[0]
		queue = queue[1:]
		if level[cur] >= maxHops {
			continue
		}
		for _, e := range adj[cur] {
			if _, seen := level[e.to]; seen {
				continue
			}
			level[e.to] = level[cur] + 1
			preds[e.to] = append(preds[e.to], predEntry{cur, e})
			if e.to == to {
				reached = true
				continue
			}
			queue = append(queue, e.to)
		}
	}
	if !reached {
		return &TablePathResult{Reachable: false, Hops: 0}, nil
	}
	// 回溯所有最短路径（前驱组合；fixture/真实规模下同跳数路径有限）
	var paths [][]TablePathStep
	var build func(t string) [][]TablePathStep
	build = func(t string) [][]TablePathStep {
		if t == from {
			return [][]TablePathStep{{}}
		}
		var out [][]TablePathStep
		for _, p := range preds[t] {
			for _, suffix := range build(p.prev) {
				step := TablePathStep{
					FromTable: p.prev, FromCol: p.edge.fromCol,
					Type: p.edge.rtype, ToTable: t, ToCol: p.edge.toCol,
				}
				out = append(out, append(append([]TablePathStep{}, suffix...), step))
			}
		}
		return out
	}
	paths = build(to)
	// 类型序列最优（fk=0...read=3 字典序最小）
	sort.SliceStable(paths, func(i, j int) bool {
		for k := 0; k < len(paths[i]) && k < len(paths[j]); k++ {
			ri, rj := relTypeRank[paths[i][k].Type], relTypeRank[paths[j][k].Type]
			if ri != rj {
				return ri < rj
			}
		}
		return len(paths[i]) < len(paths[j])
	})
	hops := 0
	if len(paths) > 0 {
		hops = len(paths[0])
	}
	return &TablePathResult{
		Path:       paths[0],
		Candidates: paths,
		Hops:       hops,
		Reachable:  true,
	}, nil
}

// ResolveTableName 表名解析：大小写不敏感精确匹配；多匹配返回候选
// 列表（空=未找到）。
func (a *Actions) ResolveTableName(name string) (string, []string, error) {
	logger := zap.L()
	logger.Info("enter (Actions).ResolveTableName", zap.String("name", name))
	defer logger.Info("exit (Actions).ResolveTableName")
	tables, err := a.repo.GetTables()
	if err != nil {
		return "", nil, err
	}
	var hits []string
	for _, t := range tables {
		if t == name {
			return t, nil, nil // 精确命中
		}
	}
	for _, t := range tables {
		if stringsEqualFold(t, name) {
			hits = append(hits, t)
		}
	}
	if len(hits) == 1 {
		return hits[0], nil, nil
	}
	if len(hits) > 1 {
		return "", hits, fmt.Errorf("表名 %q 命中多个候选", name)
	}
	return "", nil, fmt.Errorf("表 %q 不存在", name)
}

func stringsEqualFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
