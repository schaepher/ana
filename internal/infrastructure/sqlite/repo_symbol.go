package sqlite

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/schaepher/codeintel/internal/domain"
	"go.uber.org/zap"
)

// GetSymbol 按 Canonical ID 查询符号。
func (r *Repo) GetSymbol(id domain.CanonicalID) (*domain.CodeEntity, error) {
	logger := zap.L()
	logger.Debug("enter (Repo).GetSymbol")
	defer logger.Debug("exit (Repo).GetSymbol")
	return scanNode(r.QueryRow(
		"SELECT id, kind, name, file_path, line_start, line_end, properties FROM nodes WHERE id = ?",
		string(id)))
}

// GetSymbolByName 按名称查找：先精确匹配，无结果时退化为模糊匹配
// （CLI 按名查找用）。
func (r *Repo) GetSymbolByName(name string) ([]*domain.CodeEntity, error) {
	logger := zap.L()
	logger.Debug("enter (Repo).GetSymbolByName")
	defer logger.Debug("exit (Repo).GetSymbolByName")
	// 排除字段追溯的内部节点（field_access / ssa_value / external_summary）：
	// 它们是字段访问点与 SSA 临时值，不是可搜索的代码符号（field_trace.md §4）
	const exclude = "kind NOT IN ('field_access','ssa_value','external_summary')"

	rows, err := r.Query(
		"SELECT id, kind, name, file_path, line_start, line_end, properties FROM nodes WHERE name = ? AND "+exclude+" ORDER BY name LIMIT 50",
		name)
	if err != nil {
		return nil, err
	}
	nodes, err := scanNodes(rows)
	if err != nil || len(nodes) > 0 {
		return nodes, err
	}

	rows, err = r.Query(
		"SELECT id, kind, name, file_path, line_start, line_end, properties FROM nodes WHERE (name LIKE ? OR id LIKE ?) AND "+exclude+" ORDER BY name LIMIT 50",
		"%"+name+"%", "%"+name+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanNodes(rows)
}
func scanNode(row *sql.Row) (*domain.CodeEntity, error) {
	logger := zap.L()
	logger.Debug("enter scanNode")
	defer logger.Debug("exit scanNode")
	n := &domain.CodeEntity{}
	var props string
	err := row.Scan(&n.ID, &n.Kind, &n.Name, &n.FilePath, &n.LineStart, &n.LineEnd, &props)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if props != "" {
		var m map[string]any
		if err := json.Unmarshal([]byte(props), &m); err == nil {
			n.Properties = m
		}
	}
	return n, nil
}
func scanNodes(rows *sql.Rows) ([]*domain.CodeEntity, error) {
	logger := zap.L()
	logger.Debug("enter scanNodes")
	defer logger.Debug("exit scanNodes")
	var out []*domain.CodeEntity
	for rows.Next() {
		n := &domain.CodeEntity{}
		var props string
		if err := rows.Scan(&n.ID, &n.Kind, &n.Name, &n.FilePath, &n.LineStart, &n.LineEnd, &props); err != nil {
			return nil, err
		}
		if props != "" {
			var m map[string]any
			if err := json.Unmarshal([]byte(props), &m); err == nil {
				n.Properties = m
			}
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// shortStructID 压缩 struct ID 便于日志（保留 pkg 末段与类型名）。
func shortStructID(id domain.CanonicalID) string {
	s := strings.TrimPrefix(string(id), "symbol:go:")
	if i := strings.LastIndex(s, ":"); i >= 0 {
		if j := strings.LastIndex(s[:i], "/"); j >= 0 {
			return s[j+1:]
		}
		return s
	}
	return s
}

// shortMethodID 压缩方法 ID 便于日志（保留类型名与方法名）。
func shortMethodID(id string) string {
	s := strings.TrimPrefix(id, "symbol:go:")
	if i := strings.Index(s, ":("); i >= 0 {
		return s[i+1:]
	}
	return s
}

// structIDFromMethod 将方法 ID（symbol:go:<pkg>:(T).M）还原为所属 struct ID
// （symbol:go:<pkg>:T）。
func structIDFromMethod(methodID string) (string, bool) {
	s := strings.TrimPrefix(methodID, "symbol:go:")
	i := strings.Index(s, ":(")
	if i < 0 {
		return "", false
	}
	rest := s[i+2:]
	j := strings.Index(rest, ").")
	if j < 0 {
		return "", false
	}
	return "symbol:go:" + s[:i] + ":" + rest[:j], true
}

// shortNameFromID 从 canonical ID 提取函数短名（symbol:go:<pkg>:(T).m → (T).m）。
func shortNameFromID(id string) string {
	if i := strings.LastIndex(id, ":"); i >= 0 {
		return id[i+1:]
	}
	return id
}
