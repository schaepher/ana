package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

// newGitRepo 在临时目录建 git 仓库并提交两个 commit。
func newGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "a.go")
	run("commit", "-q", "-m", "first commit")
	if err := os.WriteFile(filepath.Join(dir, "b.go"), []byte("package b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "b.go")
	run("commit", "-q", "-m", "second commit")
	return dir
}

func TestIndexGitHistory(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	dir := newGitRepo(t)
	var nodes []*domain.CodeEntity
	var facts []*domain.Fact
	adapter := &Adapter{}
	err := adapter.Index(context.Background(), &domain.Repository{Path: dir}, nil, func(item domain.Item) error {
		if item.Node != nil {
			nodes = append(nodes, item.Node)
		}
		if item.Fact != nil {
			facts = append(facts, item.Fact)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	// 2 个 commit 节点（shortSHA 12 位）
	if len(nodes) != 2 {
		t.Fatalf("commit nodes = %d, want 2", len(nodes))
	}
	for _, n := range nodes {
		if n.Kind != domain.KindCommit {
			t.Errorf("node kind = %s", n.Kind)
		}
		if len(n.Name) != 12 {
			t.Errorf("short sha = %q", n.Name)
		}
	}
	// 2 条 modified_by 边（a.go、b.go → 各自 commit）
	if len(facts) != 2 {
		t.Fatalf("modified_by edges = %d, want 2", len(facts))
	}
	for _, f := range facts {
		if f.Kind != domain.FactModifiedBy || f.Confidence != 1.0 || f.ToolSource != domain.ToolGit {
			t.Errorf("fact = %+v", f)
		}
		if !filepath.HasPrefix(string(f.SourceID), "file:") {
			t.Errorf("source = %s", f.SourceID)
		}
	}
	// 提交信息（second commit 的 message）
	msgs := map[string]bool{}
	for _, n := range nodes {
		if m, ok := n.Properties["message"].(string); ok {
			msgs[m] = true
		}
	}
	if !msgs["first commit"] || !msgs["second commit"] {
		t.Errorf("commit messages = %v", msgs)
	}
	// 日期字段存在
	if _, ok := nodes[0].Properties["date"]; !ok {
		t.Error("commit should carry date")
	}
}

func TestIndexNonGitDir(t *testing.T) {
	// 非 git 目录：git log 失败 → 返回错误
	adapter := &Adapter{}
	dir := t.TempDir()
	err := adapter.Index(context.Background(), &domain.Repository{Path: dir}, nil, func(domain.Item) error { return nil })
	if err == nil {
		t.Error("Index on non-git dir should fail")
	}
}

func TestIndexMaxCommits(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	dir := newGitRepo(t)
	// MaxCommits=1：只取最近一个 commit
	adapter := &Adapter{MaxCommits: 1}
	count := 0
	err := adapter.Index(context.Background(), &domain.Repository{Path: dir}, nil, func(item domain.Item) error {
		if item.Node != nil {
			count++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	if count != 1 {
		t.Errorf("commits with MaxCommits=1 = %d, want 1", count)
	}
}

func TestNormalizePath(t *testing.T) {
	cases := map[string]string{
		"a.go":                             "a.go",
		`"renamed.go"`:                     "renamed.go",
		"{old => new}/file.go":             "new/file.go",
		"dir/{old.go => new.go}":           "new.go",
		"  spaced.go  ":                    "spaced.go",
		`"dir/a.go"`:                       "dir/a.go",
		"{src => dst}/internal/app/app.go": "dst/internal/app/app.go",
	}
	for in, want := range cases {
		if got := normalizePath(in); got != want {
			t.Errorf("normalizePath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestShortSHA(t *testing.T) {
	sha := "0123456789abcdef0123456789abcdef"
	if got := shortSHA(sha); got != "0123456789ab" {
		t.Errorf("shortSHA = %q", got)
	}
	if got := shortSHA("short"); got != "short" {
		t.Errorf("shortSHA(short) = %q", got)
	}
}
