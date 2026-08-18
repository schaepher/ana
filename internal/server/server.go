// Package server 提供图探索 HTTP 服务：/api/roots 初始入口、
// /api/expand 点击展开、静态前端页面（go:embed）。
package server

import (
	"context"
	"io/fs"
	"net/http"
	"sync"

	"github.com/schaepher/codeintel/internal/action"
	"github.com/schaepher/codeintel/internal/logging"
)

// Server 承载图查询 HTTP 接口。全部查询经 action 层调用仓储
// （CLI/HTTP 只做参数解析与结果 JSON 化）。
type Server struct {
	ctx  context.Context // 携带 root span，handler 日志由此取带链路信息的 logger
	acts *action.Actions
	web  fs.FS  // 前端静态资源
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
	mux.HandleFunc("/api/module-calls", s.handleModuleCalls)
	mux.HandleFunc("/api/er", s.handleER)
	mux.Handle("/", http.FileServer(http.FS(s.web)))
	return mux
}

// handleSearch 全库符号搜索（名称/ID/文件模糊匹配，上限 50）。

// NodeJSON 节点输出格式。
type NodeJSON struct {
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	Kind       string          `json:"kind"`
	File       string          `json:"file,omitempty"`
	Line       int             `json:"line,omitempty"`
	Signature  string          `json:"signature,omitempty"`
	Type       string          `json:"type,omitempty"`       // properties.type_string（参数/返回等）
	FullPath   string          `json:"fullPath,omitempty"`   // properties.full_path（字段访问）
	FuncName   string          `json:"funcName,omitempty"`   // 所属函数短名（字段访问/SSA 值）
	Flags      []string        `json:"flags,omitempty"`      // main / http / grpc
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
// Metadata：passes_result 携带 arg_index/arg_name（Q185 实参来源标注）等。
type EdgeJSON struct {
	Source    string         `json:"source"`
	Target    string         `json:"target"`
	Kind      string         `json:"kind"`
	Direction string         `json:"direction"`
	Line      int            `json:"line,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// handleModuleCalls 模块间调用（field_trace.md §21.3）：HTTP JSON 透出
// action.ModuleCalls（grpc + http，transport 标注）——前端模块视图数据源。

// handleIncremental 增量构建自动触发（field_trace.md §20.1）：
// POST /incremental（无负载，serve 已绑定 repo）→ 202 + 异步执行；
// 执行中再请求 → 409（单写者）；未配置 buildFn → 404（提示先 init）。

// handleRoots 返回顶层入口节点（main 入口 + HTTP/gRPC 服务入口）。

// handleExpand 返回某节点的一级邻居（双向）。

// nodeToJSON 转换节点为前端格式；roots 场景补充入口标记。

// FlowRowJSON 函数内字段数据流的一步（/api/flows 输出）。
type FlowRowJSON struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Depth      int      `json:"depth"`
	Dir        int      `json:"dir"`                // 0=产生链（反向），1=使用链（正向）
	EdgeKind   string   `json:"edgeKind,omitempty"` // 进入该节点的边类型
	Line       int      `json:"line,omitempty"`
	Kind       string   `json:"kind"`             // field_access / ssa_value
	Access     string   `json:"access,omitempty"` // field_access 的 read/write
	FullPath   string   `json:"fullPath,omitempty"`
	FuncID     string   `json:"funcId,omitempty"`     // 所属函数 canonical ID
	FuncName   string   `json:"funcName,omitempty"`   // 所属函数短名（函数上下文分组）
	Conditions []string `json:"conditions,omitempty"` // 路径条件标注（Q92，查询期叠加）
}

// handleValueTrace 返回数据值全链（函数上下文分组渲染数据）。

// handleFlows 返回函数/方法节点内的完整字段数据流（文本树渲染数据）。

// shortFuncName 从 canonical ID 提取函数短名（symbol:go:<pkg>:<name> → <name>，
// 方法保留 (T).m 形式）——字段访问/SSA 值节点所属函数的展示用。
