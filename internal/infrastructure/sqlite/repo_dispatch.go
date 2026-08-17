package sqlite

import (
	"encoding/json"

	"github.com/schaepher/codeintel/internal/domain"
	"go.uber.org/zap"
)

// GetIndirectWriteEdges 返回函数的 INDIRECT_WRITE 边（Q90 调用点回连：
// metadata 含 call_line / call_args）。
func (r *Repo) GetIndirectWriteEdges(funcID domain.CanonicalID) ([]*domain.Fact, error) {
	logger := zap.L()
	logger.Debug("enter (Repo).GetIndirectWriteEdges")
	defer logger.Debug("exit (Repo).GetIndirectWriteEdges")
	rows, err := r.Query(`SELECT source_id, target_id, kind, tool_source, confidence, metadata
		FROM edges WHERE source_id = ? AND kind = 'indirect_write'`, string(funcID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFacts(rows)
}

// GetDispatchEdges 返回接口类型的 dispatch_to 边（Q95：symbol 详情候选集）。
func (r *Repo) GetDispatchEdges(ifaceID domain.CanonicalID) ([]*domain.Fact, error) {
	logger := zap.L()
	logger.Debug("enter (Repo).GetDispatchEdges")
	defer logger.Debug("exit (Repo).GetDispatchEdges")
	rows, err := r.Query(`SELECT source_id, target_id, kind, tool_source, confidence, metadata
		FROM edges WHERE source_id = ? AND kind = 'dispatch_to'`, string(ifaceID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFacts(rows)
}

// GetDispatchTargets 返回全部 dispatch_to 边的候选实现 → 派发元数据
// （Q157 P1：value-trace 候选标注——链路混入多个接口候选实现时区分）。
func (r *Repo) GetDispatchTargets() (map[domain.CanonicalID]domain.DispatchMeta, error) {
	logger := zap.L()
	logger.Debug("enter (Repo).GetDispatchTargets")
	defer logger.Debug("exit (Repo).GetDispatchTargets")
	rows, err := r.Query(`SELECT target_id, confidence, metadata FROM edges WHERE kind = 'dispatch_to'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[domain.CanonicalID]domain.DispatchMeta{}
	for rows.Next() {
		var (
			tid    string
			conf   float64
			meta   []byte
			origin string
		)
		if err := rows.Scan(&tid, &conf, &meta); err != nil {
			return nil, err
		}
		if len(meta) > 0 && string(meta) != "null" {
			var m map[string]any
			if err := json.Unmarshal(meta, &m); err == nil {
				if o, ok := m["origin"].(string); ok {
					origin = o
				}
			}
		}
		out[domain.CanonicalID(tid)] = domain.DispatchMeta{Origin: origin, Confidence: conf}
	}
	return out, rows.Err()
}

// FindFieldReads 按 full_path 查字段读节点（③：写锚点的下游消费跳板——
// 同字段的读节点及其使用链）。
func (r *Repo) FindFieldReads(fullPath string) ([]*domain.CodeEntity, error) {
	logger := zap.L()
	logger.Debug("enter (Repo).FindFieldReads")
	defer logger.Debug("exit (Repo).FindFieldReads")
	rows, err := r.Query(`SELECT id, kind, name, file_path, line_start, line_end, properties
		FROM nodes WHERE kind = 'field_access'
		  AND json_extract(properties, '$.access_kind') = 'read'
		  AND json_extract(properties, '$.full_path') = ?
		ORDER BY line_start, id`, fullPath)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanNodes(rows)
}
