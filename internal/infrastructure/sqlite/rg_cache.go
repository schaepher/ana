package sqlite

import (
	"time"

	"github.com/schaepher/codeintel/internal/domain"
	"go.uber.org/zap"
)

// relationsAlgoVersion relations 推断逻辑版本（Q199：跨函数 write 丢弃；
// Q200：缓存键版本化后 query 恢复验证；Q205：filterFKNoise 的 id 起点
// 过滤不再作用于 query 键关联；Q208：缓存改存未过滤全量（hops 过滤
// 读取期行为））——并入缓存键，分析逻辑变更后
// 旧缓存自动失效，无需手动 clean/reindex。**每次修改 relations 推断
// 逻辑（rg_*.go / relationsFor*）必须递增此版本**，否则旧缓存残留。
const relationsAlgoVersion = "q210" // Q220b：BFS 阻断 error 类型值（元组 (T, error) 共享节点假链）

// cacheKey 缓存键 = build_id + 分析逻辑版本（build_id 变化或逻辑版本
// 变化都失效）。
func (r *Repo) cacheKey() string {
	bid := r.currentBuildID()
	if bid == "" {
		return ""
	}
	return bid + ":" + relationsAlgoVersion
}

// cachedRelationGraph 进程内关系图缓存（任务 #165）：serve 进程内
// 单表展开/全量查询复用内存图（loadRelationGraph 每次 500ms+）。
// 语义：
//   - 键 = cacheKey（build_id + 分析逻辑版本）——增量构建写新 build_id
//     或 rg_*.go 逻辑变更都自动失效重载；无 build_metadata 时不缓存
//     （与 relation_candidates 同语义）
//   - 图对象只读共享：relationsFor/BFS 纯读（Go map 并发读安全），
//     RWMutex 只保护缓存槽本身
//   - 大图不缓存（shouldCacheGraph 阈值，防 ~100MB 常驻膨胀）——
//     超限图每次请求重载；auto 模式下超限图本就走 SQL 路径（不加载
//     内存图），此分支仅 --memory full 强制时可达
func (r *Repo) cachedRelationGraph() (*relationGraph, error) {
	logger := zap.L()
	key := r.cacheKey()
	r.graphMu.RLock()
	if key != "" && r.graphCacheKey == key && r.graphCache != nil {
		g := r.graphCache
		r.graphMu.RUnlock()
		logger.Info("relations graph cache hit", zap.String("key", key))
		return g, nil
	}
	r.graphMu.RUnlock()

	if key == "" {
		// 无 build_metadata：不缓存，每次现场加载（与 relation_candidates 同语义）
		return loadRelationGraph(r)
	}
	// double-checked locking：并发首请求在写锁内串行化加载——等待者
	// 二次检查命中后直接复用，避免 N 个请求各加载一次全图
	r.graphMu.Lock()
	defer r.graphMu.Unlock()
	if r.graphCacheKey == key && r.graphCache != nil {
		logger.Info("relations graph cache hit", zap.String("key", key))
		return r.graphCache, nil
	}
	start := time.Now()
	g, err := loadRelationGraph(r)
	if err != nil {
		return nil, err
	}
	logger.Info("relations graph loaded",
		zap.String("key", key), zap.Duration("elapsed", time.Since(start)))
	// 大图（超安全阈值）不缓存：本次调用返回新图，槽位留空——
	// 后续请求每次重载（auto 模式超限图本就走 SQL 路径，仅
	// --memory full 强制可达此分支）
	if shouldCacheGraph(g, relationGraphMaxNodes, relationGraphMaxEdges) {
		r.graphCacheKey = key
		r.graphCache = g
	}
	return g, nil
}

// shouldCacheGraph 进程内图缓存阈值判定：节点数或边数超过上限则不缓存
// （每次请求重载）。独立纯函数便于单测。
func shouldCacheGraph(g *relationGraph, maxNodes, maxEdges int) bool {
	if len(g.nodes) > maxNodes {
		return false
	}
	edgeCount := 0
	for _, out := range g.allOut {
		edgeCount += len(out)
	}
	return edgeCount <= maxEdges
}

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
	buildID := r.cacheKey()
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
		rels = []*domain.TableRelation{}
	}
	return rels, true
}

// loadAllRelationCandidates 读全库缓存（Q177）：该 build_id 的 from_table
// 覆盖与当前全部表集合一致才算完整（marker 行保证无关联表也有记录）——
// 命中直接返回，跳过全图加载与逐表 BFS。
func (r *Repo) loadAllRelationCandidates(buildID string) ([]*domain.TableRelation, bool) {
	// 完整性判定：全量 marker 行（from_table=''，--all 重建时写入）存在
	// 即完整——不依赖当前表集合（图手动变更不影响已构建的 build_id 缓存）
	var cnt int
	if err := r.QueryRow(`SELECT COUNT(*) FROM relation_candidates
		WHERE build_id = ? AND from_table = ''`, buildID).Scan(&cnt); err != nil || cnt == 0 {
		return nil, false
	}
	rows, err := r.Query(`SELECT from_table, from_col, to_table, to_col, hops, type
		FROM relation_candidates WHERE build_id = ? AND from_col <> '' AND from_table <> ''
		ORDER BY from_table, from_col, to_table, to_col`, buildID)
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
		rels = []*domain.TableRelation{}
	}
	return rels, true
}

// saveRelationCandidates 写单表缓存（覆盖旧行；rels 为空时写 marker 行，
// 标记"该表已计算过、无关联"，避免每次查询重算）。
func (r *Repo) saveRelationCandidates(table string, rels []*domain.TableRelation) {
	buildID := r.cacheKey()
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
// 写全量 marker（from_table=''，loadAllRelationCandidates 完整性判定用）、
// 每张表写 marker（含无关联表），再写全部真实关联行。
func (r *Repo) rebuildRelationCandidates(rels []*domain.TableRelation, tables []string) {
	buildID := r.cacheKey()
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
	// 全量 marker（from_table=''）：loadAllRelationCandidates 完整性判定
	_, _ = tx.Exec(`INSERT OR REPLACE INTO relation_candidates
		(build_id, from_table, from_col, to_table, to_col, hops, type)
		VALUES (?, '', '', '', '', 0, '')`, buildID)
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
