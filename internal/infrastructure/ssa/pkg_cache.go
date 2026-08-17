// 包级分析缓存（field_trace.md §37，Q176）：
// init/update 时跳过未变更包的分析（emitFunction 是大头），从缓存文件
// 加载产物（节点/边/函数摘要 fd）直接写库；computeAliases/emitSummaries
// 仍全量重算（全局依赖），但输入 fd 从缓存加载。
//
// 缓存键：包源码内容 hash（CompiledGoFiles sha256）。文件位置：
// <repo>/.codeintel/cache/<sha256(pkgPath)>.json（clean 随 .codeintel 删除）。
package ssa

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/schaepher/codeintel/internal/domain"
)

// pkgCacheFormat 缓存文件格式版本（结构变更时递增，旧缓存全部失效）。
const pkgCacheFormat = 1

// pkgCacheFile 单包缓存文件。
type pkgCacheFile struct {
	Version  int                     `json:"version"`
	PkgHash  string                  `json:"pkg_hash"`
	Nodes    []*domain.CodeEntity    `json:"nodes"`
	Facts    []*domain.Fact          `json:"facts"`
	FuncData map[string]*cachedFuncData `json:"func_data"`
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

// loadPkgCache 读缓存并校验 hash；未命中（缺文件/版本/hash 不符）返回 nil。
func loadPkgCache(path, wantHash string) *pkgCacheFile {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var c pkgCacheFile
	if err := json.Unmarshal(data, &c); err != nil {
		return nil
	}
	if c.Version != pkgCacheFormat || c.PkgHash != wantHash {
		return nil
	}
	return &c
}

// savePkgCache 写缓存（目录自动创建；写失败不阻塞构建——缓存是加速非必需）。
func savePkgCache(path, hash string, nodes []*domain.CodeEntity, facts []*domain.Fact,
	fd map[domain.CanonicalID]*funcData) {
	c := &pkgCacheFile{
		Version:  pkgCacheFormat,
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
