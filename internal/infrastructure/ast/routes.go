package ast

import (
	"fmt"
	"go/ast"
	"go/types"
	"os"
	"path/filepath"
	"strings"

	"github.com/schaepher/codeintel/internal/domain"
	"gopkg.in/yaml.v3"
)

// httpRoute 路由表条目（field_trace.md §18.7，routes.yaml 人工维护）。
type httpRoute struct {
	Path    string `yaml:"path"`
	Handler string `yaml:"handler"` // 符号名（module 相对包路径 + :(T).m / :name）
	Method  string `yaml:"method"`  // 可选（HTTP 方法）
}

// loadRoutes 读取仓库根 routes.yaml（不存在返回空；解析失败警告降级）。
func loadRoutes(repoPath string) ([]httpRoute, []string) {
	data, err := os.ReadFile(filepath.Join(repoPath, "routes.yaml"))
	if err != nil {
		return nil, nil // 无路由表：HTTP 调用全为外部
	}
	var cfg struct {
		Routes []httpRoute `yaml:"routes"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, []string{fmt.Sprintf("routes.yaml 解析失败，已忽略: %v", err)}
	}
	return cfg.Routes, nil
}

// routeMatch 客户端 URL 路径 ↔ 路由路径匹配（§18.7 Q143）：
// 精确相等；路由以 / 结尾 → 前缀；含 {id} 通配段 → 按段匹配任意单段。
func routeMatch(urlPath, routePath string) bool {
	if urlPath == routePath {
		return true
	}
	if strings.HasSuffix(routePath, "/") {
		return strings.HasPrefix(urlPath, routePath)
	}
	// {id} 通配：按段对齐（/api/orders/{id} ↔ /api/orders/123）
	rs, us := strings.Split(routePath, "/"), strings.Split(urlPath, "/")
	if len(rs) != len(us) {
		return false
	}
	for i := range rs {
		if rs[i] == us[i] {
			continue
		}
		if strings.HasPrefix(rs[i], "{") && strings.HasSuffix(rs[i], "}") {
			continue
		}
		return false
	}
	return true
}

// parseURL 从 URL 字符串提取 host 与 path（query/fragment 剥离）。
// 相对 URL（/path）host 为空。
func parseURL(url string) (host, path string) {
	u := url
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	}
	if i := strings.IndexAny(u, "/?"); i >= 0 {
		host = u[:i]
		u = u[i:]
	} else {
		host = u
		u = ""
	}
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	if u == "" {
		u = "/"
	}
	return host, u
}

// resolveRouteHandler 解析路由表 handler 符号名为函数 canonical ID：
// 符号格式 "<module相对包路径>:(T).Method" 或 "<module相对包路径>:Name"。
func (a *Adapter) resolveRouteHandler(repo *domain.Repository, handler string) (domain.CanonicalID, bool) {
	idx := strings.LastIndex(handler, ":")
	if idx <= 0 {
		return "", false
	}
	pkgRel, sym := handler[:idx], handler[idx+1:]
	pkgPath := pkgRel
	if !strings.HasPrefix(pkgPath, repo.Module) {
		pkgPath = repo.Module + "/" + strings.TrimPrefix(pkgPath, "/")
	}
	pkg, ok := a.pkgsByPath[pkgPath]
	if !ok {
		fmt.Fprintf(os.Stderr, "DBG handler pkg 未找到: %s (pkgPath=%s)\n", handler, pkgPath)
		return "", false
	}
	methodName, recvName := sym, ""
	if i := strings.Index(sym, ")."); i >= 0 {
		recvName = strings.TrimSuffix(strings.TrimPrefix(sym[:i+1], "("), ")")
		methodName = sym[i+2:]
	}
	for _, f := range pkg.Syntax {
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Name.Name != methodName {
				continue
			}
			if recvName != "" {
				if fd.Recv == nil || len(fd.Recv.List) == 0 {
					continue
				}
				rt := fd.Recv.List[0].Type
				if st, ok := rt.(*ast.StarExpr); ok {
					rt = st.X
				}
				if id, ok := rt.(*ast.Ident); !ok || id.Name != recvName {
					continue
				}
			} else if fd.Recv != nil {
				continue
			}
			obj, ok := pkg.TypesInfo.Defs[fd.Name].(*types.Func)
			if !ok || obj == nil {
				continue
			}
			id, _ := fnID(obj)
			if id != "" {
				return id, true
			}
		}
	}
	return "", false
}
