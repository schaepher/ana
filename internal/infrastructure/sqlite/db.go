// Package sqlite 实现 Code Index Repository（TD.md 4.1/4.2）。
// SQLite 单文件存储图数据：nodes / edges / build_metadata。
// 说明：sqlite-vec 向量表（semble_vectors）在 Semble 适配器接入前不创建。
package sqlite

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "github.com/mattn/go-sqlite3"
)

// SchemaVersion 数据库 schema 版本（PRAGMA user_version）。
// v1.0 前无自动迁移：版本不匹配时提示手动重建（TD.md 10.2）。
const SchemaVersion = 1

const schema = `
CREATE TABLE IF NOT EXISTS nodes (
    id TEXT PRIMARY KEY,  -- Canonical ID
    kind TEXT NOT NULL,
    name TEXT NOT NULL,
    file_path TEXT,
    line_start INTEGER,
    line_end INTEGER,
    properties JSON,
    -- 生成列：供签名搜索
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
    -- 同义边合并：同一 (source, target, kind) 保留最高置信度（TD.md 5.3）
    UNIQUE(source_id, target_id, kind)
);
CREATE INDEX IF NOT EXISTS idx_edges_source ON edges(source_id);
CREATE INDEX IF NOT EXISTS idx_edges_target ON edges(target_id);
CREATE INDEX IF NOT EXISTS idx_edges_kind ON edges(kind);
CREATE INDEX IF NOT EXISTS idx_edges_confidence ON edges(confidence) WHERE confidence >= 0.8;

CREATE TABLE IF NOT EXISTS build_metadata (
    build_id TEXT PRIMARY KEY,
    commit_sha TEXT,
    tool_name TEXT,           -- 'all' (全量) 或 'incremental'
    status TEXT,              -- 'success', 'degraded', 'failed'
    duration_ms INTEGER,
    error_message TEXT,
    timestamp INTEGER DEFAULT (strftime('%s', 'now'))
);
CREATE INDEX IF NOT EXISTS idx_build_commit ON build_metadata(commit_sha);
`

// Open 打开（或创建）仓库根目录下的 .codeintel/codeintel.db，并校验 schema 版本。
func Open(repoPath string) (*DB, error) {
	dir := filepath.Join(repoPath, ".codeintel")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create .codeintel dir: %w", err)
	}
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)",
		filepath.Join(dir, "codeintel.db"))
	raw, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// 单写者场景，限制连接池避免锁竞争
	raw.SetMaxOpenConns(1)

	db := &DB{DB: raw, repoPath: repoPath}
	if err := db.init(); err != nil {
		raw.Close()
		return nil, err
	}
	return db, nil
}

// DB 包装 *sql.DB，提供仓储实现。
type DB struct {
	*sql.DB
	repoPath string
}

func (db *DB) init() error {
	// 检查 schema 版本
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}
	if v != 0 && v != SchemaVersion {
		return fmt.Errorf("schema version mismatch: db is v%d, this binary needs v%d; run 'codeintel clean' and rebuild",
			v, SchemaVersion)
	}
	if v == 0 {
		if _, err := db.Exec(schema); err != nil {
			return fmt.Errorf("create schema: %w", err)
		}
		if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", SchemaVersion)); err != nil {
			return fmt.Errorf("set user_version: %w", err)
		}
	}
	return nil
}

// RepoPath 返回数据库所属仓库路径。
func (db *DB) RepoPath() string { return db.repoPath }
