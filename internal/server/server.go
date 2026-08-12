// Package server 提供图探索 HTTP 服务：/api/roots 初始入口、
// /api/expand 点击展开、静态前端页面（go:embed）。
package server

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"

	"github.com/schaepher/codeintel/internal/domain"
	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
	"go.uber.org/zap"
)

// Server 承载图查询 HTTP 接口。
type Server struct {
	repo *sqlite.Repo
	web  fs.FS // 前端静态资源
}

// New 创建 Server。
func New(repo *sqlite.Repo, webFS fs.FS) *Server {
	logger := zap.L()
	logger.Debug("enter New")
	defer logger.Debug("exit New")
	return &Server{repo: repo, web: webFS}
}

// Handler 返回 HTTP 处理器。
func (s *Server) Handler() http.Handler {
	logger := zap.L()
	logger.Debug("enter (Server).Handler")
	defer logger.Debug("exit (Server).Handler")
	mux := http.NewServeMux()
	mux.HandleFunc("/api/roots", s.handleRoots)
	mux.HandleFunc("/api/expand", s.handleExpand)
	mux.Handle("/", http.FileServer(http.FS(s.web)))
	return mux
}

// NodeJSON 节点输出格式。
type NodeJSON struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Kind      string   `json:"kind"`
	File      string   `json:"file,omitempty"`
	Line      int      `json:"line,omitempty"`
	Signature string   `json:"signature,omitempty"`
	Flags     []string `json:"flags,omitempty"` // main / http / grpc
}

// EdgeJSON 边输出格式；direction: "out"=该节点依赖对方，"in"=对方依赖该节点。
type EdgeJSON struct {
	Source    string `json:"source"`
	Target    string `json:"target"`
	Kind      string `json:"kind"`
	Direction string `json:"direction"`
	Line      int    `json:"line,omitempty"`
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
	logger := zap.L()
	logger.Debug("enter (Server).handleRoots")
	defer logger.Debug("exit (Server).handleRoots")
	roots, err := s.repo.GetRoots()
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
	logger := zap.L()
	logger.Debug("enter (Server).handleExpand")
	defer logger.Debug("exit (Server).handleExpand")
	id := r.URL.Query().Get("id")
	if id == "" {
		writeErr(w, http.StatusBadRequest, "missing id")
		return
	}
	cur, err := s.repo.GetSymbol(domain.CanonicalID(id))
	if err != nil {
		writeErr(w, http.StatusNotFound, "symbol not found: "+id)
		return
	}
	facts, neighbors, err := s.repo.Expand(domain.CanonicalID(id))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
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
	if n.Property("serves_http") == "true" {
		j.Flags = append(j.Flags, "http")
	}
	if n.Property("serves_grpc") == "true" {
		j.Flags = append(j.Flags, "grpc")
	}
	return j
}
