package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDeleteIndexDB：reindex 前删除旧库（含 wal/shm），绕过 schema 版本检查；
// 不存在时静默成功（首次 reindex 无库）。
func TestDeleteIndexDB(t *testing.T) {
	dir := t.TempDir()
	idx := filepath.Join(dir, ".codeintel")
	if err := os.MkdirAll(idx, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"codeintel.db", "codeintel.db-wal", "codeintel.db-shm", "codeintel.log"} {
		if err := os.WriteFile(filepath.Join(idx, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// 无关文件（index.scip 等）应保留
	if err := os.WriteFile(filepath.Join(idx, "index-0.scip"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := deleteIndexDB(dir); err != nil {
		t.Fatalf("deleteIndexDB: %v", err)
	}
	for _, f := range []string{"codeintel.db", "codeintel.db-wal", "codeintel.db-shm"} {
		if _, err := os.Stat(filepath.Join(idx, f)); !os.IsNotExist(err) {
			t.Errorf("%s 应被删除", f)
		}
	}
	if _, err := os.Stat(filepath.Join(idx, "codeintel.log")); os.IsNotExist(err) {
		t.Error("codeintel.log 不应被删除（日志保留）")
	}
	if _, err := os.Stat(filepath.Join(idx, "index-0.scip")); os.IsNotExist(err) {
		t.Error("无关文件不应被删除")
	}
	// 无 .codeintel 目录：静默成功
	if err := deleteIndexDB(t.TempDir()); err != nil {
		t.Errorf("无库目录 deleteIndexDB = %v", err)
	}
}

// TestCmdReindexNoRepo：reindex 缺 --repo → exit 2。
func TestCmdReindexNoRepo(t *testing.T) {
	if code := cmdReindex(nil, []string{}); code != 2 {
		t.Errorf("reindex 缺 --repo exit = %d, want 2", code)
	}
}
