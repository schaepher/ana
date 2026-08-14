package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
)

// TestStaleInfo：索引过期检测（field_trace.md §20.3）——build_metadata
// timestamp 早于 git HEAD commit 时间 → 返回提示；否则空。
func TestStaleInfo(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/m\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package m\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"add", "-A"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "base"},
	} {
		full := append([]string{"-C", dir}, args...)
		if out, err := exec.Command("git", full...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}

	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	r := sqlite.NewRepo(db)

	// 1. 无 build_metadata → 空提示
	if tip := staleInfo(dir, r); tip != "" {
		t.Errorf("无构建记录应无提示: %q", tip)
	}

	// 2. 旧构建（1 小时前）→ 提示
	old := time.Now().Add(-time.Hour).Unix()
	if _, err := db.Exec(`INSERT INTO build_metadata (build_id, tool_name, status, timestamp) VALUES ('b1','all','success',?)`, old); err != nil {
		t.Fatal(err)
	}
	tip := staleInfo(dir, r)
	if tip == "" || !strings.Contains(tip, "索引可能过期") {
		t.Errorf("旧构建应提示过期: %q", tip)
	}

	// 3. 新构建（现在）→ 空提示
	if _, err := db.Exec(`INSERT INTO build_metadata (build_id, tool_name, status, timestamp) VALUES ('b2','all','success',?)`, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	if tip := staleInfo(dir, r); tip != "" {
		t.Errorf("新构建应无提示: %q", tip)
	}
}
