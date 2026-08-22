package sqlite

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

// Q235-3 schema 加法自动迁移：打开时总是幂等执行 schema DDL
// （CREATE IF NOT EXISTS）——旧版本库（v1–v3）自动补建缺失表/索引，
// 不再要求 clean；结构齐全性检查（期望列 ⊆ 实际列）替代「版本号相等」
// 校验——列变更（幂等 DDL 无法表达减法）仍报错提示 clean。不存
// fingerprint（规避 GitNexus 交替二进制反复重建教训：子集判断无写入
// 状态，旧二进制打开新库同样通过）。

// v1Schema 模拟 v1 时代的最小 schema（nodes/edges/build_metadata——
// 与本版 DDL 一致，v1→v4 演进只加表/索引，旧表未变）。
const v1Schema = `
CREATE TABLE IF NOT EXISTS nodes (
    id TEXT PRIMARY KEY,
    kind TEXT NOT NULL,
    name TEXT NOT NULL,
    file_path TEXT,
    line_start INTEGER,
    line_end INTEGER,
    properties JSON,
    signature_text TEXT GENERATED ALWAYS AS (json_extract(properties, '$.signature')) VIRTUAL,
    created_at INTEGER DEFAULT (strftime('%s', 'now'))
);
CREATE INDEX IF NOT EXISTS idx_nodes_file_kind ON nodes(file_path, kind) WHERE file_path IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_nodes_name ON nodes(name);
CREATE INDEX IF NOT EXISTS idx_nodes_signature ON nodes(signature_text);
CREATE TABLE IF NOT EXISTS edges (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    source_id TEXT NOT NULL,
    target_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    tool_source TEXT NOT NULL,
    confidence REAL NOT NULL DEFAULT 0.5,
    metadata JSON,
    FOREIGN KEY (source_id) REFERENCES nodes(id) ON DELETE CASCADE,
    FOREIGN KEY (target_id) REFERENCES nodes(id) ON DELETE CASCADE,
    UNIQUE(source_id, target_id, kind)
);
CREATE TABLE IF NOT EXISTS build_metadata (
    build_id TEXT PRIMARY KEY,
    commit_sha TEXT,
    tool_name TEXT,
    status TEXT,
    duration_ms INTEGER,
    error_message TEXT,
    nodes_count INTEGER,
    edges_count INTEGER,
    timestamp INTEGER DEFAULT (strftime('%s', 'now'))
);
`

// openRaw 不经 Open（绕过自动补建）构造任意 schema 的库。
func openRaw(t *testing.T, dir, ddl string, version int) *sql.DB {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".codeintel"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	db, err := sql.Open("sqlite3", filepath.Join(dir, ".codeintel", "codeintel.db"))
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if _, err := db.Exec(ddl); err != nil {
		db.Close()
		t.Fatalf("exec ddl: %v", err)
	}
	if _, err := db.Exec("PRAGMA user_version = " + fmt.Sprintf("%d", version)); err != nil {
		db.Close()
		t.Fatalf("set version: %v", err)
	}
	return db
}

// TestOpenSchemaAutoMigrate：v1 旧库（无 function_field_summary /
// summary_origins / relation_candidates）Open 自动补建——新表与索引
// 存在，查询可用。
func TestOpenSchemaAutoMigrate(t *testing.T) {
	dir := t.TempDir()
	db := openRaw(t, dir, v1Schema, 1)
	db.Close()

	d, err := Open(dir)
	if err != nil {
		t.Fatalf("Open v1 旧库应自动补建，got %v", err)
	}
	defer d.Close()
	r := NewRepo(d)
	for _, tbl := range []string{"function_field_summary", "summary_origins", "relation_candidates"} {
		var name string
		if err := r.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, tbl).Scan(&name); err != nil {
			t.Errorf("v1 旧库应自动补建 %s 表，got %v", tbl, err)
		}
	}
	// 新表可用：summary 落库 + relations 预计算
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID("symbol:go:m:f"), Kind: domain.KindFunction, Name: "f", FilePath: "a.go"},
	}
	if _, err := r.SaveBatchStats(nodes, nil, nil); err != nil {
		t.Fatalf("save: %v", err)
	}
	// 索引存在（relations 查询不报错即可证明 schema 完整）
	if _, err := r.GetTables(); err != nil {
		t.Fatalf("GetTables: %v", err)
	}
}

// TestOpenSchemaMissingColumnFails：核心表缺列（幂等 DDL 无法补列——
// 破坏性变更）→ Open 报错提示 clean。
func TestOpenSchemaMissingColumnFails(t *testing.T) {
	dir := t.TempDir()
	// properties 换名 → 生成列引用悬空，须同步改
	broken := strings.Replace(v1Schema, "    properties JSON,", "    properties_missing TEXT,", 1)
	broken = strings.Replace(broken, "json_extract(properties, '$.signature')",
		"json_extract(properties_missing, '$.signature')", 1)
	db := openRaw(t, dir, broken, 1)
	db.Close()

	_, err := Open(dir)
	if err == nil {
		t.Fatal("nodes 缺 properties 列应报错（列变更须 clean）")
	}
	if !strings.Contains(err.Error(), "clean") {
		t.Errorf("报错应提示 clean 重建，got: %v", err)
	}
}

// TestOpenSchemaUnknownVersionSelfHeal：user_version 为未知高版本
// （v=99）但结构齐全 → Open 成功——user_version 不再是严格相等校验
// （替代原 TestOpenSchemaVersionMismatch 语义：结构齐全即可用）。
func TestOpenSchemaUnknownVersionSelfHeal(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := db.Exec("PRAGMA user_version = 99"); err != nil {
		t.Fatalf("set user_version: %v", err)
	}
	db.Close()

	d2, err := Open(dir)
	if err != nil {
		t.Fatalf("结构齐全的未知版本库应可用，got %v", err)
	}
	d2.Close()
}
