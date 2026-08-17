package ssa

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestAnalyzerVersionHash：分析器版本 = 二进制内容 hash——分析逻辑任何
// 变化（提交/未提交）→ 重 build → 二进制变 → 缓存自动失效（Q181 确定
// 机制：此前只按包源码 hash，逻辑变化不失效——radar 曾命中旧逻辑缓存）。
func TestAnalyzerVersionHash(t *testing.T) {
	v1 := analyzerVersionHash()
	if v1 == "" || v1 == "unknown" {
		t.Fatalf("analyzerVersionHash 应为非空二进制 hash，got %q", v1)
	}
	// 幂等（进程内缓存）
	if v2 := analyzerVersionHash(); v2 != v1 {
		t.Errorf("analyzerVersionHash 应幂等，got %q vs %q", v1, v2)
	}
}

// TestLoadPkgCacheAnalyzerMismatch：analyzer 版本不符（分析逻辑变化后的
// 旧缓存）→ load 返回 nil（自动失效，不产出陈旧结果）。
func TestLoadPkgCacheAnalyzerMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.json")
	hash := "abc"
	c := pkgCacheFile{
		Version:  pkgCacheFormat,
		Analyzer: "stale-analyzer",
		PkgHash:  hash,
	}
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	// 版本不符：旧分析逻辑生成的缓存必须被拒绝
	if got := loadPkgCache(path, hash); got != nil {
		t.Errorf("analyzer 不匹配的缓存应返回 nil（自动失效），got %+v", got)
	}
}

// TestSaveLoadPkgCacheRoundTrip：save 后 load 命中（analyzer + pkg_hash
// 均匹配）——正常路径不受影响。
func TestSaveLoadPkgCacheRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.json")
	hash := "pkg-hash"
	savePkgCache(path, hash, nil, nil, nil)
	got := loadPkgCache(path, hash)
	if got == nil {
		t.Fatal("analyzer+pkg_hash 匹配的缓存应命中")
	}
	if got.Analyzer != analyzerVersionHash() {
		t.Errorf("缓存 analyzer = %q, want %q", got.Analyzer, analyzerVersionHash())
	}
}
