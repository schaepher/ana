package sqlite

import "github.com/schaepher/codeintel/internal/domain"

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
		rels = []*domain.TableRelation{}
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
