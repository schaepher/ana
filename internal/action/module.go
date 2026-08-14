package action

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/schaepher/codeintel/internal/domain"
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
// 配置前缀为 module 相对路径——先去掉 go.mod 的 module 前缀；
// 未匹配归 _root）。
func (a *Actions) ModuleOf(pkgPath string) string {
	rel := strings.TrimPrefix(pkgPath, a.moduleName()+"/")
	if rel == pkgPath {
		rel = pkgPath // 不在 module 下（外部包）：按原路径匹配
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

// moduleName 从 <repo>/go.mod 解析 module 路径（缓存）。
func (a *Actions) moduleName() string {
	if a.modName != "" {
		return a.modName
	}
	data, err := os.ReadFile(filepath.Join(a.repo.RepoPath(), "go.mod"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "module "); ok {
			a.modName = strings.TrimSpace(rest)
			return a.modName
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
	Service    string `json:"service"`   // 生成包路径 + 服务名
	Method     string `json:"method"`
	Caller     string `json:"caller"` // 客户端调用方函数 ID
	Line       int    `json:"line"`
}

// ModuleCalls 模块间调用表（field_trace.md §18.4）：grpc_call 边 →
// 调用方函数所属模块；经 grpc_impl 边 → 服务实现类型所属模块。
// filter 非空时只返回该模块作为调用方的调用。
func (a *Actions) ModuleCalls(filter string) ([]ModuleCall, error) {
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
		out = append(out, ModuleCall{
			FromModule: from,
			ToModule:   implModule[string(row.ServiceID)],
			Service:    row.Service,
			Method:     row.Method,
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
