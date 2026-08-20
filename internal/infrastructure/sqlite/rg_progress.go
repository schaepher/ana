package sqlite

import (
	"sort"
	"time"

	"github.com/schaepher/codeintel/internal/domain"
	"go.uber.org/zap"
)

// Q228：全量 relations 计算进度层。
//
// 计算入口：`codeintel precompute relations` 命令（前台同步执行）；
// 查询端（CLI query relations --all / serve /api/er 全量）先查
// relation_progress——done 才返回数据；running/pending 返回进度
// （前端轮询展示）。serve 端对未知/过期状态自动启动后台计算兜底
// （进程内单例 + db 状态抢占防跨进程重复）。

// ErrRelationInProgress 全量计算未完成（domain 哨兵）——查询端据
// Status/Done/Total 返回进度（前端轮询），不现场计算。
var ErrRelationInProgress = domain.ErrRelationInProgress

// RelationProgress 读当前计算进度（无记录 = 未知，返回零值）。
func (r *Repo) RelationProgress() (domain.RelationProgress, error) {
	logger := zap.L()
	logger.Debug("enter (Repo).RelationProgress")
	defer logger.Debug("exit (Repo).RelationProgress")
	bid := r.cacheKey()
	if bid == "" {
		return domain.RelationProgress{}, nil
	}
	rows, err := r.Query(`SELECT status, done_count, total_count FROM relation_progress
		WHERE build_id = ?`, bid)
	if err != nil {
		return domain.RelationProgress{}, err
	}
	defer rows.Close()
	var p domain.RelationProgress
	if rows.Next() {
		if err := rows.Scan(&p.Status, &p.Done, &p.Total); err != nil {
			return domain.RelationProgress{}, err
		}
	}
	return p, rows.Err()
}

// progressUpdatedAt 读最近一次进度更新时间（活跃任务判定用）。
func (r *Repo) progressUpdatedAt() int64 {
	logger := zap.L()
	logger.Debug("enter (Repo).progressUpdatedAt")
	defer logger.Debug("exit (Repo).progressUpdatedAt")
	bid := r.cacheKey()
	if bid == "" {
		return 0
	}
	rows, err := r.Query(`SELECT updated_at FROM relation_progress WHERE build_id = ?`, bid)
	if err != nil {
		return 0
	}
	defer rows.Close()
	var ts int64
	if rows.Next() {
		_ = rows.Scan(&ts)
	}
	return ts
}

// beginRelationCompute 抢占计算任务（Q228）：已 done 或 running 且
// 10 分钟内更新过（视为有任务在跑，跨进程防重复）→ 不抢占。
// 成功置 running 并返回 total。
func (r *Repo) beginRelationCompute(total int) (bool, error) {
	logger := zap.L()
	logger.Debug("enter (Repo).beginRelationCompute")
	defer logger.Debug("exit (Repo).beginRelationCompute")
	bid := r.cacheKey()
	if bid == "" {
		return false, nil
	}
	now := time.Now().Unix()
	// 原子 UPDATE：仅当非 running 或 running 已过期（>10min）时抢占成功
	res, err := r.Exec(`UPDATE relation_progress SET status = 'running',
			done_count = 0, total_count = ?, updated_at = ?
		WHERE build_id = ? AND (status != 'running' OR updated_at < ?)`,
		total, now, bid, now-600)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return true, nil
	}
	// 无记录（从未计算）→ INSERT
	res, err = r.Exec(`INSERT OR IGNORE INTO relation_progress
		(build_id, status, done_count, total_count, updated_at)
		VALUES (?, 'running', 0, ?, ?)`, bid, total, now)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return true, nil
	}
	return false, nil // 已有 running 任务（其他进程/goroutine）
}

// updateRelationProgress 推进进度（每批表写一次，避免逐表写库）。
func (r *Repo) updateRelationProgress(done int) error {
	logger := zap.L()
	logger.Debug("enter (Repo).updateRelationProgress")
	defer logger.Debug("exit (Repo).updateRelationProgress")
	bid := r.cacheKey()
	if bid == "" {
		return nil
	}
	_, err := r.Exec(`UPDATE relation_progress SET done_count = ?, updated_at = ?
		WHERE build_id = ?`, done, time.Now().Unix(), bid)
	return err
}

// finishRelationCompute 计算完成：置 done（缓存写入由调用方先完成）。
func (r *Repo) finishRelationCompute(total int) error {
	logger := zap.L()
	logger.Debug("enter (Repo).finishRelationCompute")
	defer logger.Debug("exit (Repo).finishRelationCompute")
	bid := r.cacheKey()
	if bid == "" {
		return nil
	}
	_, err := r.Exec(`UPDATE relation_progress SET status = 'done',
		done_count = ?, updated_at = ? WHERE build_id = ?`,
		total, time.Now().Unix(), bid)
	return err
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

// MaxRelationHops 关系跳数上限默认值（Q195/Q196：6-10 跳长链为噪音失真）。
