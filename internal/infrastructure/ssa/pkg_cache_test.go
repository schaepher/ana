package ssa

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/schaepher/codeintel/internal/domain"
	"golang.org/x/tools/go/packages"
)

// TestAnalyzerVersionHash：分析器版本 = 二进制内容 hash——分析逻辑任何
// 变化（提交/未提交）→ 重 build → 二进制变 → 缓存自动失效（Q181 确定
// 机制：此前只按包源码 hash，逻辑变化不失效——验证仓库 曾命中旧逻辑缓存）。
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

// TestAnalyzerMarkerRoundTrip：全局分析器 marker（Q182）——FullBuild 写、
// IncrementalBuild 读；写后读回命中；无 marker 返回空。
func TestAnalyzerMarkerRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if err := SaveAnalyzerMarker(dir); err != nil {
		t.Fatal(err)
	}
	if got := LoadAnalyzerMarker(dir); got != AnalyzerVersionHash() {
		t.Errorf("marker = %q, want %q", got, AnalyzerVersionHash())
	}
	if got := LoadAnalyzerMarker(t.TempDir()); got != "" {
		t.Errorf("无 marker 应返回空，got %q", got)
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

// Q213 依赖签名变化纳入缓存失效键：本包 hash + 直接依赖包 hash 复合
// ——依赖包 API 变化（本包源码未变）→ 本包缓存自动失效。

// TestPkgCacheKeyHashDependencyChange：改直接依赖包源码 → 本包缓存键
// hash 变化（失效）；改回 → hash 恢复（确定性）。
func TestPkgCacheKeyHashDependencyChange(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": `module example.com/mtest

go 1.26
`,
		"lib/lib.go": `package lib

func Greet(name string) string { return "hi " + name }
`,
		"app/app.go": `package app

import "example.com/mtest/lib"

func Run(name string) string { return lib.Greet(name) }
`,
	}
	for path, content := range files {
		writeFile(t, filepath.Join(dir, path), content)
	}
	pkgs, err := loadTestPackages(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	var appPkg *packages.Package
	for _, p := range pkgs {
		if p.PkgPath == "example.com/mtest/app" {
			appPkg = p
			break
		}
	}
	if appPkg == nil {
		t.Fatalf("app 包未加载（pkgs=%d）", len(pkgs))
	}
	memo := map[string]string{}
	h1, err := pkgCacheKeyHash(appPkg, memo)
	if err != nil || h1 == "" {
		t.Fatalf("pkgCacheKeyHash = %q, %v", h1, err)
	}
	// 改依赖包源码（API 变化——函数签名不变但内容变，保守失效）
	writeFile(t, filepath.Join(dir, "lib", "lib.go"), `package lib

func Greet(name string) string { return "hello " + name }
`)
	// 改依赖包源码（模拟新构建：depMemo 每次 Index 新建）
	h2, err := pkgCacheKeyHash(appPkg, map[string]string{})
	if err != nil {
		t.Fatalf("after dep change: %v", err)
	}
	if h1 == h2 {
		t.Fatal("依赖包源码变化后本包缓存键应变化（失效）")
	}
	// 改回 → hash 恢复（确定性）
	writeFile(t, filepath.Join(dir, "lib", "lib.go"), `package lib

func Greet(name string) string { return "hi " + name }
`)
	h3, err := pkgCacheKeyHash(appPkg, map[string]string{})
	if err != nil {
		t.Fatalf("after restore: %v", err)
	}
	if h1 != h3 {
		t.Fatal("依赖包源码恢复后缓存键应恢复（确定性）")
	}
	// 同一构建内 memo 复用（依赖未变 hash 一致）
	h4, err := pkgCacheKeyHash(appPkg, map[string]string{})
	if err != nil || h4 != h3 {
		t.Fatalf("memo 复用后 hash 应一致，got %q vs %q, %v", h4, h3, err)
	}
}

// TestPkgCacheDependencyInvalidation：端到端——命中缓存时节点会被重放
// （emit 回调同样收到，节点数无法区分命中/重算），以缓存文件 mtime
// 区分：失效重算会重写缓存文件（mtime 变化），命中不重写（mtime 不变）。
func TestPkgCacheDependencyInvalidation(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": `module example.com/mtest

go 1.26
`,
		"lib/lib.go": `package lib

func Greet(name string) string { return "hi " + name }
`,
		"app/app.go": `package app

import "example.com/mtest/lib"

func Run(name string) string { return lib.Greet(name) }
`,
	}
	for path, content := range files {
		writeFile(t, filepath.Join(dir, path), content)
	}
	index := func() error {
		pkgs, err := loadTestPackages(dir)
		if err != nil {
			return err
		}
		adapter := &Adapter{}
		repo := &domain.Repository{Path: dir, Module: "example.com/mtest", Modules: []string{"example.com/mtest"}}
		return adapter.Index(context.Background(), repo, pkgs, func(domain.Item) error { return nil })
	}
	cacheMtime := func() map[string]time.Time {
		out := map[string]time.Time{}
		entries, err := os.ReadDir(filepath.Join(dir, ".codeintel", "cache"))
		if err != nil {
			return out
		}
		for _, e := range entries {
			if info, err := e.Info(); err == nil {
				out[e.Name()] = info.ModTime()
			}
		}
		return out
	}
	if err := index(); err != nil {
		t.Fatalf("first index: %v", err)
	}
	mt1 := cacheMtime()
	if len(mt1) == 0 {
		t.Fatal("首次构建应写入包级缓存")
	}
	// 改依赖包 → 第二次：app 缓存失效重算 → 缓存文件重写（mtime 变化）
	writeFile(t, filepath.Join(dir, "lib", "lib.go"), `package lib

func Greet(name string) string { return "hello " + name }
`)
	if err := index(); err != nil {
		t.Fatalf("second index (dep changed): %v", err)
	}
	mt2 := cacheMtime()
	changed := false
	for name, mt := range mt1 {
		if mt2[name].After(mt) {
			changed = true
		}
	}
	if !changed {
		t.Fatal("依赖包变化后缓存文件应重写（mtime 变化）")
	}
	// 依赖不变 → 第三次：全部命中缓存 → 缓存文件不重写（mtime 不变）
	time.Sleep(10 * time.Millisecond) // mtime 粒度保护
	if err := index(); err != nil {
		t.Fatalf("third index: %v", err)
	}
	mt3 := cacheMtime()
	for name, mt := range mt2 {
		if mt3[name].After(mt) {
			t.Fatalf("依赖未变应命中缓存（缓存文件不重写），%s mtime 变化", name)
		}
	}
}
