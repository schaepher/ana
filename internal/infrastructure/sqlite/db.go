// Package sqlite 实现 Code Index Repository（TD.md 4.1/4.2）。
// SQLite 单文件存储图数据：nodes / edges / build_metadata。
// 说明：sqlite-vec 向量表（semble_vectors）在 Semble 适配器接入前不创建。
package sqlite

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "github.com/mattn/go-sqlite3"
	"go.uber.org/zap"
)

// SchemaVersion 数据库 schema 版本（PRAGMA user_version）。
// v1.0 前无自动迁移：版本不匹配时提示手动重建（TD.md 10.2）。
// v2：新增 function_field_summary 表（SSA 字段追溯，field_trace.md §5.2）。
// v3：新增 summary_origins 表（Q161 间接写多来源聚合）。
// v4：新增 relation_candidates 表（P0③ 表关联缓存）+ 边复合索引（P0①）。
// Q235-3：user_version 仅作初次建库标记——v1→v4 演进全是加法（新表/
// 新索引），打开时总是幂等执行 schema DDL 自动补建（CREATE IF NOT
// EXISTS），无需 clean；列变更（幂等无法表达减法）由 verifySchema
// 报错提示 clean。不存 fingerprint（交替二进制无反复迁移：结构检查
// 是「期望列 ⊆ 实际列」子集判断，无写入状态）。
const SchemaVersion = 4

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
-- 边复合索引（P0①）：邻接查询（source_id=? 或 target_id=?）按方向各走
-- 一个索引；kind 等值过滤在索引内完成（覆盖旧单列索引的查询形态）
CREATE INDEX IF NOT EXISTS idx_edges_source_kind ON edges(source_id, kind);
CREATE INDEX IF NOT EXISTS idx_edges_target_kind ON edges(target_id, kind);

CREATE TABLE IF NOT EXISTS build_metadata (
    build_id TEXT PRIMARY KEY,
    commit_sha TEXT,
    tool_name TEXT,           -- 'all' (全量) 或 'incremental'
    status TEXT,              -- 'success', 'degraded', 'failed'
    duration_ms INTEGER,
    error_message TEXT,
    nodes_count INTEGER,      -- 构建产物规模（--memory auto 判断缓存，P0④）
    edges_count INTEGER,
    timestamp INTEGER DEFAULT (strftime('%s', 'now'))
);
CREATE INDEX IF NOT EXISTS idx_build_commit ON build_metadata(commit_sha);

-- 函数字段摘要表（SSA 字段追溯，field_trace.md §5.2）
CREATE TABLE IF NOT EXISTS function_field_summary (
    function_id TEXT NOT NULL,
    access_kind TEXT NOT NULL CHECK(access_kind IN ('direct_read','direct_write','indirect_write')),
    field_path TEXT NOT NULL,
    instance_path TEXT,
    line_start INTEGER,
    code_snippet TEXT,
    FOREIGN KEY(function_id) REFERENCES nodes(id) ON DELETE CASCADE,
    UNIQUE(function_id, access_kind, field_path)
);
CREATE INDEX IF NOT EXISTS idx_summary_func_access ON function_field_summary(function_id, access_kind);
CREATE INDEX IF NOT EXISTS idx_summary_field ON function_field_summary(field_path);

-- 摘要来源表（Q161）：间接写多来源（调用点 × 被调函数），origin/confidence
-- 查询期从 dispatch_to 边 join（callee 为候选实现时自然带出）
CREATE TABLE IF NOT EXISTS summary_origins (
    function_id TEXT NOT NULL,
    access_kind TEXT NOT NULL CHECK(access_kind IN ('direct_read','direct_write','indirect_write')),
    field_path TEXT NOT NULL,
    call_line INTEGER,
    callee_id TEXT,
    FOREIGN KEY(function_id) REFERENCES nodes(id) ON DELETE CASCADE,
    UNIQUE(function_id, access_kind, field_path, call_line, callee_id)
);
CREATE INDEX IF NOT EXISTS idx_summary_origins_func ON summary_origins(function_id, access_kind);

-- 表达式索引：field_access 定位（S2/S3 起点），字段追溯，field_trace.md §5.2
CREATE INDEX IF NOT EXISTS idx_nodes_field_path ON nodes(json_extract(properties, '$.full_path'));
CREATE INDEX IF NOT EXISTS idx_nodes_func_id ON nodes(json_extract(properties, '$.func_id'));

-- 表关联候选缓存（P0③）：relations 结果按 build_id 持久化（--all 全量
-- 重建 / 单表查询命中直接返回）；from_col='' 为 marker 行（标记"该表已
-- 计算过、无关联"），避免无关联表每次查询重算
CREATE TABLE IF NOT EXISTS relation_candidates (
    build_id TEXT NOT NULL,
    from_table TEXT NOT NULL,
    from_col TEXT NOT NULL,
    to_table TEXT NOT NULL,
    to_col TEXT NOT NULL,
    hops INTEGER NOT NULL,
    type TEXT NOT NULL,
    PRIMARY KEY (build_id, from_table, from_col, to_table, to_col)
);
CREATE INDEX IF NOT EXISTS idx_relcand_build_from ON relation_candidates(build_id, from_table);
`

// configSchema 配置表（Q220c）：独立于 schema 版本演进——幂等补建
// （CREATE TABLE IF NOT EXISTS，打开时始终执行，旧库自动获得配置表，
// 不要求 clean 重建）；ResetGraphTables 不 DROP（clean/reindex 保留）。
// 用户连线规则：外键形态列（merchant_id 等）值来自参数、无值流验证时，
// 由用户声明规则连线。from_table=” 为模式规则（所有含 from_col 列的
// 表 → to_table.to_col）；否则为显式列对。生效时校验目标表/列存在。
const configSchema = `
CREATE TABLE IF NOT EXISTS relation_rules (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    from_table TEXT NOT NULL DEFAULT '',
    from_col TEXT NOT NULL,
    to_table TEXT NOT NULL,
    to_col TEXT NOT NULL DEFAULT 'id',
    created_at INTEGER NOT NULL DEFAULT 0
);
-- Q228：全量 relations 计算进度（precompute 命令/查询端自动兜底写入，
-- 按 build_id 主键——增量构建或分析逻辑版本变更自动失效）。
-- status: pending / running / done；done_count/total_count = 已完成表数/总表数。
CREATE TABLE IF NOT EXISTS relation_progress (
    build_id TEXT PRIMARY KEY,
    status TEXT NOT NULL,
    done_count INTEGER NOT NULL DEFAULT 0,
    total_count INTEGER NOT NULL DEFAULT 0,
    updated_at INTEGER NOT NULL DEFAULT 0
);
`

// Open 打开（或创建）仓库根目录下的 .codeintel/codeintel.db，并校验 schema 版本。
func Open(repoPath string) (*DB, error) {
	logger := zap.L()
	logger.Debug("enter Open")
	defer logger.Debug("exit Open")
	dir := filepath.Join(repoPath, ".codeintel")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create .codeintel dir: %w", err)
	}
	// 注意：go-sqlite3 只识别 _foreign_keys/_journal_mode/_busy_timeout 这类
	// 直连参数；_pragma=xxx(ON) 形式实测不生效（外键/WAL 会静默关闭）
	dsn := fmt.Sprintf("file:%s?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=ON",
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
	logger := zap.L()
	logger.Debug("enter (DB).init")
	defer logger.Debug("exit (DB).init")
	// Q235-3：user_version 仅作初次建库标记，不再严格相等校验——
	// schema 演进全为加法，打开时总是幂等执行补建缺失表/索引
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}
	if v == 0 {
		if _, err := db.Exec(schema); err != nil {
			return fmt.Errorf("create schema: %w", err)
		}
		if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", SchemaVersion)); err != nil {
			return fmt.Errorf("set user_version: %w", err)
		}
	} else {
		// 旧库自动补建缺失表/索引（加法迁移）
		if _, err := db.Exec(schema); err != nil {
			// 缺列（破坏性变更）：补建表达式索引/生成列引用缺失列 →
			// 包装 clean 提示（verifySchema 覆盖不到的早期失败路径）
			if strings.Contains(err.Error(), "no such column") {
				return fmt.Errorf("schema mismatch: 列结构不兼容（%v）; run 'codeintel clean --repo <path> --force' and rebuild", err)
			}
			return fmt.Errorf("migrate schema: %w", err)
		}
	}
	// Q220c：配置表幂等补建（旧库自动获得 relation_rules，不要求 clean）
	if _, err := db.Exec(configSchema); err != nil {
		return fmt.Errorf("create config schema: %w", err)
	}
	// 结构齐全性检查：期望列 ⊆ 实际列（缺表已补建；缺列=破坏性变更
	// 幂等 DDL 无法表达 → 报错提示 clean）
	if err := db.verifySchema(); err != nil {
		return err
	}
	return nil
}

// schemaCols 各表期望列（Q235-3 结构齐全性检查）。schema 变更时须
// 同步更新本清单；列变更（含加列——CREATE IF NOT EXISTS 不动已存在
// 表）触发报错 clean，由清单与 DDL 不一致暴露。
var schemaCols = map[string][]string{
	"nodes":                 {"id", "kind", "name", "file_path", "line_start", "line_end", "properties", "signature_text", "created_at"},
	"edges":                 {"id", "source_id", "target_id", "kind", "tool_source", "confidence", "metadata"},
	"build_metadata":        {"build_id", "commit_sha", "tool_name", "status", "duration_ms", "error_message", "nodes_count", "edges_count", "timestamp"},
	"function_field_summary": {"function_id", "access_kind", "field_path", "instance_path", "line_start", "code_snippet"},
	"summary_origins":       {"function_id", "access_kind", "field_path", "call_line", "callee_id"},
	"relation_candidates":   {"build_id", "from_table", "from_col", "to_table", "to_col", "hops", "type"},
	"relation_rules":        {"id", "from_table", "from_col", "to_table", "to_col", "created_at"},
	"relation_progress":     {"build_id", "status", "done_count", "total_count", "updated_at"},
}

// verifySchema 结构齐全性检查：每个核心表期望列全部存在。旧二进制
// 打开新库（实际列更多）通过——子集判断，不存 fingerprint 无反复
// 迁移（Q235-3）。
func (db *DB) verifySchema() error {
	for tbl, want := range schemaCols {
		// table_xinfo（非 table_info）：table_info 不返回生成列
		// （signature_text VIRTUAL 只在 xinfo 里）
		rows, err := db.Query("PRAGMA table_xinfo(" + tbl + ")")
		if err != nil {
			return fmt.Errorf("verify schema %s: %w", tbl, err)
		}
		have := map[string]bool{}
		for rows.Next() {
			var cid, notnull, pk, hidden int
			var name, typ string
			var dflt sql.NullString
			if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk, &hidden); err != nil {
				rows.Close()
				return fmt.Errorf("verify schema %s: %w", tbl, err)
			}
			have[name] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("verify schema %s: %w", tbl, err)
		}
		for _, c := range want {
			if !have[c] {
				return fmt.Errorf("schema mismatch: %s 缺列 %q（列变更无法自动迁移）; run 'codeintel clean --repo <path> --force' and rebuild", tbl, c)
			}
		}
	}
	return nil
}

// RepoPath 返回数据库所属仓库路径。
func (db *DB) RepoPath() string {
	logger := zap.L()
	logger.Debug("enter (DB).RepoPath")
	defer logger.Debug("exit (DB).RepoPath")
	return db.repoPath
}
