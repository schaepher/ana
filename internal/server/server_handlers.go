package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/schaepher/codeintel/internal/action"
	"github.com/schaepher/codeintel/internal/domain"
	"github.com/schaepher/codeintel/internal/logging"
	"go.uber.org/zap"
)

// handleSearch 全库符号搜索（名称/ID/文件模糊匹配，上限 50）。
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		writeErr(w, http.StatusBadRequest, "missing q")
		return
	}
	found, err := s.acts.Search(q)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	nodes := make([]NodeJSON, 0, len(found))
	for _, n := range found {
		nodes = append(nodes, nodeToJSON(n))
	}
	writeJSON(w, map[string]any{"nodes": nodes})
}

// handleModuleCalls 模块间调用（field_trace.md §21.3）：HTTP JSON 透出
// action.ModuleCalls（grpc + http，transport 标注）——前端模块视图数据源。
func (s *Server) handleModuleCalls(w http.ResponseWriter, r *http.Request) {
	calls, err := s.acts.ModuleCalls("")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if calls == nil {
		calls = []action.ModuleCall{}
	}
	writeJSON(w, map[string]any{"calls": calls})
}

// handleER 数据库 ER 图（/api/er）：全库外部表 + 各表列清单 + 表间关联
// （query 键关联高置信 / write 同源中置信 / read 间接低置信）——
// 前端 er.html 数据源。
func (s *Server) handleER(w http.ResponseWriter, r *http.Request) {
	// 跳数上限参数（Q197，网页版可配置）：q_hops/w_hops/r_hops，
	// 缺省或非法用默认 4；0 = 不限制
	h := domain.DefaultRelationHops
	for _, kv := range []struct {
		key string
		dst *int
	}{{"q_hops", &h.Query}, {"w_hops", &h.Write}, {"r_hops", &h.Read}} {
		if v, err := strconv.Atoi(r.URL.Query().Get(kv.key)); err == nil && v >= 0 {
			*kv.dst = v
		}
	}
	s.acts.SetRelationHops(h)
	// Q209/Q210：加载粒度三态——
	//   ?table=X      只返回该表相关 relations（双击展开按需加载，单表 BFS）
	//   skip_relations=1  只返回表清单（首次加载，不触发任何 BFS）
	//   缺省           全量 relations（全图画线开关）
	var data *domain.ERData
	var err error
	switch {
	case r.URL.Query().Get("table") != "":
		data, err = s.acts.ERTable(r.URL.Query().Get("table"))
	case r.URL.Query().Get("skip_relations") == "1":
		data, err = s.acts.ERTables()
	default:
		data, err = s.acts.ER()
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if data.Tables == nil {
		data.Tables = []domain.ERTable{}
	}
	if data.Relations == nil {
		data.Relations = []*domain.TableRelation{}
	}
	writeJSON(w, data)
}

// handleIncremental 增量构建自动触发（field_trace.md §20.1）：
// POST /incremental（无负载，serve 已绑定 repo）→ 202 + 异步执行；
// 执行中再请求 → 409（单写者）；未配置 buildFn → 404（提示先 init）。
func (s *Server) handleIncremental(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	s.buildMu.Lock()
	if s.buildFn == nil {
		s.buildMu.Unlock()
		writeErr(w, http.StatusNotFound, "serve 未配置增量构建（先 codeintel init 构建索引）")
		return
	}
	if s.building {
		s.buildMu.Unlock()
		writeErr(w, http.StatusConflict, "增量构建进行中")
		return
	}
	s.building = true
	s.buildMu.Unlock()
	buildFn := s.buildFn
	go func() {
		defer func() {
			s.buildMu.Lock()
			s.building = false
			s.buildMu.Unlock()
		}()
		buildID, err := buildFn()
		logger := logging.FromContext(s.ctx)
		if err != nil {
			logger.Error("增量构建失败", zap.String("build_id", buildID), zap.Error(err))
			return
		}
		logger.Info("增量构建完成", zap.String("build_id", buildID))
	}()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]any{"status": "accepted"})
}
func writeJSON(w http.ResponseWriter, v any) {
	logger := zap.L()
	logger.Debug("enter writeJSON")
	defer logger.Debug("exit writeJSON")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		fmt.Printf("write json: %v\n", err)
	}
}
func writeErr(w http.ResponseWriter, code int, msg string) {
	logger := zap.L()
	logger.Debug("enter writeErr")
	defer logger.Debug("exit writeErr")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// handleRoots 返回顶层入口节点（main 入口 + HTTP/gRPC 服务入口）。
func (s *Server) handleRoots(w http.ResponseWriter, r *http.Request) {
	logger := logging.FromContext(s.ctx)
	logger.Debug("enter (Server).handleRoots")
	defer logger.Debug("exit (Server).handleRoots")
	roots, err := s.acts.Roots()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	nodes := make([]NodeJSON, 0, len(roots))
	for _, n := range roots {
		nodes = append(nodes, nodeToJSON(n))
	}
	writeJSON(w, map[string]any{"nodes": nodes})
}

// handleExpand 返回某节点的一级邻居（双向）。
func (s *Server) handleExpand(w http.ResponseWriter, r *http.Request) {
	logger := logging.FromContext(s.ctx)
	logger.Debug("enter (Server).handleExpand")
	defer logger.Debug("exit (Server).handleExpand")
	id := r.URL.Query().Get("id")
	if id == "" {
		writeErr(w, http.StatusBadRequest, "missing id")
		return
	}
	cur, facts, neighbors, err := s.acts.Expand(domain.CanonicalID(id))
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "symbol not found: "+id)
		} else {
			writeErr(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	edges := make([]EdgeJSON, 0, len(facts))
	for _, f := range facts {
		e := EdgeJSON{
			Source: string(f.SourceID),
			Target: string(f.TargetID),
			Kind:   string(f.Kind),
		}
		if f.SourceID == cur.ID {
			e.Direction = "out"
		} else {
			e.Direction = "in"
		}
		if ln, ok := f.Metadata["line_num"].(float64); ok {
			e.Line = int(ln)
		}
		if len(f.Metadata) > 0 {
			e.Metadata = f.Metadata
		}
		edges = append(edges, e)
	}

	nodes := make([]NodeJSON, 0, len(neighbors))
	for _, n := range neighbors {
		nodes = append(nodes, nodeToJSON(n))
	}
	writeJSON(w, map[string]any{
		"node":      nodeToJSON(cur),
		"neighbors": nodes,
		"edges":     edges,
	})
}

// nodeToJSON 转换节点为前端格式；roots 场景补充入口标记。
func nodeToJSON(n *domain.CodeEntity) NodeJSON {
	logger := zap.L()
	logger.Debug("enter nodeToJSON")
	defer logger.Debug("exit nodeToJSON")
	j := NodeJSON{
		ID:        string(n.ID),
		Name:      n.Name,
		Kind:      string(n.Kind),
		File:      n.FilePath,
		Line:      n.LineStart,
		Signature: n.Signature(),
	}
	if n.Name == "main" && n.Kind == domain.KindFunction {
		j.Flags = append(j.Flags, "main")
	}
	if n.Property("framework") == "true" {
		j.Flags = append(j.Flags, "framework")
	}
	if n.Property("serves_http") == "true" {
		j.Flags = append(j.Flags, "http")
	}
	if n.Property("serves_grpc") == "true" {
		j.Flags = append(j.Flags, "grpc")
	}
	j.Type = n.Property("type_string")
	j.FullPath = n.Property("full_path")
	j.FuncName = shortFuncName(n.Property("func_id"))
	j.DocComment = n.Property("doc_comment")
	j.Message = n.Property("message")
	j.Date = n.Property("date")
	if raw, ok := n.Properties["fields"].([]any); ok {
		for _, f := range raw {
			m, ok := f.(map[string]any)
			if !ok {
				continue
			}
			j.Fields = append(j.Fields, NodeFieldJSON{
				Name: fmt.Sprint(m["name"]),
				Type: fmt.Sprint(m["type"]),
			})
		}
	}
	return j
}
