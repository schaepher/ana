package sqlite

import (
	"database/sql"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/schaepher/codeintel/internal/domain"
	"go.uber.org/zap"
)

// GetTableRelations 表间关联分析（query relations）：对该表的全部列
// 虚拟节点沿数据流边 BFS，收集命中其他表的虚拟节点（表.列，is_external）。
// P0② 一次加载全图到内存（loadRelationGraph）替代逐节点 SQL；P0③ 结果
// 按 build_id 缓存到 relation_candidates，命中直接返回（无 build_metadata
// 时跳过缓存）。mode 为 --memory（""=auto 按规模、full=强制内存图、
// sql=强制逐节点 SQL——大仓库防爆内存逃生口）。无外键依赖——纯代码使用
// 方式推断（A.x 读出值流入 B.y 过滤/写入）。
func (r *Repo) GetTableRelations(table, mode string) ([]*domain.TableRelation, error) {
	logger := zap.L()
	logger.Debug("enter (Repo).GetTableRelations")
	defer logger.Debug("exit (Repo).GetTableRelations")

	if rels, ok := r.loadRelationCandidates(table); ok {
		// Q220c：缓存命中路径同样合并用户连线规则（规则独立于 build_id）
		out := dedupRelationNoise(rels, r.relationHops)
		return r.mergeRuleRelations(out, table)
	}
	var rels []*domain.TableRelation
	var err error
	if r.useMemoryGraph(mode) {
		var g *relationGraph
		// 任务 #165：进程内图缓存（按 build_id 失效）——serve 单表展开
		// 每次请求复用内存图，不再重复 loadRelationGraph
		if g, err = r.cachedRelationGraph(); err == nil {
			rels = g.relationsFor(table)
		}
	} else {
		rels, err = r.relationsForSQL(table)
	}
	if err != nil {
		return nil, err
	}
	// Q208：缓存存未过滤全量——hops 过滤是读取期行为（缓存命中路径
	// 也过 dedup）。此前存 dedup 后行：首次窄参数查询后放宽 q_hops
	// 无法展示长链（长链行没进缓存）。
	r.saveRelationCandidates(table, rels)
	out := dedupRelationNoise(rels, r.relationHops)
	// Q220c：合并用户连线规则（单表查询只合并本表规则线，规则生成 fk，
	// 同 key 覆盖低 rank）
	return r.mergeRuleRelations(out, table)
}

// GetTables 枚举全库外部表名（gorm/sql 虚拟节点表名去重，Q160）。
func (r *Repo) GetTables() ([]string, error) {
	logger := zap.L()
	logger.Debug("enter (Repo).GetTables")
	defer logger.Debug("exit (Repo).GetTables")
	rows, err := r.Query(`SELECT DISTINCT substr(name, 1, instr(name, '.') - 1) FROM nodes
		WHERE kind = 'field_access' AND json_extract(properties, '$.is_external') = 'true'
		  AND json_extract(properties, '$.type_string') IN ('gorm', 'sql', 'xorm')
		  AND name NOT LIKE '%.%.%' ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		if t == "" {
			continue
		}
		tables = append(tables, t)
	}
	return tables, rows.Err()
}

// GetAllTableRelations 全库关联聚合（Q160）：一次加载图（loadRelationGraph），
// 全部表内存 BFS 合并去重——同 from/to 列对取 hops 最小 + Type 最高
// （query > write > read）。结果按 build_id 全量写入 relation_candidates。
// 缓存优先（Q177）：当前 build_id 已覆盖全部表（marker + 关联行）时
// 直接读缓存返回——--all 与单表查询同源，避免重复全图 BFS。输出按
// from/to 稳定排序，AGENT 一次调用拿全库（query relations --all /
// export relations）。mode 同 GetTableRelations（--memory）；sql 模式
// 逐表走 relationsForSQL。
func (r *Repo) GetAllTableRelations(mode string) ([]*domain.TableRelation, error) {
	logger := zap.L()
	logger.Info("enter (Repo).GetAllTableRelations", zap.String("mode", mode)) // Q207：全库 BFS 耗时可观测
	start := time.Now()
	defer func() {
		logger.Info("exit (Repo).GetAllTableRelations", zap.Duration("elapsed", time.Since(start)))
	}()
	// Q228：全量路径先查计算进度——done 才返回数据；未完成返回
	// ErrRelationInProgress（调用方读 RelationProgress 展示/轮询进度，
	// 不现场计算——计算由 precompute 命令或 serve 后台任务执行）
	if p, _ := r.RelationProgress(); p.Status != "done" {
		logger.Debug("relations --all 计算未完成", zap.String("status", p.Status))
		return nil, ErrRelationInProgress
	}
	// 缓存优先：该 build_id（含分析逻辑版本）已完整计算（覆盖全部表）→ 直接返回
	if buildID := r.cacheKey(); buildID != "" {
		if rels, ok := r.loadAllRelationCandidates(buildID); ok {
			logger.Debug("relations --all 命中缓存", zap.String("build_id", buildID))
			out := dedupRelationNoise(rels, r.relationHops)
			return r.mergeRuleRelations(out, "")
		}
	}
	if !r.useMemoryGraph(mode) {
		rels, err := r.getAllTableRelationsSQL()
		if err != nil {
			return nil, err
		}
		out := dedupRelationNoise(rels, r.relationHops)
		return r.mergeRuleRelations(out, "")
	}
	g, err := r.cachedRelationGraph() // 任务 #165：进程内图缓存（按 build_id 失效）
	if err != nil {
		return nil, err
	}
	tables := g.tables()
	seen := map[string]*domain.TableRelation{}
	for _, t := range tables {
		for _, rel := range g.relationsFor(t) {
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
	// Q208：缓存存未过滤全量（rebuild 写全量；返回仍按当前 hops 过滤）
	r.rebuildRelationCandidates(out, tables)
	final := dedupRelationNoise(out, r.relationHops)
	// Q220c：合并用户连线规则（规则生成 fk，同 key 覆盖低 rank）
	return r.mergeRuleRelations(final, "")
}

// PrecomputeAllRelations 全量计算并写入缓存（Q228）：加载关系图 →
// 逐表 relationsFor → 每批写进度（progressFn(done, total)）→ 完成写
// relation_candidates + status=done。CLI precompute 命令（前台同步）
// 与 serve 后台任务（goroutine）共用。
func (r *Repo) PrecomputeAllRelations(progressFn func(done, total int)) error {
	logger := zap.L()
	logger.Info("enter (Repo).PrecomputeAllRelations")
	start := time.Now()
	defer func() {
		logger.Info("exit (Repo).PrecomputeAllRelations", zap.Duration("elapsed", time.Since(start)))
	}()
	g, err := r.cachedRelationGraph()
	if err != nil {
		return err
	}
	tables := g.tables()
	total := len(tables)
	if ok, err := r.beginRelationCompute(total); err != nil {
		return err
	} else if !ok {
		// 已有任务在跑（serve 兜底刚抢占或跨进程）——继续计算：rebuild
		// 缓存为幂等覆盖写（结果一致），finish 统一置 done；进度沿用
		// 已有任务行（Q228：begin 失败不再提前 return——否则 serve
		// 兜底启动的 goroutine 抢占失败后不计算，进度永远停在 running）
		logger.Debug("begin 抢占失败——继续计算（幂等覆盖）")
	}
	seen := map[string]*domain.TableRelation{}
	for i, t := range tables {
		for _, rel := range g.relationsFor(t) {
			key := rel.FromTable + "|" + rel.FromCol + "|" + rel.ToTable + "|" + rel.ToCol
			ex, ok := seen[key]
			if !ok || rel.Hops < ex.Hops || (rel.Hops == ex.Hops && relTypeRank(rel.Type) > relTypeRank(ex.Type)) {
				seen[key] = rel
			}
		}
		// 每 5 表写一次进度（避免逐表写库）；total<=5 时最后一表也写
		if (i+1)%5 == 0 || i+1 == total {
			if err := r.updateRelationProgress(i + 1); err != nil {
				return err
			}
		}
		if progressFn != nil {
			progressFn(i+1, total)
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
	return r.finishRelationCompute(total)
}

// StartRelationComputeIfNeeded 查询端自动兜底（Q228，serve /api/er
// 全量路径）：计算未完成且无活跃任务（unknown/pending/过期 running）
// 时抢占并启动——返回 started=true 表示调用方应起 goroutine 执行
// PrecomputeAllRelations；已有 done/活跃任务返回 false。
func (r *Repo) StartRelationComputeIfNeeded() (bool, error) {
	logger := zap.L()
	logger.Debug("enter (Repo).StartRelationComputeIfNeeded")
	defer logger.Debug("exit (Repo).StartRelationComputeIfNeeded")
	p, err := r.RelationProgress()
	if err != nil {
		return false, err
	}
	if p.Status == "done" {
		return false, nil
	}
	if p.Status == "running" && time.Now().Unix()-r.progressUpdatedAt() < 600 {
		return false, nil // 活跃任务在跑（本进程或其他进程）
	}
	g, err := r.cachedRelationGraph()
	if err != nil {
		return false, err
	}
	return r.beginRelationCompute(len(g.tables()))
}

// relTypeRank 关联类型优先级（聚合去重用）：fk > query > write > read。
func relTypeRank(t string) int {
	switch t {
	case domain.RelationFK:
		return 3
	case domain.RelationQuery:
		return 2
	case domain.RelationWrite:
		return 1
	default:
		return 0
	}
}

// MaxRelationHops 关系跳数上限默认值（Q195/Q196：6-10 跳长链为噪音失真）。
const MaxRelationHops = 4

// DefaultRelationHops 默认跳数上限（引用 domain 版，当前设定值 4/4/4）。
var DefaultRelationHops = domain.DefaultRelationHops

// dedupRelationNoise 关系降噪（Q195/Q196/Q197，全部 relations 出口统一应用——
// 缓存命中路径也过一遍，保证旧缓存同样被降噪）：
// ① 跳数上限：按类型取 h（0=不限制）——query 长链同样失真，
//    需要查看长链时设 Query=0（--include-long-query）
// ② 同源写/间接读按 from字段→to表 聚合：同一 from 字段流入同一 to 表
//    的多列（全列 INSERT/UPDATE 的列爆炸，如 atoms.aliases →
//    knowledge_graphs 的 13 列各一条）只保留 hops 最小一条；
//    query 保持列级（键关联每列独立有意义）。
// 输出保持输入顺序（第一条位次，后续 hops 更小者替换值）。
func dedupRelationNoise(rels []*domain.TableRelation, h domain.RelationHops) []*domain.TableRelation {
	// Q208：无快速路径（曾 `len(rels) < 2 直接返回`）——跳数上限过滤
	// 对单条同样生效（单表查询只有 1 条长链时曾被跳过过滤）
	seen := map[string]*domain.TableRelation{}
	var order []string
	for _, r := range rels {
		// 兜底防御（Q198）：自关联（同表字段间值流）不属于表间关联语义。
		// 主机制在 BFS 终点判定已排除（rg_relationsfor.go / rg_sql.go 的
		// otherTable == table continue），此处防未来路径/桥接回归
		if r.FromTable == r.ToTable {
			continue
		}
		limit := h.Read
		switch r.Type {
		case domain.RelationFK:
			limit = 0 // Q218：fk 值流已验证，默认不限跳（真实链 11 跳可显示）
		case domain.RelationQuery:
			limit = h.Query
		case domain.RelationWrite:
			limit = h.Write
		}
		if limit > 0 && r.Hops > limit {
			continue
		}
		var key string
		if r.Type == domain.RelationFK || r.Type == domain.RelationQuery {
			// Q218：fk/query 列级独立（键关联每列独立有意义）
			key = r.FromTable + "|" + r.FromCol + "|" + r.ToTable + "|" + r.ToCol
		} else if r.Type == domain.RelationWrite && strings.HasSuffix(r.ToCol, "id") {
			// Q202b：id 结尾列（外键列 res_id/role_id）不聚合——每个外键
			// 列是独立真实关联（rbac_role.id → res_id 与 role_id 都要）；
			// 非外键列（全列 INSERT 的列爆炸）才按 字段→表 聚合
			key = r.FromTable + "|" + r.FromCol + "|" + r.ToTable + "|" + r.ToCol
		} else {
			key = r.FromTable + "|" + r.FromCol + "|" + r.ToTable // 字段→表聚合
		}
		ex, ok := seen[key]
		if !ok {
			order = append(order, key)
			seen[key] = r
		} else if r.Hops < ex.Hops {
			seen[key] = r
		}
	}
	out := make([]*domain.TableRelation, 0, len(order))
	for _, k := range order {
		out = append(out, seen[k])
	}
	return out
}

// GetTableColumns 按表名聚合列虚拟节点（query table）：Name=表（整表行）
// 或 表.列（Q97 持久化映射）；每列带写入方（summary_io 入边 source 值节点
// 的所属函数与行号）。读取方（出边）通常为空——SELECT 读路径未解析。
func (r *Repo) GetTableColumns(table string) ([]*domain.TableColumn, error) {
	logger := zap.L()
	logger.Debug("enter (Repo).GetTableColumns")
	defer logger.Debug("exit (Repo).GetTableColumns")
	rows, err := r.Query(`SELECT id, name, line_start, properties
		FROM nodes WHERE kind = 'field_access'
		  AND json_extract(properties, '$.is_external') = 'true'
		  AND (name = ? OR name LIKE ?)
		ORDER BY name, id`, table, table+".%")
	if err != nil {
		return nil, err
	}
	// 先收完外层行再关（SQLite 单连接：迭代中开新 Query 会挂起）
	type rowT struct {
		id, name, access string
		line             int
	}
	var raw []rowT
	for rows.Next() {
		var id, name, props string
		var line int
		if err := rows.Scan(&id, &name, &line, &props); err != nil {
			return nil, err
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(props), &m); err != nil {
			return nil, err
		}
		access := ""
		if a, ok := m["access_kind"].(string); ok {
			access = a
		}
		raw = append(raw, rowT{id: id, name: name, access: access, line: line})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()

	cols := map[string]*domain.TableColumn{}
	var order []string
	for _, rt := range raw {
		col, ok := cols[rt.name]
		if !ok {
			col = &domain.TableColumn{Name: rt.name, Access: rt.access, LineStart: rt.line}
			cols[rt.name] = col
			order = append(order, rt.name)
		}

		ws, err := r.Query(`SELECT source_id, json_extract(metadata, '$.line_num')
			FROM edges WHERE target_id = ? AND kind = 'summary_io'`, rt.id)
		if err != nil {
			return nil, err
		}
		for ws.Next() {
			var src string
			var ln sql.NullFloat64
			if err := ws.Scan(&src, &ln); err != nil {
				ws.Close()
				return nil, err
			}

			funcID := src
			if i := strings.LastIndex(src, "#"); i >= 0 {
				funcID = src[:i]
			}

			line := rt.line
			if ln.Valid {
				line = int(ln.Float64)
			}
			col.Writers = append(col.Writers, domain.TableEndpoint{
				FuncID:   funcID,
				FuncName: shortNameFromID(funcID),
				Line:     line,
			})
		}
		ws.Close()

		if rt.access == "read" {
			rs, err := r.Query(`SELECT target_id, json_extract(metadata, '$.line_num')
				FROM edges WHERE source_id = ? AND kind = 'summary_io'`, rt.id)
			if err != nil {
				return nil, err
			}
			for rs.Next() {
				var tgt string
				var ln sql.NullFloat64
				if err := rs.Scan(&tgt, &ln); err != nil {
					rs.Close()
					return nil, err
				}
				funcID := tgt
				if i := strings.LastIndex(tgt, "#"); i >= 0 {
					funcID = tgt[:i]
				}
				line := rt.line
				if ln.Valid {
					line = int(ln.Float64)
				}
				col.Readers = append(col.Readers, domain.TableEndpoint{
					FuncID:   funcID,
					FuncName: shortNameFromID(funcID),
					Line:     line,
				})
			}
			rs.Close()
		}
	}
	out := make([]*domain.TableColumn, 0, len(order))
	for _, name := range order {
		out = append(out, cols[name])
	}
	return out, nil
}

// GetAllTableColumns ER 图列数据源（/api/er）：一次查询全库外部表列
// （表.列 形态，过滤与 GetTables 一致：is_external + gorm/sql/xorm），
// 按列名排序去重（同名列多节点首个保留）。不带 writers/readers 明细
// （ER 图不需要，避免逐表 N+1 查询）。
func (r *Repo) GetAllTableColumns() ([]*domain.TableColumn, error) {
	logger := zap.L()
	logger.Debug("enter (Repo).GetAllTableColumns")
	defer logger.Debug("exit (Repo).GetAllTableColumns")
	rows, err := r.Query(`SELECT name, line_start, properties FROM nodes
		WHERE kind = 'field_access'
		  AND json_extract(properties, '$.is_external') = 'true'
		  AND json_extract(properties, '$.type_string') IN ('gorm', 'sql', 'xorm')
		  AND name LIKE '%.%' AND name NOT LIKE '%.%.%'
		ORDER BY name, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	var out []*domain.TableColumn
	for rows.Next() {
		var name, props string
		var line int
		if err := rows.Scan(&name, &line, &props); err != nil {
			return nil, err
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		var m map[string]any
		if err := json.Unmarshal([]byte(props), &m); err != nil {
			return nil, err
		}
		access := ""
		if a, ok := m["access_kind"].(string); ok {
			access = a
		}
		out = append(out, &domain.TableColumn{Name: name, Access: access, LineStart: line})
	}
	return out, rows.Err()
}
