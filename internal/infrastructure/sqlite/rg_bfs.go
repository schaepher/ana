package sqlite

import (
	"database/sql"
	"sort"
	"strings"
	"time"

	"github.com/schaepher/codeintel/internal/domain"
	"go.uber.org/zap"
)

// loadRelationGraph 一次加载全库图。两条全表查询：
//  1. 全部边（kind 一并取回，分流 dataAdj/allOut）
//  2. 全部节点元数据（json_extract 6 个属性）
//
// 空库返回空图（BFS 自然空结果，不报错）。
func loadRelationGraph(r *Repo) (*relationGraph, error) {
	logger := zap.L()
	logger.Info("enter loadRelationGraph") // Q207：内存图加载耗时可观测（大仓库秒级）
	start := time.Now()
	defer func() {
		logger.Info("exit loadRelationGraph", zap.Duration("elapsed", time.Since(start)))
	}()
	g := &relationGraph{
		dataAdj:     map[string][]string{},
		crossEdges:  map[string]map[string]bool{}, // Q199：跨函数边（argument/returns 正向）
		allOut:      map[string][]string{},
		nodes:       map[string]*relNode{},
		readsByFunc: map[string][]*relNode{},
		whereCols:   map[string]bool{}, // Q234：where 条件字段集
	}
	rows, err := r.Query(`SELECT source_id, target_id, kind FROM edges`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var src, tgt, kind string
		if err := rows.Scan(&src, &tgt, &kind); err != nil {
			rows.Close()
			return nil, err
		}
		g.allOut[src] = append(g.allOut[src], tgt)
		if isDataKind(kind) {
			g.dataAdj[src] = append(g.dataAdj[src], tgt)
			g.dataAdj[tgt] = append(g.dataAdj[tgt], src)
			// Q199：跨函数边（argument/returns，无向记录）——write 终点
			// 的链经过跨函数边时丢弃（对象级传递 ≠ 字段值流入）；query
			// 键关联跨函数是常态，不受限。遍历本身保持无向（单向化会
			// 切断真实的跨函数键关联链）
			if isDirectedKind(kind) {
				if g.crossEdges[src] == nil {
					g.crossEdges[src] = map[string]bool{}
				}
				g.crossEdges[src][tgt] = true
				if g.crossEdges[tgt] == nil {
					g.crossEdges[tgt] = map[string]bool{}
				}
				g.crossEdges[tgt][src] = true
			}
		}
	}
	rows.Close()

	nrows, err := r.Query(`SELECT id, kind, name,
		json_extract(properties, '$.access_kind'),
		json_extract(properties, '$.type_string'),
		json_extract(properties, '$.func_id'),
		json_extract(properties, '$.full_path'),
		json_extract(properties, '$.is_external') FROM nodes`)
	if err != nil {
		return nil, err
	}
	for nrows.Next() {
		var id, kind, name string
		var access, ts, funcID, fullPath sql.NullString
		var ext sql.NullString
		if err := nrows.Scan(&id, &kind, &name, &access, &ts, &funcID, &fullPath, &ext); err != nil {
			nrows.Close()
			return nil, err
		}
		n := &relNode{
			id:         id,
			kind:       kind,
			name:       name,
			access:     access.String,
			typeString: ts.String,
			funcID:     funcID.String,
			fullPath:   fullPath.String,
			isExternal: ext.String == "true",
		}
		g.nodes[id] = n
		if kind == string(domain.KindFieldAccess) && n.access == "read" {
			g.readsByFunc[n.funcID] = append(g.readsByFunc[n.funcID], n)
		}
		// Q234：where 条件字段集（查询时 where 使用的列）——规则 A 提升
		// BFS 终点 / 规则 B 直接识别的依据
		if kind == string(domain.KindFieldAccess) && n.access == "filter" && n.isExternal {
			g.whereCols[n.name] = true
		}
	}
	nrows.Close()
	return g, nrows.Err()
}

// tables 内存版 GetTables（语义一致：外部 gorm/sql/xorm 虚拟节点
// 表名去重排序；name 无点或含多点不产生表名）。

func (g *relationGraph) tables() []string {
	set := map[string]bool{}
	for _, n := range g.nodes {
		if n.kind != string(domain.KindFieldAccess) || !n.isExternal || !relTypeStrings[n.typeString] {
			continue
		}

		dot := strings.Index(n.name, ".")
		if dot <= 0 || strings.Index(n.name[dot+1:], ".") >= 0 {
			continue
		}
		set[n.name[:dot]] = true
	}
	out := make([]string, 0, len(set))
	for t := range set {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// typeNameOf 内存版：节点 type_string 提取类型名（[]example.com/m.Session →
// Session；*Session → Session；无类型/基本类型返回 ok=false）。
func (g *relationGraph) typeNameOf(id string) (string, bool) {
	n := g.nodes[id]
	if n == nil || n.typeString == "" {
		return "", false
	}
	t := n.typeString
	if i := strings.LastIndex(t, "."); i >= 0 {
		t = t[i+1:]
	}
	t = strings.Trim(t, "[]*")
	switch t {
	case "", "any", "string", "int", "int64", "bool", "error", "byte":
		return "", false
	}
	return t, true
}

// filterReachable2 桥条件：该 read 节点下游 2 跳内可达 filter 外部节点
// （字段 → 值 → filter：真正进 Where 的字段；防同类型全字段扩散）。
// 定向出边（allOut）——与旧 SQL EXISTS（e1.source_id = n2.id）等价；
// 双向会让桥过度宽松 → 多关联噪音。
func (g *relationGraph) filterReachable2(id string) bool {
	for _, x := range g.allOut[id] {
		for _, y := range g.allOut[x] {
			if n := g.nodes[y]; n != nil && n.kind == string(domain.KindFieldAccess) &&
				n.access == "filter" && n.isExternal {
				return true
			}
		}
	}
	return false
}

func (r *Repo) typeNameOf(id string) (string, bool) {
	var ts sql.NullString
	if err := r.QueryRow(`SELECT json_extract(properties, '$.type_string') FROM nodes WHERE id = ?`, id).Scan(&ts); err != nil || !ts.Valid {
		return "", false
	}
	t := ts.String
	if i := strings.LastIndex(t, "."); i >= 0 {
		t = t[i+1:]
	}
	t = strings.Trim(t, "[]*")
	if t == "" || t == "any" || t == "string" || t == "int" || t == "int64" || t == "bool" || t == "error" || t == "byte" {
		return "", false
	}
	return t, true
}
