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

// seedGitRepo 建 git 仓库（go.mod + main.go，commit "base"）并返回目录。
func seedGitRepo(t *testing.T) string {
	t.Helper()
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
	return dir
}

// TestStaleInfoByCommit：build commit_sha 与 HEAD 不同 → 提示含 SHA 与
// 变更文件数（Q243 新鲜度显式化）。
func TestStaleInfoByCommit(t *testing.T) {
	dir := seedGitRepo(t)
	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	r := sqlite.NewRepo(db)
	if _, err := db.Exec(`INSERT INTO build_metadata (build_id, commit_sha, tool_name, status, timestamp) VALUES ('b1','deadbeef','all','success',?)`, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	tip := staleInfo(dir, r)
	if tip == "" || !strings.Contains(tip, "索引可能过期") {
		t.Fatalf("commit_sha 不同应提示过期: %q", tip)
	}
	if !strings.Contains(tip, "deadbeef") {
		t.Errorf("提示应含索引 commit sha: %q", tip)
	}
}

// TestStaleInfoDirty：SHA 相同但工作区有未提交变更 → 提示变更文件数。
func TestStaleInfoDirty(t *testing.T) {
	dir := seedGitRepo(t)
	head, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	sha := strings.TrimSpace(string(head))
	if err := os.WriteFile(filepath.Join(dir, "newfile.go"), []byte("package m\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	r := sqlite.NewRepo(db)
	if _, err := db.Exec(`INSERT INTO build_metadata (build_id, commit_sha, tool_name, status, timestamp) VALUES ('b1',?, 'all','success',?)`, sha, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	tip := staleInfo(dir, r)
	if tip == "" || !strings.Contains(tip, "1 个文件") {
		t.Fatalf("工作区变更应提示文件数: %q", tip)
	}
}

// TestStaleInfoFreshBySHA：commit_sha 与 HEAD 一致且无变更 → 空提示。
func TestStaleInfoFreshBySHA(t *testing.T) {
	dir := seedGitRepo(t)
	head, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	sha := strings.TrimSpace(string(head))
	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	r := sqlite.NewRepo(db)
	if _, err := db.Exec(`INSERT INTO build_metadata (build_id, commit_sha, tool_name, status, timestamp) VALUES ('b1',?, 'all','success',?)`, sha, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	if tip := staleInfo(dir, r); tip != "" {
		t.Errorf("SHA 一致应无提示: %q", tip)
	}
}
