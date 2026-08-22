package sqlite

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"go.uber.org/zap"
)

// Q238 全局注册表：~/.codeintel/codeintel.db（目录由调用方决定，默认
// home/.codeintel）。纯台账（repos 表）——记录所有 init 过的仓库：
// 路径/module/HEAD/build/时间戳/worktree 归属/workspace 归属。
// 语义（design-q238.md）：
//   - 库缺失自动重建（纯台账丢失无副作用，Q12）
//   - schema 列变更自动重建表 + 迁移数据（repos 小，避免手动删库丢台账，Q16）
//   - 单写者 + busy_timeout=5000（与仓库内 db 同模式）
//   - 注册表从不作为命令必需前置（list 在库缺失时显示空）

// RegistrySchemaVersion 全局注册表 schema 版本。
const RegistrySchemaVersion = 1

const registrySchema = `
CREATE TABLE IF NOT EXISTS repos (
    id            INTEGER PRIMARY KEY,
    path          TEXT NOT NULL UNIQUE,   -- 绝对路径（主仓库或 worktree 工作目录）
    module        TEXT,                   -- module 名（多 go.mod 取根）
    go_mod_count  INTEGER NOT NULL DEFAULT 0,
    head_commit   TEXT,                   -- 注册/刷新时的 HEAD（非 git 仓库为空）
    build_id      TEXT,                   -- 最近构建 build_id（未构建为空）
    last_built_at TEXT,                   -- 最近构建时间（未构建为空）
    is_worktree   INTEGER NOT NULL DEFAULT 0,
    worktree_of   TEXT,                   -- 主仓库绝对路径（is_worktree=1 时有值）
    workspace     TEXT,                   -- 归属 workspace 目录（可选）
    registered_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_repos_worktree_of ON repos(worktree_of);
CREATE INDEX IF NOT EXISTS idx_repos_module ON repos(module);
`

// 期望列集合（列变更检测：实际列 ⊆ 期望列 且 期望列 ⊆ 实际列）。
var registryCols = []string{"id", "path", "module", "go_mod_count", "head_commit",
	"build_id", "last_built_at", "is_worktree", "worktree_of", "workspace", "registered_at"}

// RegistryRepo 注册表条目（design-q238.md §3.1）。
type RegistryRepo struct {
	Path         string
	Module       string
	GoModCount   int
	HeadCommit   string
	BuildID      string
	LastBuiltAt  string
	IsWorktree   bool
	WorktreeOf   string
	Workspace    string
	RegisteredAt string
}

// Registry 全局注册表句柄（独立于图库 DB——不同 schema/生命周期）。
type Registry struct {
	db *sql.DB
}

// OpenRegistry 打开（缺失自动创建）全局注册表。dir 是注册表目录
// （默认 ~/.codeintel，由调用方决定）；每次打开幂等执行 schema +
// 列变更检测（缺/多列 → 重建表并迁移数据，不丢台账）。
func OpenRegistry(dir string) (*Registry, error) {
	logger := zap.L()
	logger.Debug("enter OpenRegistry")
	defer logger.Debug("exit OpenRegistry")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create registry dir: %w", err)
	}
	db, err := openRawSQLite(filepath.Join(dir, "codeintel.db"))
	if err != nil {
		return nil, err
	}
	r := &Registry{db: db}
	if err := r.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return r, nil
}

// migrate 幂等 schema + 列变更重建迁移（Q16）。
func (r *Registry) migrate() error {
	if _, err := r.db.Exec(registrySchema); err != nil {
		return fmt.Errorf("registry schema: %w", err)
	}
	// 列变更检测：期望列 ⊆ 实际列 且 实际列 ⊆ 期望列
	actual, err := tableCols(r.db, "repos")
	if err != nil {
		return err
	}
	hasAll := true
	for _, c := range registryCols {
		if !actual[c] {
			hasAll = false
			break
		}
	}
	onlyExpected := true
	for c := range actual {
		found := false
		for _, want := range registryCols {
			if c == want {
				found = true
				break
			}
		}
		if !found {
			onlyExpected = false
			break
		}
	}
	if hasAll && onlyExpected {
		_, _ = r.db.Exec(fmt.Sprintf("PRAGMA user_version = %d", RegistrySchemaVersion))
		return nil
	}
	// 重建迁移：公共列复制，新列默认值，旧版独有列丢弃
	if err := r.rebuildMigrate(); err != nil {
		return err
	}
	_, _ = r.db.Exec(fmt.Sprintf("PRAGMA user_version = %d", RegistrySchemaVersion))
	return nil
}

// rebuildMigrate 重建 repos 表并迁移共有列数据（Q16 列变更不丢台账）。
func (r *Registry) rebuildMigrate() error {
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// 共有列 = 期望列 ∩ 实际列
	actual, err := tableCols(r.db, "repos")
	if err != nil {
		return err
	}
	var shared, dropCols []string
	for _, c := range registryCols {
		if actual[c] {
			shared = append(shared, c)
		} else {
			dropCols = append(dropCols, c)
		}
	}
	if _, err := tx.Exec("ALTER TABLE repos RENAME TO repos_old"); err != nil {
		return fmt.Errorf("rename old: %w", err)
	}
	if _, err := tx.Exec(registrySchema); err != nil {
		return fmt.Errorf("create new: %w", err)
	}
	if len(shared) > 0 {
		cols := joinCols(shared)
		if _, err := tx.Exec(fmt.Sprintf("INSERT INTO repos(%s) SELECT %s FROM repos_old", cols, cols)); err != nil {
			return fmt.Errorf("migrate data: %w", err)
		}
	}
	if _, err := tx.Exec("DROP TABLE repos_old"); err != nil {
		return fmt.Errorf("drop old: %w", err)
	}
	_ = dropCols
	return tx.Commit()
}

func joinCols(cols []string) string {
	s := ""
	for i, c := range cols {
		if i > 0 {
			s += ", "
		}
		s += c
	}
	return s
}

// tableCols 返回表实际列集合（PRAGMA table_xinfo——含生成列）。
func tableCols(db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_xinfo(%s)", table))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var dflt any
		var hidden int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk, &hidden); err != nil {
			return nil, err
		}
		out[name] = true
	}
	return out, rows.Err()
}

// RegisterRepo 注册（UPSERT：已存在则整体刷新，含 registered_at 保持——
// 用 INSERT ... ON CONFLICT(path) DO UPDATE 且 registered_at 取原值）。
func (r *Registry) RegisterRepo(repo RegistryRepo) error {
	_, err := r.db.Exec(`INSERT INTO repos(path, module, go_mod_count, head_commit, build_id,
		last_built_at, is_worktree, worktree_of, workspace, registered_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET
		module=excluded.module, go_mod_count=excluded.go_mod_count,
		head_commit=excluded.head_commit, build_id=excluded.build_id,
		last_built_at=excluded.last_built_at, is_worktree=excluded.is_worktree,
		worktree_of=excluded.worktree_of, workspace=excluded.workspace`,
		repo.Path, repo.Module, repo.GoModCount, repo.HeadCommit, repo.BuildID,
		repo.LastBuiltAt, b2i(repo.IsWorktree), repo.WorktreeOf, repo.Workspace,
		repo.RegisteredAt)
	if err != nil {
		return fmt.Errorf("register repo: %w", err)
	}
	return nil
}

// RefreshRepo 刷新构建状态（update 语义：head/build 更新，registered_at 不变）。
func (r *Registry) RefreshRepo(path, headCommit, buildID string) error {
	_, err := r.db.Exec(`UPDATE repos SET head_commit=?, build_id=?, last_built_at=?
		WHERE path=?`, headCommit, buildID, nowStamp(), path)
	if err != nil {
		return fmt.Errorf("refresh repo: %w", err)
	}
	return nil
}

// UnregisterRepo 注销（clean 语义）：主仓库注销级联删除其 worktree 条目（Q7）。
func (r *Registry) UnregisterRepo(path string) error {
	if _, err := r.db.Exec(`DELETE FROM repos WHERE path=? OR worktree_of=?`, path, path); err != nil {
		return fmt.Errorf("unregister repo: %w", err)
	}
	return nil
}

// ListRepos 全部条目（按路径排序）。
func (r *Registry) ListRepos() ([]RegistryRepo, error) {
	rows, err := r.db.Query(`SELECT path, module, go_mod_count, head_commit, build_id,
		last_built_at, is_worktree, worktree_of, workspace, registered_at
		FROM repos ORDER BY path`)
	if err != nil {
		return nil, fmt.Errorf("list repos: %w", err)
	}
	defer rows.Close()
	var out []RegistryRepo
	for rows.Next() {
		var rp RegistryRepo
		var wt int
		var module, head, build, built, wtOf, ws, reg sql.NullString
		if err := rows.Scan(&rp.Path, &module, &rp.GoModCount, &head,
			&build, &built, &wt, &wtOf, &ws, &reg); err != nil {
			return nil, err
		}
		rp.Module, rp.HeadCommit = module.String, head.String
		rp.BuildID, rp.LastBuiltAt = build.String, built.String
		rp.WorktreeOf, rp.Workspace = wtOf.String, ws.String
		rp.RegisteredAt = reg.String
		rp.IsWorktree = wt != 0
		out = append(out, rp)
	}
	return out, rows.Err()
}

// CountRepos 条数（测试/提示用）。
func (r *Registry) CountRepos() (int, error) {
	var n int
	if err := r.db.QueryRow("SELECT COUNT(*) FROM repos").Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// FindRepo 按路径查条目（不存在返回 false）。Q238：workspace 注册的
// worktree 条目经 --build 后 cmdInit 重新注册——须保留 workspace 字段。
func (r *Registry) FindRepo(path string) (RegistryRepo, bool, error) {
	rows, err := r.db.Query(`SELECT path, module, go_mod_count, head_commit, build_id,
		last_built_at, is_worktree, worktree_of, workspace, registered_at
		FROM repos WHERE path=?`, path)
	if err != nil {
		return RegistryRepo{}, false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return RegistryRepo{}, false, rows.Err()
	}
	var rp RegistryRepo
	var wt int
	var module, head, build, built, wtOf, ws, reg sql.NullString
	if err := rows.Scan(&rp.Path, &module, &rp.GoModCount, &head,
		&build, &built, &wt, &wtOf, &ws, &reg); err != nil {
		return RegistryRepo{}, false, err
	}
	rp.Module, rp.HeadCommit = module.String, head.String
	rp.BuildID, rp.LastBuiltAt = build.String, built.String
	rp.WorktreeOf, rp.Workspace = wtOf.String, ws.String
	rp.RegisteredAt = reg.String
	rp.IsWorktree = wt != 0
	return rp, true, nil
}

// Close 关闭全局库连接。
func (r *Registry) Close() error {
	return r.db.Close()
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// nowStamp RFC3339 时间戳（注册/刷新用）。
func nowStamp() string {
	return time.Now().UTC().Format(time.RFC3339)
}

// openRawSQLite 裸 sql.Open（busy_timeout=5000 单写者模式 + 外键）。
// 注册表与图库共用；测试用它构造旧版表验证迁移。
func openRawSQLite(path string) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=ON", path)
	return sql.Open("sqlite3", dsn)
}
