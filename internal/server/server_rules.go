package server

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/schaepher/codeintel/internal/domain"
	"github.com/schaepher/codeintel/internal/logging"
)

// handleRules 用户连线规则（Q226，ER 页面配置规则的后端端点）：
//
//	GET    /api/rules        规则列表 {"rules": [RelationRule...]}
//	POST   /api/rules        添加规则（JSON body：from_table?/from_col/
//	                         to_table/to_col?）→ 201 {"id": n}
//	DELETE /api/rules?id=N   删除规则 → {"ok": true}
//
// 规则存 relation_rules 表（clean/reindex 保留），读取期与推断关系
// 合并（/api/er 响应自动含规则生成的 fk 线）——添加/删除后前端重新
// 拉取 /api/er 即生效，无需 reindex。
func (s *Server) handleRules(w http.ResponseWriter, r *http.Request) {
	logger := logging.FromContext(s.ctx)
	logger.Debug("enter (Server).handleRules")
	switch r.Method {
	case http.MethodGet:
		rules, err := s.acts.ListRelationRules()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"rules": rules})
	case http.MethodPost:
		var rule domain.RelationRule
		if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
			http.Error(w, "请求体无效: "+err.Error(), http.StatusBadRequest)
			return
		}
		// 与 CLI 语义一致：目标列省略默认 id；FromTable 空 = 模式规则
		if rule.ToCol == "" {
			rule.ToCol = "id"
		}
		id, err := s.acts.AddRelationRule(rule)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, map[string]any{"id": id})
	case http.MethodDelete:
		raw := r.URL.Query().Get("id")
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			http.Error(w, "参数 id 无效: "+raw, http.StatusBadRequest)
			return
		}
		if err := s.acts.RemoveRelationRule(id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	default:
		w.Header().Set("Allow", "GET, POST, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
