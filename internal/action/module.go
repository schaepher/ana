package action

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/schaepher/codeintel/internal/domain"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

// moduleConfig modules.yaml（field_trace.md §18.1）：包路径前缀 → 模块名。
type moduleConfig struct {
	Modules []struct {
		Prefix string `yaml:"prefix"`
		Name   string `yaml:"name"`
	} `yaml:"modules"`
}

// ModuleOf 包路径 → 模块名（modules.yaml 前缀匹配，最长前缀优先；
// 配置前缀为 module 相对路径——先去掉所在 go.mod 的 module 前缀
// （P2-3 多 go.mod：任一 module 前缀匹配即剥离）；未匹配归 _root）。
func (a *Actions) ModuleOf(pkgPath string) string {
	logger := zap.L()
	logger.Info("enter (Actions).ModuleOf", zap.String("pkg_path", pkgPath))
	defer logger.Info("exit (Actions).ModuleOf")
	rel := pkgPath
	for _, m := range a.modules() {
		if m == "" {
			continue
		}
		if r, ok := strings.CutPrefix(pkgPath, m+"/"); ok {
			rel = r
			break
		}
	}
	if rel == pkgPath {
		rel = pkgPath // 不在任何 module 下（外部包）：按原路径匹配
	}
	cfg := a.moduleConfig()
	best := ""
	bestLen := -1
	for _, m := range cfg.Modules {
		if strings.HasPrefix(rel, m.Prefix) && len(m.Prefix) > bestLen {
			best = m.Name
			bestLen = len(m.Prefix)
		}
	}
	if best != "" {
		return best
	}
	return "_root"
}

// modules 从仓库根递归扫描 go.mod 解析全部 module 路径（P2-3 多 go.mod；
// 缓存；根 module 在前）。
func (a *Actions) modules() []string {
	if len(a.modNames) > 0 {
		return a.modNames
	}
	var out []string
	var walk func(dir string)
	walk = func(dir string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			switch e.Name() {
			case ".git", ".codeintel", "vendor", "node_modules":
				continue
			}
			sub := filepath.Join(dir, e.Name())
			if m := readGoModModule(sub); m != "" {
				out = append(out, m)
				continue // module 目录内不再嵌套扫描
			}
			walk(sub)
		}
	}
	walk(a.repo.RepoPath())
	// 根 go.mod（若存在）
	root := readGoModModule(a.repo.RepoPath())
	if root != "" {
		out = append([]string{root}, out...)
	}
	a.modNames = out
	return out
}

// readGoModModule 读取目录下 go.mod 的 module 行（无 go.mod 返回空）。
func readGoModModule(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "module "); ok {
			m := strings.TrimSpace(rest)
			if i := strings.Index(m, " "); i >= 0 {
				m = m[:i]
			}
			return m
		}
	}
	return ""
}

// moduleConfig 惰性加载 <repo>/modules.yaml（不存在返回空配置）。
func (a *Actions) moduleConfig() *moduleConfig {
	data, err := os.ReadFile(filepath.Join(a.repo.RepoPath(), "modules.yaml"))
	if err != nil {
		return &moduleConfig{}
	}
	var cfg moduleConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		fmt.Fprintf(os.Stderr, "warning: modules.yaml 解析失败: %v\n", err)
		return &moduleConfig{}
	}
	return &cfg
}

// ModuleCall 模块间调用一行（field_trace.md §18.4）。
type ModuleCall struct {
	FromModule string `json:"from_module"`
	ToModule   string `json:"to_module"` // 服务端不在仓库内时为空
	Service    string `json:"service"`   // grpc：生成包路径+服务名；http：host/route
	Method     string `json:"method"`    // grpc：方法名；http：路径
	Transport  string `json:"transport"` // grpc / http
	Caller     string `json:"caller"`    // 客户端调用方函数 ID
	Line       int    `json:"line"`
}

// ModuleCalls 模块间调用表（field_trace.md §18.4）：grpc_call 边 →
// 调用方函数所属模块；经 grpc_impl 边 → 服务实现类型所属模块。
// filter 非空时只返回该模块作为调用方的调用。
func (a *Actions) ModuleCalls(filter string) ([]ModuleCall, error) {
	logger := zap.L()
	logger.Info("enter (Actions).ModuleCalls", zap.String("filter", filter))
	defer logger.Info("exit (Actions).ModuleCalls")
	rows, err := a.repo.GetGrpcCalls()
	if err != nil {
		return nil, err
	}
	implModule := map[string]string{} // grpc_service ID → 实现模块
	for _, row := range rows {
		if row.ImplTypeID != "" {
			pkg := pkgOfID(row.ImplTypeID)
			implModule[string(row.ServiceID)] = a.ModuleOf(pkg)
		}
	}
	out := make([]ModuleCall, 0, len(rows))
	for _, row := range rows {
		if row.ImplTypeID == "" && len(implModule) > 0 {
			// 服务无实现（可能实现类型未识别）；保持 ToModule 空
		}
		from := a.ModuleOf(pkgOfID(row.CallerID))
		if filter != "" && from != filter {
			continue
		}
		transport := "grpc"
		if row.Transport == "http_call" {
			transport = "http"
		}
		out = append(out, ModuleCall{
			FromModule: from,
			ToModule:   implModule[string(row.ServiceID)],
			Service:    row.Service,
			Method:     row.Method,
			Transport:  transport,
			Caller:     string(row.CallerID),
			Line:       row.Line,
		})
	}
	return out, nil
}

// pkgOfID 从 canonical ID 提取包路径（symbol:go:<pkg>:<name>）。
func pkgOfID(id domain.CanonicalID) string {
	s := strings.TrimPrefix(string(id), "symbol:go:")
	if i := strings.LastIndex(s, ":"); i >= 0 {
		return s[:i]
	}
	return s
}
