// 包级分析缓存（field_trace.md §37，Q176）：
// init/update 时跳过未变更包的分析（emitFunction 是大头），从缓存文件
// 加载产物（节点/边/函数摘要 fd）直接写库；computeAliases/emitSummaries
// 仍全量重算（全局依赖），但输入 fd 从缓存加载。
//
// 缓存键：包源码内容 hash（CompiledGoFiles sha256）+ 分析器版本（Q181）。
// 文件位置：<repo>/.codeintel/cache/<sha256(pkgPath)>.json（clean 随
// .codeintel 删除）。
//
// 失效条件（确定机制）：
//   - 包源码变化 → pkg_hash 不符 → 自动失效
//   - 分析逻辑变化（emitFunction/摘要/别名等，含未提交改动）→ 二进制
//     内容变化 → analyzer 不符 → 自动失效（Q181：此前只按源码 hash，
//     验证仓库 曾命中 Q178 前旧逻辑的缓存，receiver 数据边全部陈旧）
//   - 缓存文件结构变化 → pkgCacheFormat 递增
//
// Q213（2026-08-18）：失效键纳入直接依赖包源码 hash——依赖包 API 变化
// （本包源码未变）→ 本包缓存自动失效。传递性自动覆盖：C 变 → B 键
// 失效 → B 重建后 hash 变 → A 键含 B hash → A 也失效。已知权衡：依赖
// 包非 API 改动（注释/内部实现）也保守失效，可接受。
package ssa

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/schaepher/codeintel/internal/domain"
	"golang.org/x/tools/go/packages"
)

// pkgCacheFormat 缓存文件格式版本（结构变更时递增，旧缓存全部失效）。
const pkgCacheFormat = 1

// pkgCacheFile 单包缓存文件。
type pkgCacheFile struct {
	Version  int                        `json:"version"`
	Analyzer string                     `json:"analyzer"` // 分析器版本（分析源码 hash，Q181/Q183）
	PkgHash  string                     `json:"pkg_hash"`
	Nodes    []*domain.CodeEntity       `json:"nodes"`
	Facts    []*domain.Fact             `json:"facts"`
	FuncData map[string]*cachedFuncData `json:"func_data"`
}

var (
	analyzerOnce sync.Once
	analyzerHash string
)

//go:embed *.go
var ssaSourceFS embed.FS

// analyzerVersionHash 分析器版本：ssa 包生产源码内容 hash（编译时 embed
// 快照，Q183）。只对影响索引产物的分析逻辑变化敏感——CLI 输出/前端/
// 日志等无关改动（即使 rebuild）不触发缓存重建（Q181 用二进制 hash，
// 任何 rebuild 都全量失效——过度）。embed 与 cwd 无关、覆盖未提交改动
// （编译时读取）、目录扫描自动含新增文件。_test.go 排除（测试不影响
// 产物）。
func analyzerVersionHash() string {
	analyzerOnce.Do(func() {
		entries, err := ssaSourceFS.ReadDir(".")
		if err != nil {
			analyzerHash = "unknown"
			return
		}
		var files []string
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			files = append(files, e.Name())
		}
		sort.Strings(files)
		h := sha256.New()
		for _, f := range files {
			data, err := ssaSourceFS.ReadFile(f)
			if err != nil {
				analyzerHash = "unknown"
				return
			}
			h.Write([]byte(f))
			h.Write(data)
			h.Write([]byte{0})
		}
		analyzerHash = hex.EncodeToString(h.Sum(nil))[:16]
	})
	return analyzerHash
}

// cachedFuncData funcData 的可序列化形态（字段未导出，需 DTO）。
type cachedFuncData struct {
	DirectReads    []cachedFieldEntry `json:"direct_reads"`
	DirectWrites   []cachedFieldEntry `json:"direct_writes"`
	IndirectWrites []cachedFieldEntry `json:"indirect_writes"`
	Calls          []cachedCallInfo   `json:"calls"`
}

type cachedFieldEntry struct {
	FieldPath    string `json:"field_path"`
	InstancePath string `json:"instance_path"`
	Line         int    `json:"line"`
	Snippet      string `json:"snippet"`
	CallLine     int    `json:"call_line"`
	CallArg      string `json:"call_arg"`
}

type cachedCallInfo struct {
	CalleeID       string   `json:"callee_id"`
	ArgStructPaths []string `json:"arg_struct_paths"`
	CallLine       int      `json:"call_line"`
	ArgNames       []string `json:"arg_names"`
}

func toCachedFD(fd *funcData) *cachedFuncData {
	if fd == nil {
		return nil
	}
	c := &cachedFuncData{
		DirectReads:    make([]cachedFieldEntry, 0, len(fd.directReads)),
		DirectWrites:   make([]cachedFieldEntry, 0, len(fd.directWrites)),
		IndirectWrites: make([]cachedFieldEntry, 0, len(fd.indirectWrites)),
		Calls:          make([]cachedCallInfo, 0, len(fd.calls)),
	}
	for _, e := range fd.directReads {
		c.DirectReads = append(c.DirectReads, cachedFieldEntry{
			FieldPath: e.fieldPath, InstancePath: e.instancePath,
			Line: e.line, Snippet: e.snippet, CallLine: e.callLine, CallArg: e.callArg,
		})
	}
	for _, e := range fd.directWrites {
		c.DirectWrites = append(c.DirectWrites, cachedFieldEntry{
			FieldPath: e.fieldPath, InstancePath: e.instancePath,
			Line: e.line, Snippet: e.snippet, CallLine: e.callLine, CallArg: e.callArg,
		})
	}
	for _, e := range fd.indirectWrites {
		c.IndirectWrites = append(c.IndirectWrites, cachedFieldEntry{
			FieldPath: e.fieldPath, InstancePath: e.instancePath,
			Line: e.line, Snippet: e.snippet, CallLine: e.callLine, CallArg: e.callArg,
		})
	}
	for _, cInfo := range fd.calls {
		c.Calls = append(c.Calls, cachedCallInfo{
			CalleeID: string(cInfo.calleeID), ArgStructPaths: cInfo.argStructPaths,
			CallLine: cInfo.callLine, ArgNames: cInfo.argNames,
		})
	}
	return c
}

func fromCachedFD(c *cachedFuncData) *funcData {
	if c == nil {
		return nil
	}
	fd := &funcData{}
	for _, e := range c.DirectReads {
		fd.directReads = append(fd.directReads, fieldEntry{
			fieldPath: e.FieldPath, instancePath: e.InstancePath,
			line: e.Line, snippet: e.Snippet, callLine: e.CallLine, callArg: e.CallArg,
		})
	}
	for _, e := range c.DirectWrites {
		fd.directWrites = append(fd.directWrites, fieldEntry{
			fieldPath: e.FieldPath, instancePath: e.InstancePath,
			line: e.Line, snippet: e.Snippet, callLine: e.CallLine, callArg: e.CallArg,
		})
	}
	for _, e := range c.IndirectWrites {
		fd.indirectWrites = append(fd.indirectWrites, fieldEntry{
			fieldPath: e.FieldPath, instancePath: e.InstancePath,
			line: e.Line, snippet: e.Snippet, callLine: e.CallLine, callArg: e.CallArg,
		})
	}
	for _, cInfo := range c.Calls {
		fd.calls = append(fd.calls, callInfo{
			calleeID: domain.CanonicalID(cInfo.CalleeID), argStructPaths: cInfo.ArgStructPaths,
			callLine: cInfo.CallLine, argNames: cInfo.ArgNames,
		})
	}
	return fd
}

// AnalyzerVersionHash 导出当前分析器版本（二进制内容 hash，Q182 全局
// marker 用——orchestrator 判断"新特性须全量重建"）。
func AnalyzerVersionHash() string { return analyzerVersionHash() }

// analyzerMarkerPath 全局分析器版本 marker（.codeintel/cache/analyzer）：
// FullBuild 写入；IncrementalBuild 读取——不匹配（分析器版本变化）时
// 增量写库范围（仅变更文件）无法让新特性在未变更包上生效，须降级全量
// 重建（Q182：区分两种重建场景——包源码变化走增量局部，新特性走全量
// 全局）。
func analyzerMarkerPath(repoDir string) string {
	return filepath.Join(repoDir, ".codeintel", "cache", "analyzer")
}

// AnalyzerMarkerPath 导出 marker 路径（orchestrator 测试用）。
func AnalyzerMarkerPath(repoDir string) string { return analyzerMarkerPath(repoDir) }

// LoadAnalyzerMarker 读全局 marker；无 marker 返回空。
func LoadAnalyzerMarker(repoDir string) string {
	data, err := os.ReadFile(analyzerMarkerPath(repoDir))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// SaveAnalyzerMarker 写全局 marker（目录自动创建；失败返回错误——调用方
// 决定是否阻塞）。
func SaveAnalyzerMarker(repoDir string) error {
	path := analyzerMarkerPath(repoDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(analyzerVersionHash()), 0o644)
}

// pkgCachePath 缓存文件路径（.codeintel/cache/<sha256(pkgPath)>.json）。
func pkgCachePath(repoDir, pkgPath string) string {
	sum := sha256.Sum256([]byte(pkgPath))
	return filepath.Join(repoDir, ".codeintel", "cache", hex.EncodeToString(sum[:])+".json")
}

// pkgContentHash 包源码内容 hash（CompiledGoFiles 拼接 sha256）。
func pkgContentHash(files []string) (string, error) {
	h := sha256.New()
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return "", err
		}
		h.Write(data)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// pkgCacheKeyHash Q213：本包缓存键 = 本包源码 hash + 直接依赖包源码
// hash 列表（按包路径排序拼接保证确定性）。depMemo 复用依赖包 hash
// （每包重读依赖文件是 O(包数×依赖文件总量)，memo 后降为每包一次）。
func pkgCacheKeyHash(pkg *packages.Package, depMemo map[string]string) (string, error) {
	h := sha256.New()
	files := pkg.CompiledGoFiles
	if len(files) == 0 {
		files = pkg.GoFiles
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return "", err
		}
		h.Write(data)
		h.Write([]byte{0})
	}
	deps := make([]string, 0, len(pkg.Imports))
	for path := range pkg.Imports {
		deps = append(deps, path)
	}
	sort.Strings(deps)
	for _, path := range deps {
		dp := pkg.Imports[path]
		if dp == nil {
			continue
		}
		dh, ok := depMemo[path]
		if !ok {
			dfiles := dp.CompiledGoFiles
			if len(dfiles) == 0 {
				dfiles = dp.GoFiles
			}
			var err error
			dh, err = pkgContentHash(dfiles)
			if err != nil {
				return "", err
			}
			depMemo[path] = dh
		}
		h.Write([]byte(path))
		h.Write([]byte{0})
		h.Write([]byte(dh))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// loadPkgCache 读缓存并校验 hash；未命中（缺文件/版本/analyzer/hash 不符）
// 返回 nil。Q181：Analyzer 是二进制内容 hash——分析逻辑变化后旧缓存
// 自动失效（确定机制，无需手动清理）。
func loadPkgCache(path, wantHash string) *pkgCacheFile {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var c pkgCacheFile
	if err := json.Unmarshal(data, &c); err != nil {
		return nil
	}
	if c.Version != pkgCacheFormat || c.Analyzer != analyzerVersionHash() || c.PkgHash != wantHash {
		return nil
	}
	return &c
}

// savePkgCache 写缓存（目录自动创建；写失败不阻塞构建——缓存是加速非必需）。
func savePkgCache(path, hash string, nodes []*domain.CodeEntity, facts []*domain.Fact,
	fd map[domain.CanonicalID]*funcData) {
	c := &pkgCacheFile{
		Version:  pkgCacheFormat,
		Analyzer: analyzerVersionHash(),
		PkgHash:  hash,
		Nodes:    nodes,
		Facts:    facts,
		FuncData: map[string]*cachedFuncData{},
	}
	for id, f := range fd {
		c.FuncData[string(id)] = toCachedFD(f)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	data, err := json.Marshal(c)
	if err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o644)
}
