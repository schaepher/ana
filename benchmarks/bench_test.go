//go:build benchmark

// Package benchmarks 性能基准（field_trace.md §20.2）：对指定仓库跑
// 进程内 FullBuild，记录各适配器耗时 / 峰值内存 / DB 大小。
// 运行：go test ./benchmarks/ -bench-repo <仓库> [-bench-json]
package benchmarks

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"strings"

	"github.com/schaepher/codeintel/internal/domain"
	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
	"github.com/schaepher/codeintel/internal/orchestrator"
)

// moduleNameOf 从 go.mod 解析 module 路径（或chestrator 需要模块前缀判定）。
func moduleNameOf(repoPath string) string {
	data, err := os.ReadFile(filepath.Join(repoPath, "go.mod"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

var (
	benchRepo = flag.String("bench-repo", ".", "基准构建目标仓库（须含 go.mod）")
	benchJSON = flag.Bool("bench-json", false, "输出 JSON 结构化结果")
)

// result 一次基准构建的结果。
type result struct {
	Repo       string           `json:"repo"`
	TotalMs    int64            `json:"total_ms"`
	Adapters   map[string]int64 `json:"adapters_ms"`
	PeakAlloc  uint64           `json:"peak_alloc_bytes"`
	DBBytes    int64            `json:"db_bytes"`
	Nodes      int              `json:"nodes"`
	Edges      int              `json:"edges"`
	Status     string           `json:"status"`
	FinishedAt string           `json:"finished_at"`
}

// TestBenchmarkFullBuild 对 -bench-repo 执行一次全量构建并输出指标。
func TestBenchmarkFullBuild(t *testing.T) {
	abs, err := filepath.Abs(*benchRepo)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(abs, "go.mod")); err != nil {
		t.Fatalf("-bench-repo %s 无 go.mod: %v", abs, err)
	}
	// 记录峰值内存：构建前后采样（GC 后取 HeapAlloc 最大值近似）
	var peak uint64
	sample := func() {
		runtime.GC()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		if m.HeapAlloc > peak {
			peak = m.HeapAlloc
		}
	}
	sample()

	db, err := sqlite.Open(abs)
	if err != nil {
		t.Fatal(err)
	}
	repo := &domain.Repository{Path: abs, Module: moduleNameOf(abs), Modules: []string{moduleNameOf(abs)}}
	orch := orchestrator.New(repo, db)
	res, err := orch.FullBuild(context.Background())
	if err != nil {
		t.Fatalf("FullBuild: %v", err)
	}
	sample()
	db.Close()

	adapters := map[string]int64{}
	for _, a := range res.Adapter {
		adapters[a.Name] = a.Duration.Milliseconds()
	}
	var dbBytes int64
	if fi, err := os.Stat(filepath.Join(abs, ".codeintel", "codeintel.db")); err == nil {
		dbBytes = fi.Size()
	}
	r := result{
		Repo:       abs,
		TotalMs:    res.Duration.Milliseconds(),
		Adapters:   adapters,
		PeakAlloc:  peak,
		DBBytes:    dbBytes,
		Nodes:      res.Nodes,
		Edges:      res.Edges,
		Status:     res.Status,
		FinishedAt: time.Now().Format("2006-01-02 15:04:05"),
	}
	if *benchJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(r)
		return
	}
	fmt.Printf("构建基准: %s（%s）\n", r.Repo, r.FinishedAt)
	fmt.Printf("  总耗时:   %d ms\n", r.TotalMs)
	fmt.Printf("  适配器:   ")
	for _, a := range res.Adapter {
		fmt.Printf("%s=%dms ", a.Name, a.Duration.Milliseconds())
	}
	fmt.Println()
	fmt.Printf("  峰值内存: %.1f MB\n", float64(r.PeakAlloc)/1024/1024)
	fmt.Printf("  DB 大小:  %.1f MB（%d 节点 / %d 边）\n", float64(r.DBBytes)/1024/1024, r.Nodes, r.Edges)
	fmt.Printf("  状态:     %s\n", r.Status)
}
