package sqlite

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/mattn/go-sqlite3"
	"github.com/schaepher/codeintel/internal/domain"
	"go.uber.org/zap"
)

// SaveBatch 在单个事务中保存节点与边（节点必须先于边插入以满足外键）。
func (r *Repo) SaveBatch(nodes []*domain.CodeEntity, edges []*domain.Fact) error {
	logger := zap.L()
	logger.Debug("enter (Repo).SaveBatch")
	defer logger.Debug("exit (Repo).SaveBatch")
	_, err := r.SaveBatchStats(nodes, edges, nil)
	return err
}

// SaveBatchStats 与 SaveBatch 相同，但返回批次统计（跳过的外键冲突边数），
// 并接受函数字段摘要行（function_field_summary）。
// 端点节点不存在的边（如 Git 追踪到 SCIP 未索引的文件）静默跳过，不中断构建。
func (r *Repo) SaveBatchStats(nodes []*domain.CodeEntity, edges []*domain.Fact,
	summaries []*domain.FunctionFieldSummary, origins ...[]*domain.SummaryOrigin) (*saveBatchResult, error) {
	logger := zap.L()
	logger.Debug("enter (Repo).SaveBatchStats")
	defer logger.Debug("exit (Repo).SaveBatchStats")
	result := &saveBatchResult{}
	allOrigins := []*domain.SummaryOrigin{}
	for _, os := range origins {
		allOrigins = append(allOrigins, os...)
	}
	if len(nodes) == 0 && len(edges) == 0 && len(summaries) == 0 && len(allOrigins) == 0 {
		return result, nil
	}
	tx, err := r.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	if len(nodes) > 0 {
		stmt, err := tx.Prepare(insertNodeSQL)
		if err != nil {
			return nil, fmt.Errorf("prepare node insert: %w", err)
		}
		for _, n := range nodes {
			props, err := marshalProps(n.Properties)
			if err != nil {
				stmt.Close()
				return nil, fmt.Errorf("marshal properties of %s: %w", n.ID, err)
			}
			if _, err := stmt.Exec(string(n.ID), string(n.Kind), n.Name, n.FilePath,
				n.LineStart, n.LineEnd, string(props)); err != nil {
				stmt.Close()
				return nil, fmt.Errorf("insert node %s: %w", n.ID, err)
			}
		}
		stmt.Close()
	}
	if len(edges) > 0 {
		stmt, err := tx.Prepare(insertEdgeSQL)
		if err != nil {
			return nil, fmt.Errorf("prepare edge insert: %w", err)
		}
		for _, e := range edges {
			meta, err := json.Marshal(e.Metadata)
			if err != nil {
				stmt.Close()
				return nil, fmt.Errorf("marshal metadata: %w", err)
			}
			if _, err := stmt.Exec(string(e.SourceID), string(e.TargetID), string(e.Kind),
				e.ToolSource, e.Confidence, string(meta)); err != nil {
				if isFKError(err) {

					result.FailedEdges = append(result.FailedEdges, e)
					continue
				}
				stmt.Close()
				return nil, fmt.Errorf("insert edge %s->%s (%s): %w", e.SourceID, e.TargetID, e.Kind, err)
			}
		}
		stmt.Close()
	}
	if len(summaries) > 0 {
		stmt, err := tx.Prepare(insertSummarySQL)
		if err != nil {
			return nil, fmt.Errorf("prepare summary insert: %w", err)
		}
		for _, s := range summaries {

			if _, err := stmt.Exec(string(s.FunctionID), s.AccessKind, s.FieldPath,
				s.InstancePath, s.LineStart, s.CodeSnippet); err != nil {
				if isFKError(err) {
					result.FailedSummaries = append(result.FailedSummaries, s)
					continue
				}
				stmt.Close()
				return nil, fmt.Errorf("insert summary %s %s %s: %w", s.FunctionID, s.AccessKind, s.FieldPath, err)
			}
		}
		stmt.Close()
	}
	if len(allOrigins) > 0 {
		stmt, err := tx.Prepare(`INSERT OR REPLACE INTO summary_origins
			(function_id, access_kind, field_path, call_line, callee_id)
			VALUES (?, ?, ?, ?, ?)`) // Q215：覆盖旧行（同 Q215 summary）
		if err != nil {
			return nil, fmt.Errorf("prepare origin insert: %w", err)
		}
		for _, o := range allOrigins {

			if o.FunctionID == "" || o.AccessKind == "" || o.FieldPath == "" {
				continue
			}
			if _, err := stmt.Exec(string(o.FunctionID), o.AccessKind, o.FieldPath,
				o.CallLine, string(o.CalleeID)); err != nil {
				if isFKError(err) {
					result.FailedOrigins = append(result.FailedOrigins, o)
					continue
				}
				stmt.Close()
				return nil, fmt.Errorf("insert origin %s %s %s: %w", o.FunctionID, o.AccessKind, o.FieldPath, err)
			}
		}
		stmt.Close()
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

// marshalProps 序列化节点属性；nil 映射为空对象（json_patch 需要对象操作数）。
func marshalProps(props map[string]any) ([]byte, error) {
	logger := zap.L()
	logger.Debug("enter marshalProps")
	defer logger.Debug("exit marshalProps")
	if props == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(props)
}

// isFKError 判断是否为外键约束错误（SQLITE_CONSTRAINT_FOREIGNKEY = 787）。
// go-sqlite3 的 sqlite3.Error 用 ExtendedCode 存扩展错误码。
func isFKError(err error) bool {
	logger := zap.L()
	logger.Debug("enter isFKError")
	defer logger.Debug("exit isFKError")
	var e sqlite3.Error
	if errors.As(err, &e) {
		return e.ExtendedCode == 787
	}
	return false
}

// SaveNode 保存单个节点（TD.md 4.2 接口）。
func (r *Repo) SaveNode(node *domain.CodeEntity) error {
	logger := zap.L()
	logger.Debug("enter (Repo).SaveNode")
	defer logger.Debug("exit (Repo).SaveNode")
	return r.SaveBatch([]*domain.CodeEntity{node}, nil)
}

// SaveEdges 保存边列表（TD.md 4.2 接口）。
func (r *Repo) SaveEdges(edges []*domain.Fact) error {
	logger := zap.L()
	logger.Debug("enter (Repo).SaveEdges")
	defer logger.Debug("exit (Repo).SaveEdges")
	return r.SaveBatch(nil, edges)
}

// DeleteByFile 删除某个文件的所有节点及其边（级联），用于增量构建。
func (r *Repo) DeleteByFile(filePath string) error {
	logger := zap.L()
	logger.Debug("enter (Repo).DeleteByFile")
	defer logger.Debug("exit (Repo).DeleteByFile")
	_, err := r.Exec("DELETE FROM nodes WHERE file_path = ?", filePath)
	if err != nil {
		return fmt.Errorf("delete nodes of file %s: %w", filePath, err)
	}
	return nil
}

// Counts 返回节点数与边数（构建报告用）。
func (r *Repo) Counts() (nodes, edges int, err error) {
	logger := zap.L()
	logger.Debug("enter (Repo).Counts")
	defer logger.Debug("exit (Repo).Counts")
	if err = r.QueryRow("SELECT COUNT(*) FROM nodes").Scan(&nodes); err != nil {
		return 0, 0, err
	}
	if err = r.QueryRow("SELECT COUNT(*) FROM edges").Scan(&edges); err != nil {
		return 0, 0, err
	}
	return nodes, edges, nil
}

// ResetGraphTables 清空图数据表（DROP + 重建）——全量重建语义
// （orchestrator.FullBuild 用）。比 DELETE 全表快（无逐行 WAL/索引
// 维护）且释放文件空间；build_metadata（构建记录）与未来配置表
// 保留。FK 顺序：先 DROP 子表（edges/function_field_summary）
// 再 DROP 父表（nodes）。
func (r *Repo) ResetGraphTables() error {
	logger := zap.L()
	logger.Debug("enter (Repo).ResetGraphTables")
	defer logger.Debug("exit (Repo).ResetGraphTables")
	tx, err := r.Begin()
	if err != nil {
		return fmt.Errorf("begin reset tx: %w", err)
	}
	defer tx.Rollback()
	for _, stmt := range []string{
		"DROP TABLE IF EXISTS edges",
		"DROP TABLE IF EXISTS function_field_summary",
		"DROP TABLE IF EXISTS nodes",
	} {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("reset %s: %w", stmt, err)
		}
	}

	if _, err := tx.Exec(schema); err != nil {
		return fmt.Errorf("rebuild schema: %w", err)
	}
	return tx.Commit()
}
