package sqlite

import (
	"database/sql"
	"sort"
	"strings"

	"github.com/schaepher/codeintel/internal/domain"
	"go.uber.org/zap"
)

// 表关联分析（query relations）P0②：一次加载全库图（边 + 节点元数据）
// 到内存，BFS 纯内存进行——替代原逐节点 SQL 查询。go2o 实测规模
// （节点 ~15 万、边 ~15 万、外部列 ~3.6 千）全内存无压力，单次加载
// ~1-2s，之后全部表 BFS 都在毫秒级。
//
// 关系图结构：
//   - dataAdj：数据流边（data_flows_to/argument/returns/summary_io/
//     alias/phi_operand）双向邻接——BFS 主边
//   - allAdj：全部边双向邻接——循环读出桥（Q152）的 2 跳可达检查
//     （桥 SQL 的 EXISTS 不限定边 kind，须用全量边）
//   - readsByFunc：函数 → 该函数全部 field_access read 节点（桥候选，
//     避免每次 BFS 全表扫描）

// relationsMaxDepth BFS 最大深度（与旧实现一致）。
const relationsMaxDepth = 12

// --memory 模式（P0④）：auto 按规模判断；full 强制内存图；sql 强制
// 逐节点 SQL（大仓库防爆内存的逃生口）。
const (
	relationMemoryAuto = ""    // 默认
	relationMemoryFull = "full"
	relationMemorySQL  = "sql"
)

// 内存图安全阈值：节点或边超过时 auto 走 SQL 路径。内存占用估计
// 节点 ×~200B + 边 ×~100B（go2o 15 万节点实测 ~40MB）；50 万节点 +
// 80 万边约 180MB，3.5G 机器安全。
const (
	relationGraphMaxNodes = 500_000
	relationGraphMaxEdges = 800_000
)

// useMemoryGraph 判定是否走内存图路径。mode 显式时跟随；auto 用
// build_metadata 缓存的规模（构建时写入，不每次 COUNT；无元数据时
// 按小库处理走内存路径）。
func (r *Repo) useMemoryGraph(mode string) bool {
	switch mode {
	case relationMemoryFull:
		return true
	case relationMemorySQL:
		return false
	}
	if m, err := r.GetLatest(); err == nil {
		if m.Nodes > relationGraphMaxNodes || m.Edges > relationGraphMaxEdges {
			return false
		}
	}
	return true
}

// relTypeStrings 参与关联终点判定的虚拟节点类型（byNode 查询条件；
// Q175 后含 xorm——旧实现漏 xorm 导致 xorm 表关联全丢，已修复）。
var relTypeStrings = map[string]bool{"sql": true, "gorm": true, "xorm": true}

type relationGraph struct {
	dataAdj     map[string][]string // 数据流边双向邻接（BFS 主边，与旧 SQL OR 双向等价）
	allOut      map[string][]string // 全部边定向邻接（出边——桥 2 跳检查须定向，
	//                              与旧 SQL EXISTS 的 e1.source_id = n2.id 等价；
	//                              双向会让桥过度宽松 → 多关联噪音）
	nodes       map[string]*relNode
	readsByFunc map[string][]*relNode
}

type relNode struct {
	id         string
	kind       string
	name       string
	access     string // field_access 的 access_kind（read/write/filter）
	typeString string // 虚拟节点类型（sql/gorm/xorm/...）
	funcID     string
	fullPath   string
	isExternal bool
}

// loadRelationGraph 一次加载全库图。两条全表查询：
//  1. 全部边（kind 一并取回，分流 dataAdj/allOut）
//  2. 全部节点元数据（json_extract 6 个属性）
//
// 空库返回空图（BFS 自然空结果，不报错）。
func loadRelationGraph(r *Repo) (*relationGraph, error) {
	logger := zap.L()
	logger.Debug("enter loadRelationGraph")
	defer logger.Debug("exit loadRelationGraph")
	g := &relationGraph{
		dataAdj:     map[string][]string{},
		allOut:      map[string][]string{},
		nodes:       map[string]*relNode{},
		readsByFunc: map[string][]*relNode{},
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
	}
	nrows.Close()
	return g, nrows.Err()
}

// isDataKind 是否为 BFS 数据流边。
func isDataKind(kind string) bool {
	switch kind {
	case "data_flows_to", "argument", "returns", "summary_io", "alias", "phi_operand":
		return true
	}
	return false
}

// tables 内存版 GetTables（语义一致：外部 gorm/sql/xorm 虚拟节点
// 表名去重排序；name 无点或含多点不产生表名）。
func (g *relationGraph) tables() []string {
	set := map[string]bool{}
	for _, n := range g.nodes {
		if n.kind != string(domain.KindFieldAccess) || !n.isExternal || !relTypeStrings[n.typeString] {
			continue
		}
		// name 恰含一个点（表.列）；无点（整表行）或多点（嵌套路径）跳过
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
		return "", false // 基本类型无字段
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

// relationsFor 单表关联分析（等价旧 GetTableRelations 逐节点 SQL 版）：
// 本表全部列虚拟节点为起点 BFS，收集其他表虚拟节点（表.列，is_external），
// 输出稳定排序（from_col, hops, to_table, to_col）。
func (g *relationGraph) relationsFor(table string) []*domain.TableRelation {
	// 起点：本表全部列虚拟节点
	var starts []*relNode
	for _, n := range g.nodes {
		if n.kind == string(domain.KindFieldAccess) && n.isExternal &&
			(n.name == table || strings.HasPrefix(n.name, table+".")) {
			starts = append(starts, n)
		}
	}
	seen := map[string]*domain.TableRelation{} // "fromCol|toTable|toCol" → 关联（Type 取最高）
	var all []*domain.TableRelation
	for _, st := range starts {
		visited := map[string]int{st.id: 0}
		queue := []string{st.id}
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			depth := visited[cur]
			if depth >= relationsMaxDepth {
				continue
			}
			// 数据边双向扩展（A 读 → 值流向 B；B 的写值也可能来自 A 的读）
			for _, other := range g.dataAdj[cur] {
				if _, ok := visited[other]; ok {
					continue
				}
				visited[other] = depth + 1
				queue = append(queue, other)
			}
			// 循环读出桥（Q152）：BFS 到 ssa_value 节点时，桥接同函数、
			// 同类型的字段读取节点（s.ID.read）：对象读出的值在函数内经
			// 字段读取使用。类型匹配（full_path 含类型名）限宽，防跨对象扩散
			if n := g.nodes[cur]; n != nil && n.funcID != "" {
				if tn, ok := g.typeNameOf(cur); ok && tn != "" {
					for _, n2 := range g.readsByFunc[n.funcID] {
						if !strings.Contains(n2.fullPath, tn) || !g.filterReachable2(n2.id) {
							continue
						}
						if _, ok := visited[n2.id]; !ok {
							visited[n2.id] = depth + 1
							queue = append(queue, n2.id)
						}
					}
				}
			}
		}
		// 命中：其他表的虚拟节点（field_access + sql/gorm/xorm）。
		if len(visited) <= 1 {
			continue
		}
		for id, d := range visited {
			if d == 0 {
				continue // 起点自身
			}
			n := g.nodes[id]
			if n == nil || n.kind != string(domain.KindFieldAccess) || !relTypeStrings[n.typeString] {
				continue // 非 表.列（ssa_value 等）
			}
			if !strings.Contains(n.name, ".") {
				continue
			}
			dot := strings.Index(n.name, ".")
			otherTable, col := n.name[:dot], n.name[dot+1:]
			if otherTable == table {
				continue // 本表内部列，非关联
			}
			fromCol := st.name
			if i := strings.Index(fromCol, "."); i >= 0 {
				fromCol = fromCol[i+1:]
			}
			rtype := domain.RelationRead
			switch n.access {
			case "filter":
				rtype = domain.RelationQuery // WHERE 过滤列：键关联（高置信）
			case "write":
				rtype = domain.RelationWrite
			}
			// 键关联列名呼应：外键列名含主键列名（session_id 含 id；
			// title/created_at → session_id 是同源共享误桥）——不呼应降级
			if rtype == domain.RelationQuery && col != fromCol &&
				!strings.HasSuffix(strings.ToLower(col), strings.ToLower(fromCol)) {
				rtype = domain.RelationRead
			}
			key := st.name + "|" + otherTable + "|" + col
			if ex, ok := seen[key]; ok {
				// 同列多节点（write/filter/read）：Type 取 rank 最高
				// （query > write > read；旧实现只升级 query——write 不覆盖
				// read，结果依赖 map 遍历顺序不确定——改为 rank 比较确定化）
				if relTypeRank(rtype) > relTypeRank(ex.Type) {
					ex.Type = rtype
				}
				if d < ex.Hops {
					ex.Hops = d
				}
				continue
			}
			seen[key] = &domain.TableRelation{
				FromTable: table, FromCol: fromCol,
				ToTable: otherTable, ToCol: col,
				Hops: d, Type: rtype,
			}
			all = append(all, seen[key])
		}
	}
	// Q159 外键语义过滤（与旧实现一致）——
	// 1) id→id 一律丢弃；2) 同目标列多起点时外键形态列（xxx_id）优先
	out := filterFKNoise(all)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.FromCol != b.FromCol {
			return a.FromCol < b.FromCol
		}
		if a.Hops != b.Hops {
			return a.Hops < b.Hops
		}
		if a.ToTable != b.ToTable {
			return a.ToTable < b.ToTable
		}
		return a.ToCol < b.ToCol
	})
	return out
}

// ---- relation_candidates 缓存（P0③）----

// currentBuildID 最新构建 id；无构建元数据（fixture/手动建库）返回空串——
// 缓存层整体跳过（现场计算，不写缓存行）。
func (r *Repo) currentBuildID() string {
	m, err := r.GetLatest()
	if err != nil {
		return ""
	}
	return m.BuildID
}

// loadRelationCandidates 读单表缓存。返回 ok=true 表示缓存已计算该表
// （含"无关联"空结果——写入时带 marker 行）；ok=false 表示未缓存需现场算。
func (r *Repo) loadRelationCandidates(table string) ([]*domain.TableRelation, bool) {
	buildID := r.currentBuildID()
	if buildID == "" {
		return nil, false
	}
	var cnt int
	if err := r.QueryRow(`SELECT COUNT(*) FROM relation_candidates
		WHERE build_id = ? AND from_table = ?`, buildID, table).Scan(&cnt); err != nil || cnt == 0 {
		return nil, false
	}
	rows, err := r.Query(`SELECT from_table, from_col, to_table, to_col, hops, type
		FROM relation_candidates WHERE build_id = ? AND from_table = ? AND from_col <> ''
		ORDER BY from_col, hops, to_table, to_col`, buildID, table)
	if err != nil {
		return nil, false
	}
	defer rows.Close()
	var rels []*domain.TableRelation
	for rows.Next() {
		var rel domain.TableRelation
		if err := rows.Scan(&rel.FromTable, &rel.FromCol, &rel.ToTable, &rel.ToCol, &rel.Hops, &rel.Type); err != nil {
			return nil, false
		}
		rels = append(rels, &rel)
	}
	if err := rows.Err(); err != nil {
		return nil, false
	}
	if rels == nil {
		rels = []*domain.TableRelation{} // 无关联也返回非 nil（CLI 输出 [] 而非 null）
	}
	return rels, true
}

// saveRelationCandidates 写单表缓存（覆盖旧行；rels 为空时写 marker 行，
// 标记"该表已计算过、无关联"，避免每次查询重算）。
func (r *Repo) saveRelationCandidates(table string, rels []*domain.TableRelation) {
	buildID := r.currentBuildID()
	if buildID == "" {
		return
	}
	tx, err := r.Begin()
	if err != nil {
		return
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM relation_candidates WHERE build_id = ? AND from_table = ?`, buildID, table); err != nil {
		return
	}
	if len(rels) == 0 {
		_, _ = tx.Exec(`INSERT OR REPLACE INTO relation_candidates
			(build_id, from_table, from_col, to_table, to_col, hops, type)
			VALUES (?, ?, '', '', '', 0, '')`, buildID, table)
	} else {
		for _, rel := range rels {
			_, _ = tx.Exec(`INSERT OR REPLACE INTO relation_candidates
				(build_id, from_table, from_col, to_table, to_col, hops, type)
				VALUES (?, ?, ?, ?, ?, ?, ?)`,
				buildID, rel.FromTable, rel.FromCol, rel.ToTable, rel.ToCol, rel.Hops, rel.Type)
		}
	}
	_ = tx.Commit()
}

// rebuildRelationCandidates --all 全量重建缓存：清空该 build_id 全部行，
// 每张表写 marker（含无关联表），再写全部真实关联行。
func (r *Repo) rebuildRelationCandidates(rels []*domain.TableRelation, tables []string) {
	buildID := r.currentBuildID()
	if buildID == "" {
		return
	}
	tx, err := r.Begin()
	if err != nil {
		return
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM relation_candidates WHERE build_id = ?`, buildID); err != nil {
		return
	}
	for _, t := range tables {
		_, _ = tx.Exec(`INSERT OR REPLACE INTO relation_candidates
			(build_id, from_table, from_col, to_table, to_col, hops, type)
			VALUES (?, ?, '', '', '', 0, '')`, buildID, t)
	}
	for _, rel := range rels {
		_, _ = tx.Exec(`INSERT OR REPLACE INTO relation_candidates
			(build_id, from_table, from_col, to_table, to_col, hops, type)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			buildID, rel.FromTable, rel.FromCol, rel.ToTable, rel.ToCol, rel.Hops, rel.Type)
	}
	_ = tx.Commit()
}

// filterFKNoise Q159 外键语义过滤（独立函数便于单测）：
// id→id 一律丢弃（两表都不会拿各自自增主键互查）；同目标列多起点时
// 外键形态列（xxx_id）优先——主键 id 起点是对象值共享桥接噪音；保留
// 形态：A.xxx_id → B.id（外键查主键）、A.id → B.xxx_id（主键被外键引用
// 查询）、A.xxx_id → B.xxx_id（业务关联键）。
func filterFKNoise(all []*domain.TableRelation) []*domain.TableRelation {
	byTarget := map[string][]*domain.TableRelation{}
	for _, rel := range all {
		byTarget[rel.ToTable+"."+rel.ToCol] = append(byTarget[rel.ToTable+"."+rel.ToCol], rel)
	}
	var out []*domain.TableRelation
	for _, rels := range byTarget {
		hasFK := false
		for _, r := range rels {
			if r.FromCol != "id" {
				hasFK = true
				break
			}
		}
		for _, r := range rels {
			if r.FromCol == "id" && r.ToCol == "id" {
				continue // id→id：主键互查不存在
			}
			if hasFK && r.FromCol == "id" {
				continue // 同目标有更直接的外键列起点
			}
			out = append(out, r)
		}
	}
	return out
}
func (r *Repo) relationsForSQL(table string) ([]*domain.TableRelation, error) {
	// 本表全部列虚拟节点
	rows, err := r.Query(`SELECT id, name FROM nodes
		WHERE kind = 'field_access' AND json_extract(properties, '$.is_external') = 'true'
		  AND (name = ? OR name LIKE ?)`, table, table+".%")
	if err != nil {
		return nil, err
	}
	type colNode struct{ id, name string }
	var starts []colNode
	for rows.Next() {
		var c colNode
		if err := rows.Scan(&c.id, &c.name); err != nil {
			rows.Close()
			return nil, err
		}
		starts = append(starts, c)
	}
	rows.Close()

	const maxDepth = 12
	dataKinds := "'data_flows_to','argument','returns','summary_io','alias','phi_operand'"
	seen := map[string]*domain.TableRelation{} // "fromCol|toTable|toCol" → 关联（Type 取最高）
	var all []*domain.TableRelation
	for _, st := range starts {
		// BFS：沿 data 边双向扩展，收集其他表虚拟节点
		visited := map[string]int{st.id: 0}
		queue := []string{st.id}
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			depth := visited[cur]
			if depth >= maxDepth {
				continue
			}
			// 出边 + 入边（双向：A 读 → 值流向 B；B 的写值也可能来自 A 的读）
			ns, err := r.Query(`SELECT e.source_id, e.target_id FROM edges e
				WHERE e.kind IN (`+dataKinds+`) AND (e.source_id = ? OR e.target_id = ?)`, cur, cur)
			if err != nil {
				return nil, err
			}
			var next []string
			for ns.Next() {
				var src, tgt string
				if err := ns.Scan(&src, &tgt); err != nil {
					ns.Close()
					return nil, err
				}
				other := src
				if src == cur {
					other = tgt
				}
				if _, ok := visited[other]; ok {
					continue
				}
				visited[other] = depth + 1
				next = append(next, other)
			}
			ns.Close()
			// 循环读出桥（Q152）：SSA 的 range 迭代器内部（Slice/phi/Next）
			// 不建边——BFS 到 ssa_value 节点时，桥接同函数、同类型的字段
			// 读取节点（s.ID.read）：对象读出的值在函数内经字段读取使用。
			// 类型匹配（type_string 含类型名）限宽，防跨对象扩散
			if ts, ok := r.typeNameOf(cur); ok && ts != "" {
				fs, err := r.Query(`SELECT n2.id FROM nodes n1, nodes n2
					WHERE n1.id = ? AND n2.kind = 'field_access'
					  AND json_extract(n2.properties, '$.access_kind') = 'read'
					  AND json_extract(n2.properties, '$.func_id') = json_extract(n1.properties, '$.func_id')
					  AND instr(json_extract(n2.properties, '$.full_path'), ?) > 0
					  -- 精确桥：仅桥下游 2 跳内可达 filter 节点的字段读取
					  -- （字段 → 值 → filter：真正进 Where 的字段；防同类型全字段扩散）
					  AND EXISTS (
						SELECT 1 FROM edges e1
						JOIN edges e2 ON e2.source_id = e1.target_id
						JOIN nodes n3 ON n3.id = e2.target_id
						WHERE e1.source_id = n2.id
						  AND n3.kind = 'field_access'
						  AND json_extract(n3.properties, '$.access_kind') = 'filter'
						  AND json_extract(n3.properties, '$.is_external') = 'true'
					  )`, cur, ts)
				if err == nil {
					var bridge []string
					for fs.Next() {
						var bid string
						if err := fs.Scan(&bid); err == nil {
							if _, ok := visited[bid]; !ok {
								visited[bid] = depth + 1
								bridge = append(bridge, bid)
							}
						}
					}
					fs.Close()
					queue = append(queue, bridge...)
				}
			}
			queue = append(queue, next...)
		}
		// 命中：其他表的虚拟节点（is_external field_access，表名不同）。
		// 列名 = 节点 Name（表.列）；表名 = 前缀
		byNode := map[string]string{} // nodeID → "name|access_kind"（预查）
		if len(visited) > 1 {
			ids := make([]any, 0, len(visited))
			for id := range visited {
				ids = append(ids, id)
			}
			q := `SELECT id, name, json_extract(properties, '$.access_kind') FROM nodes
			  WHERE id IN (` + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + `)
			  AND json_extract(properties, '$.type_string') IN ('sql', 'gorm', 'xorm')`
			ns, err := r.Query(q, ids...)
			if err != nil {
				return nil, err
			}
			for ns.Next() {
				var id, name, access string
				if err := ns.Scan(&id, &name, &access); err != nil {
					ns.Close()
					return nil, err
				}
				byNode[id] = name + "|" + access
			}
			ns.Close()
		}
		for id, d := range visited {
			if id == st.id || d == 0 {
				continue
			}
			meta := byNode[id]
			if meta == "" {
				continue
			}
			name := meta
			access := ""
			if i := strings.Index(meta, "|"); i >= 0 {
				name, access = meta[:i], meta[i+1:]
			}
			if !strings.Contains(name, ".") {
				continue // 非 表.列（ssa_value 等）
			}
			dot := strings.Index(name, ".")
			otherTable, col := name[:dot], name[dot+1:]
			if otherTable == table {
				continue // 本表内部列，非关联
			}
			key := st.name + "|" + otherTable + "|" + col
			fromCol := st.name
			if i := strings.Index(fromCol, "."); i >= 0 {
				fromCol = fromCol[i+1:]
			}
			rtype := domain.RelationRead
			switch access {
			case "filter":
				rtype = domain.RelationQuery // WHERE 过滤列：键关联（高置信）
			case "write":
				rtype = domain.RelationWrite
			}
			// 键关联列名呼应：外键列名含主键列名（session_id 含 id；
			// title/created_at → session_id 是同源共享误桥）——不呼应降级
			if rtype == domain.RelationQuery && col != fromCol &&
				!strings.HasSuffix(strings.ToLower(col), strings.ToLower(fromCol)) {
				rtype = domain.RelationRead
			}
			if ex, ok := seen[key]; ok {
				// 同列多节点（write/filter/read）：Type 取 rank 最高
				// （query > write > read，与内存路径一致——旧逻辑只升级
				// query 导致结果依赖遍历顺序）
				if relTypeRank(rtype) > relTypeRank(ex.Type) {
					ex.Type = rtype
				}
				if d < ex.Hops {
					ex.Hops = d
				}
				continue
			}
			rel := &domain.TableRelation{
				FromTable: table, FromCol: fromCol,
				ToTable: otherTable, ToCol: col,
				Hops: d, Type: rtype,
			}
			seen[key] = rel
			all = append(all, rel)
		}
	}
	// Q159：外键语义过滤——
	// 1) id→id 一律丢弃（两表都不会拿各自自增主键互查）；
	// 2) 同目标列多起点时外键形态列（xxx_id）优先——主键 id 起点是
	//    对象值共享桥接噪音；保留形态：A.xxx_id → B.id（外键查主键）、
	//    A.id → B.xxx_id（主键被外键引用查询，如 mm_member.id →
	//    mm_account.member_id）、A.xxx_id → B.xxx_id（业务关联键）
	byTarget := map[string][]*domain.TableRelation{}
	for _, rel := range all {
		byTarget[rel.ToTable+"."+rel.ToCol] = append(byTarget[rel.ToTable+"."+rel.ToCol], rel)
	}
	var out []*domain.TableRelation
	for _, rels := range byTarget {
		hasFK := false
		for _, r := range rels {
			if r.FromCol != "id" {
				hasFK = true
				break
			}
		}
		for _, r := range rels {
			if r.FromCol == "id" && r.ToCol == "id" {
				continue // id→id：主键互查不存在
			}
			if hasFK && r.FromCol == "id" {
				continue // 同目标有更直接的外键列起点
			}
			out = append(out, r)
		}
	}
	return out, nil
}

// typeNameOf 查节点 type_string 并提取类型名（[]example.com/m.Session →
// Session；*Session → Session；无类型/非 ssa_value 返回 ok=false）。
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
		return "", false // 基本类型无字段
	}
	return t, true
}

// getAllTableRelationsSQL --memory sql 模式的全库聚合：GetTables 枚举 +
// 逐表 relationsForSQL（逐节点查询，内存 O(1)——大仓库逃生路径）。
func (r *Repo) getAllTableRelationsSQL() ([]*domain.TableRelation, error) {
	tables, err := r.GetTables()
	if err != nil {
		return nil, err
	}
	seen := map[string]*domain.TableRelation{} // ft|fc|tt|tc → 最佳关联
	for _, t := range tables {
		rels, err := r.relationsForSQL(t)
		if err != nil {
			return nil, err
		}
		for _, rel := range rels {
			key := rel.FromTable + "|" + rel.FromCol + "|" + rel.ToTable + "|" + rel.ToCol
			ex, ok := seen[key]
			if !ok || rel.Hops < ex.Hops || (rel.Hops == ex.Hops && relTypeRank(rel.Type) > relTypeRank(ex.Type)) {
				seen[key] = rel
			}
		}
	}
	out := make([]*domain.TableRelation, 0, len(seen))
	for _, rel := range seen {
		out = append(out, rel)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.FromTable != b.FromTable {
			return a.FromTable < b.FromTable
		}
		if a.FromCol != b.FromCol {
			return a.FromCol < b.FromCol
		}
		if a.ToTable != b.ToTable {
			return a.ToTable < b.ToTable
		}
		return a.ToCol < b.ToCol
	})
	r.rebuildRelationCandidates(out, tables)
	return out, nil
}
