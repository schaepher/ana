// Package ast 实现调用图适配器（对应 TD.md 的 CodeGraph 适配器角色，置信度 0.8）。
// 基于 golang.org/x/tools/go/packages 的 AST + 类型信息，纯 Go 无外部进程：
//   - CALLS 边：调用者函数 → 被调用函数/方法（精确调用点）
//   - IMPORTS 边：包 → 直接依赖的项目内包
package ast

import (
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"

	"golang.org/x/tools/go/packages"

	"github.com/schaepher/codeintel/internal/domain"
	"go.uber.org/zap"
)

// Adapter 是基于 go/packages 的调用图分析器。
type Adapter struct {
	// 包路径 → packages.Package：跨包解析构造器函数体（链式调用
	// 返回接口时分析 return 的具体类型），Index 时填充
	pkgsByPath map[string]*packages.Package
	// HTTP 路由表（§18.7，routes.yaml 人工维护）：path → http_route 节点
	routes []routeEntry
	// 增量更新的变更文件（相对仓库根路径，§20.3 AST 文件级跳过）；
	// nil = 全量分析所有文件
	changedFiles map[string]bool
}

// SetChangedFiles 限定增量分析的文件集合（orchestrator 增量构建注入，
// 见 field_trace.md §20.3）；传 nil 恢复全量。文件为相对仓库根的路径。
func (a *Adapter) SetChangedFiles(files []string) {
	if files == nil {
		a.changedFiles = nil
		return
	}
	a.changedFiles = make(map[string]bool, len(files))
	for _, f := range files {
		a.changedFiles[filepath.Clean(f)] = true
	}
}

// routeEntry 构建期路由表条目。
type routeEntry struct {
	path   string
	nodeID domain.CanonicalID
}

var _ domain.IndexerPort = (*Adapter)(nil)

// Name 实现 IndexerPort。
func (a *Adapter) Name() string {
	logger := zap.L()
	logger.Debug("enter (Adapter).Name")
	defer logger.Debug("exit (Adapter).Name")
	return "codegraph"
}

// processFile 遍历单个 AST：定位每个调用点，连接调用者与被调用者。
func (a *Adapter) processFile(repo *domain.Repository, pkg *packages.Package, f *ast.File, emit domain.EmitFunc,
	serviceFlags map[domain.CanonicalID]map[string]bool, registerServers map[string]bool,
	newClients map[string]string) error {
	logger := zap.L()
	logger.Debug("enter (Adapter).processFile")
	defer logger.Debug("exit (Adapter).processFile")
	filePath := relPath(repo.Path, pkg.Fset.PositionFor(f.Pos(), false).Filename)
	if filePath == "" {
		return nil
	}
	// 增量更新（§20.3）：只分析变更文件，未变更文件跳过（节点保留在库中）
	if a.changedFiles != nil && !a.changedFiles[filepath.Clean(filePath)] {
		return nil
	}

	if err := a.emitMethodReceiver(repo, pkg, f, emit); err != nil {
		return err
	}
	if err := a.emitStructFields(repo, pkg, f, emit); err != nil {
		return err
	}

	// 遍历上下文（filectx.go：闭包状态打包 + visit/emitCall 等方法——
	// 2026-08-17 从本函数闭包拆分，逻辑逐行一致）
	ctx := &fileCtx{
		a:               a,
		repo:            repo,
		pkg:             pkg,
		f:               f,
		emit:            emit,
		serviceFlags:    serviceFlags,
		registerServers: registerServers,
		newClients:      newClients,
		// 对象流追踪：变量名 → 对象 ID（同一函数内）；表达式 Pos → 对象 ID（去重）
		objVars:  map[string]domain.CanonicalID{},
		objCache: map[token.Pos]domain.CanonicalID{},
		// gRPC 客户端对象（§18.2）：变量名 → 服务名（NewXxxClient 返回值，函数内追踪）
		grpcClients: map[string]string{},
		// 手写 client（§18.6）：同函数内 `method := "/pkg.Svc/M"` 一层赋值链
		methodVars: map[string]string{},
		// HTTP req 变量（P1-3）：req 名 → URL（req := http.NewRequest(...) 赋值追踪，
		// 供 client.Do(req) 消费防重复判断）
		reqVars:    map[string]string{},
		reqMethods: map[string]string{},
		// 本函数已 emit http_call 的 URL（NewRequest 建边后，Do(req) 不重复）
		httpURLsSeen: map[string]bool{},
		// 函数值变量（P2-1）：f := g / f := obj.Method → f 名 → *types.Func
		// （f() 调用点 callee 解析失败时查此表，unused 误报收敛）
		varFuncs: map[string]*types.Func{},
	}
	ast.Inspect(f, ctx.visit)
	return nil
}
