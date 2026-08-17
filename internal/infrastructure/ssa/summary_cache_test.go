package ssa

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

// TestPkgCacheHit：Q176——同一 repo 两次 Index：第一次写包级缓存，
// 第二次命中缓存，产物一致（nodes/facts 数相同）。
func TestPkgCacheHit(t *testing.T) {
	src1 := `package m

type Rec struct {
	A int
	B string
}

func fill(r *Rec) {
	r.A = 1
	r.B = "x"
}
`
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), moduleGoMod)
	writeFile(t, filepath.Join(dir, "main.go"), src1)
	repo := &domain.Repository{Path: dir, Module: "example.com/mtest", Modules: []string{"example.com/mtest"}}
	index := func() (nodes int, facts int) {
		pkgs, err := loadTestPackages(dir)
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		adapter := &Adapter{}
		err = adapter.Index(context.Background(), repo, pkgs, func(item domain.Item) error {
			if item.Node != nil {
				nodes++
			}
			if item.Fact != nil {
				facts++
			}
			return nil
		})
		if err != nil {
			t.Fatalf("Index: %v", err)
		}
		return
	}
	n1, f1 := index()
	cacheDir := filepath.Join(dir, ".codeintel", "cache")
	entries, err := os.ReadDir(cacheDir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("缓存文件未写入: %v", err)
	}
	n2, f2 := index()
	if n1 != n2 || f1 != f2 {
		t.Errorf("缓存命中后产物不一致: nodes %d→%d, facts %d→%d", n1, n2, f1, f2)
	}
}

// TestPkgCacheInvalidation：Q176——包源码变更后缓存失效重新分析。
func TestPkgCacheInvalidation(t *testing.T) {
	src1 := `package m

type Rec struct {
	A int
}

func fill(r *Rec) {
	r.A = 1
}
`
	src2 := `package m

type Rec struct {
	A int
	C bool
}

func fill(r *Rec) {
	r.A = 1
	r.C = true
}
`
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), moduleGoMod)
	writeFile(t, filepath.Join(dir, "main.go"), src1)
	repo := &domain.Repository{Path: dir, Module: "example.com/mtest", Modules: []string{"example.com/mtest"}}
	index := func() int {
		pkgs, err := loadTestPackages(dir)
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		adapter := &Adapter{}
		n := 0
		if err := adapter.Index(context.Background(), repo, pkgs, func(item domain.Item) error {
			if item.Node != nil {
				n++
			}
			return nil
		}); err != nil {
			t.Fatalf("Index: %v", err)
		}
		return n
	}
	n1 := index()
	writeFile(t, filepath.Join(dir, "main.go"), src2)
	n2 := index()
	if n2 <= n1 {
		t.Errorf("缓存未失效（新字段 C 未反映）: nodes %d → %d", n1, n2)
	}
}
