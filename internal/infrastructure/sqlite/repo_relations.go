package sqlite

import (
	"sort"
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

// GetTableColumns 按表名聚合列虚拟节点（query table）：Name=表（整表行）
// 或 表.列（Q97 持久化映射）；每列带写入方（summary_io 入边 source 值节点
// 的所属函数与行号）。读取方（出边）通常为空——SELECT 读路径未解析。

// GetAllTableColumns ER 图列数据源（/api/er）：一次查询全库外部表列
// （表.列 形态，过滤与 GetTables 一致：is_external + gorm/sql/xorm），
// 按列名排序去重（同名列多节点首个保留）。不带 writers/readers 明细
// （ER 图不需要，避免逐表 N+1 查询）。
