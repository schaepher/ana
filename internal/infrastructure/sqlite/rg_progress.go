package sqlite

import (
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
