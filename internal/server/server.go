// Package server 提供图探索 HTTP 服务：/api/roots 初始入口、
// /api/expand 点击展开、静态前端页面（go:embed）。
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"strings"
	"sync"

	"github.com/schaepher/codeintel/internal/action"
	"github.com/schaepher/codeintel/internal/domain"
	"github.com/schaepher/codeintel/internal/logging"
	"go.uber.org/zap"
)

// Server 承载图查询 HTTP 接口。全部查询经 action 层调用仓储
// （CLI/HTTP 只做参数解析与结果 JSON 化）。
type Server struct {
	ctx  context.Context // 携带 root span，handler 日志由此取带链路信息的 logger
	acts *action.Actions
	web  fs.FS // 前端静态资源
	root string // 仓库根目录（/api/source 读源码用）

	// 增量构建（field_trace.md §20.1）：POST /incremental 异步触发；
	// buildFn 由 cli serve 组装（orchestrator.IncrementalBuild）
	buildFn  func() (string, error)
	buildMu  sync.Mutex
	building bool
}

// SetBuildFunc 配置增量构建函数（未配置时 /incremental 返回 404）。
func (s *Server) SetBuildFunc(fn func() (string, error)) {
	s.buildMu.Lock()
	defer s.buildMu.Unlock()
	s.buildFn = fn
}

// New 创建 Server。
func New(ctx context.Context, acts *action.Actions, webFS fs.FS, repoRoot string) *Server {
	logger := logging.FromContext(ctx)
	logger.Debug("enter New")
	defer logger.Debug("exit New")
	return &Server{ctx: ctx, acts: acts, web: webFS, root: repoRoot}
}

// Handler 返回 HTTP 处理器。
func (s *Server) Handler() http.Handler {
	logger := logging.FromContext(s.ctx)
	logger.Debug("enter (Server).Handler")
	defer logger.Debug("exit (Server).Handler")
	mux := http.NewServeMux()
	mux.HandleFunc("/api/roots", s.handleRoots)
	mux.HandleFunc("/api/search", s.handleSearch)
	mux.HandleFunc("/api/expand", s.handleExpand)
	mux.HandleFunc("/api/flows", s.handleFlows)
	mux.HandleFunc("/api/value-trace", s.handleValueTrace)
	mux.HandleFunc("/api/source", s.handleSource)
	mux.HandleFunc("/incremental", s.handleIncremental)
	mux.Handle("/", http.FileServer(http.FS(s.web)))
	return mux
}

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

// NodeJSON 节点输出格式。
type NodeJSON struct {
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	Kind       string          `json:"kind"`
	File       string          `json:"file,omitempty"`
	Line       int             `json:"line,omitempty"`
	Signature  string          `json:"signature,omitempty"`
	Type       string          `json:"type,omitempty"`  // properties.type_string（参数/返回等）
	FullPath   string          `json:"fullPath,omitempty"` // properties.full_path（字段访问）
	FuncName   string          `json:"funcName,omitempty"` // 所属函数短名（字段访问/SSA 值）
	Flags      []string        `json:"flags,omitempty"` // main / http / grpc
	DocComment string          `json:"docComment,omitempty"` // properties.doc_comment
	Message    string          `json:"message,omitempty"`    // commit 说明
	Date       string          `json:"date,omitempty"`       // commit 时间
	Fields     []NodeFieldJSON `json:"fields,omitempty"`     // struct 字段表
}

// NodeFieldJSON 结构体字段（properties.fields）。
type NodeFieldJSON struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// EdgeJSON 边输出格式；direction: "out"=该节点依赖对方，"in"=对方依赖该节点。
type EdgeJSON struct {
	Source    string `json:"source"`
	Target    string `json:"target"`
	Kind      string `json:"kind"`
	Direction string `json:"direction"`
	Line      int    `json:"line,omitempty"`
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

// FlowRowJSON 函数内字段数据流的一步（/api/flows 输出）。
type FlowRowJSON struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Depth      int      `json:"depth"`
	Dir        int      `json:"dir"` // 0=产生链（反向），1=使用链（正向）
	EdgeKind   string   `json:"edgeKind,omitempty"` // 进入该节点的边类型
	Line       int      `json:"line,omitempty"`
	Kind       string   `json:"kind"` // field_access / ssa_value
	Access     string   `json:"access,omitempty"` // field_access 的 read/write
	FullPath   string   `json:"fullPath,omitempty"`
	FuncID     string   `json:"funcId,omitempty"`   // 所属函数 canonical ID
	FuncName   string   `json:"funcName,omitempty"` // 所属函数短名（函数上下文分组）
	Conditions []string `json:"conditions,omitempty"` // 路径条件标注（Q92，查询期叠加）
}

// handleValueTrace 返回数据值全链（函数上下文分组渲染数据）。
func (s *Server) handleValueTrace(w http.ResponseWriter, r *http.Request) {
	logger := logging.FromContext(s.ctx)
	logger.Debug("enter (Server).handleValueTrace")
	defer logger.Debug("exit (Server).handleValueTrace")
	id := r.URL.Query().Get("id")
	if id == "" {
		writeErr(w, http.StatusBadRequest, "missing id")
		return
	}
	rows, err := s.acts.ValueTrace(domain.CanonicalID(id), 8)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// 路径条件标注（Q92 查询期叠加）——与 CLI 输出对齐（此前前端缺失）
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
