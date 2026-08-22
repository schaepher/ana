package server

import (
	"net/http"
	"strings"

	"github.com/schaepher/codeintel/internal/domain"
	"github.com/schaepher/codeintel/internal/logging"
)

// handleValueTrace 返回数据值全链（函数上下文分组渲染数据）。
// handleContext Q235-5：/api/context?node=<锚点>——一次调用拿全链
// 上下文（action.Context 编排；子查询部分降级，主字段失败 500）。
func (s *Server) handleContext(w http.ResponseWriter, r *http.Request) {
	logger := logging.FromContext(s.ctx)
	logger.Debug("enter (Server).handleContext")
	defer logger.Debug("exit (Server).handleContext")
	node := r.URL.Query().Get("node")
	if node == "" {
		writeErr(w, http.StatusBadRequest, "missing node")
		return
	}
	ctx, err := s.acts.Context(node)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, ctx)
}

func (s *Server) handleValueTrace(w http.ResponseWriter, r *http.Request) {
	logger := logging.FromContext(s.ctx)
	logger.Debug("enter (Server).handleValueTrace")
	defer logger.Debug("exit (Server).handleValueTrace")
	id := r.URL.Query().Get("id")
	if id == "" {
		writeErr(w, http.StatusBadRequest, "missing id")
		return
	}
	rows, err := s.acts.ValueTrace(domain.CanonicalID(id), 8, 0, false)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	rows, err = s.acts.TraceConditions(rows)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	flows := make([]FlowRowJSON, 0, len(rows))
	for _, row := range rows {
		edge := ""
		if i := strings.LastIndex(row.EdgeKinds, ","); i >= 0 {
			edge = row.EdgeKinds[i+1:]
		} else {
			edge = row.EdgeKinds
		}
		flows = append(flows, FlowRowJSON{
			ID:         string(row.ID),
			Name:       row.Name,
			Depth:      row.Depth,
			Dir:        row.Dir,
			EdgeKind:   edge,
			Line:       row.Line,
			Kind:       string(row.Kind),
			Access:     row.Access,
			FuncID:     row.FuncID,
			FuncName:   shortFuncName(row.FuncID),
			FullPath:   row.FullPath,
			Conditions: row.Conditions,
		})
	}
	writeJSON(w, map[string]any{"flows": flows})
}

// handleFlows 返回函数/方法节点内的完整字段数据流（文本树渲染数据）。
func (s *Server) handleFlows(w http.ResponseWriter, r *http.Request) {
	logger := logging.FromContext(s.ctx)
	logger.Debug("enter (Server).handleFlows")
	defer logger.Debug("exit (Server).handleFlows")
	id := r.URL.Query().Get("id")
	if id == "" {
		writeErr(w, http.StatusBadRequest, "missing id")
		return
	}
	rows, err := s.acts.Flows(domain.CanonicalID(id), 8)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	flows := make([]FlowRowJSON, 0, len(rows))
	for _, row := range rows {
		edge := ""
		if i := strings.LastIndex(row.EdgeKinds, ","); i >= 0 {
			edge = row.EdgeKinds[i+1:]
		} else {
			edge = row.EdgeKinds
		}
		flows = append(flows, FlowRowJSON{
			ID:       string(row.ID),
			Name:     row.Name,
			Depth:    row.Depth,
			Dir:      row.Dir,
			EdgeKind: edge,
			Line:     row.Line,
			Kind:     string(row.Kind),
			Access:   row.Access,
			FullPath: row.FullPath,
			FuncID:   row.FuncID,
			FuncName: shortFuncName(row.FuncID),
		})
	}
	writeJSON(w, map[string]any{"flows": flows})
}

// shortFuncName 从 canonical ID 提取函数短名（symbol:go:<pkg>:<name> → <name>，
// 方法保留 (T).m 形式）——字段访问/SSA 值节点所属函数的展示用。
func shortFuncName(funcID string) string {
	if i := strings.LastIndex(funcID, ":"); i >= 0 {
		return funcID[i+1:]
	}
	return funcID
}
