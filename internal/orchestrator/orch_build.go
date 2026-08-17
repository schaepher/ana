package orchestrator

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/schaepher/codeintel/internal/domain"
	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
	"github.com/schaepher/codeintel/internal/logging"
	"go.uber.org/zap"
	"golang.org/x/tools/go/packages"
)

// FullBuild 执行全量构建并返回报告（TD.md 5.2 并行流程）。
func (o *Orchestrator) FullBuild(ctx context.Context) (*BuildResult, error) {
	logger := logging.FromContext(ctx)
	logger.Debug("enter (Orchestrator).FullBuild")
	defer logger.Debug("exit (Orchestrator).FullBuild")
	start := time.Now()

	if err := o.RepoImpl.ResetGraphTables(); err != nil {
		return nil, fmt.Errorf("reset graph tables: %w", err)
	}

	orchestraStart := time.Now()
	orchStage := func(name string) {
		logger.Info("orchestrator stage",
			zap.String("stage", name), zap.Duration("elapsed", time.Since(orchestraStart)))
		orchestraStart = time.Now()
	}
	pkgs, err := o.loadPackages(ctx)
	if err != nil {
		return nil, err
	}
	orchStage("loadPackages")
	results, skipped, err := o.runAdapters(ctx, pkgs, nil, nil)
	if err != nil {
		return nil, err
	}
	orchStage("runAdapters")
	return o.finishBuild(start, results, skipped, "all")
}

// IncrementalBuild 增量构建（TD.md 5.2 增量语义，MVP：全量分析 + 增量写入）：
//  1. 删除变更文件旧数据（节点级联删边与摘要）
//  2. 适配器全量运行，写库时只保留与变更文件相关的产出
//     （节点 file_path ∈ 变更文件；边/摘要的端点属于变更文件）
//  3. build_metadata 记录 tool_name=incremental
//
// 语义正确性：全量分析保证跨包间接写闭包等结果完整（分析成本与 init 相同），
// 增量只裁剪写入范围——未变更数据原样保留，不产生全量 DELETE 的碎片。
func (o *Orchestrator) IncrementalBuild(ctx context.Context, changedFiles []string) (*BuildResult, error) {
	logger := logging.FromContext(ctx)
	logger.Debug("enter (Orchestrator).IncrementalBuild")
	defer logger.Debug("exit (Orchestrator).IncrementalBuild")
	start := time.Now()

	changed := map[string]bool{}
	for _, f := range changedFiles {
		changed[f] = true
	}
	if err := deleteFiles(o.RepoImpl, changedFiles); err != nil {
		return nil, fmt.Errorf("delete changed files: %w", err)
	}

	endpointFile := map[string]string{}
	endpointInChanged := func(id string) bool {
		if fp, ok := endpointFile[id]; ok {
			return changed[fp]
		}
		var fp sql.NullString
		if err := o.RepoImpl.QueryRow("SELECT file_path FROM nodes WHERE id = ?", id).Scan(&fp); err != nil || !fp.Valid {
			return true
		}
		endpointFile[id] = fp.String
		return changed[fp.String]
	}
	keep := func(item domain.Item) bool {
		switch {
		case item.Node != nil:
			return changed[item.Node.FilePath]
		case item.Fact != nil:
			return endpointInChanged(string(item.Fact.SourceID)) || endpointInChanged(string(item.Fact.TargetID))
		case item.Summary != nil:
			return endpointInChanged(string(item.Summary.FunctionID))
		}
		return false
	}
	pkgs, err := o.loadPackages(ctx)
	if err != nil {
		return nil, err
	}
	results, skipped, err := o.runAdapters(ctx, pkgs, keep, changedFiles)
	if err != nil {
		return nil, err
	}
	return o.finishBuild(start, results, skipped, "incremental")
}

// loadPackages 统一加载仓库 go/packages（内存优化：AST/SSA 适配器共享
// 一次类型检查，避免各自 Load 翻倍）。返回共享结果供适配器复用。
// loadPackages 统一加载仓库 go/packages（内存优化：AST/SSA 适配器共享
// 一次类型检查，避免各自 Load 翻倍）。返回共享结果供适配器复用。
// P2-3 多 go.mod：每个 module 单独 Load（go/packages 不能跨 module），
// 按 PkgPath 去重合并（同一包路径只属于一个 module，Go 语义保证）。
func (o *Orchestrator) loadPackages(ctx context.Context) ([]*packages.Package, error) {
	dirs := []string{o.Repo.Path}
	for i, d := range o.Repo.ModuleDirs {
		if i == 0 {
			continue
		}
		dirs = append(dirs, filepath.Join(o.Repo.Path, d))
	}
	seen := map[string]bool{}
	var out []*packages.Package
	for _, dir := range dirs {
		cfg := &packages.Config{
			Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedSyntax |
				packages.NeedTypes | packages.NeedTypesInfo | packages.NeedImports | packages.NeedDeps,
			Dir: dir,
		}
		pkgs, err := packages.Load(cfg, "./...")
		if err != nil {
			return nil, fmt.Errorf("go/packages load (%s): %w", dir, err)
		}
		for _, p := range pkgs {
			if seen[p.PkgPath] {
				continue
			}
			seen[p.PkgPath] = true
			out = append(out, p)
		}
	}
	return out, nil
}

// DiscoverModules 递归扫描仓库根下的 go.mod（跳过 .git/.codeintel/vendor/
// node_modules），返回 module 路径与相对仓库根的目录（根 go.mod 在前）。
// P2-3 多 go.mod monorepo。
func DiscoverModules(repoPath string) (modules []string, dirs []string, err error) {
	rootMod, err := readGoModModule(filepath.Join(repoPath, "go.mod"))
	if err != nil {
		return nil, nil, err
	}
	modules = append(modules, rootMod)
	dirs = append(dirs, ".")
	var walk func(dir string) error
	walk = func(dir string) error {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			switch e.Name() {
			case ".git", ".codeintel", "vendor", "node_modules":
				continue
			}
			sub := filepath.Join(dir, e.Name())
			if _, err := os.Stat(filepath.Join(sub, "go.mod")); err == nil {
				m, err := readGoModModule(filepath.Join(sub, "go.mod"))
				if err != nil {
					continue
				}
				rel, _ := filepath.Rel(repoPath, sub)
				modules = append(modules, m)
				dirs = append(dirs, filepath.ToSlash(rel))
				continue
			}
			if err := walk(sub); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(repoPath); err != nil {
		return nil, nil, err
	}
	return modules, dirs, nil
}

// readGoModModule 解析 go.mod 的 module 指令。
func readGoModModule(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read go.mod: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "module "); ok {
			m := strings.TrimSpace(rest)
			if i := strings.Index(m, " "); i >= 0 {
				m = m[:i]
			}
			if m != "" {
				return m, nil
			}
		}
	}
	return "", fmt.Errorf("go.mod 无 module 指令: %s", path)
}

// deleteFiles 删除指定文件的节点（级联删边与摘要行）；分批避免 SQLite
// 参数上限（999）。
func deleteFiles(repo *sqlite.Repo, files []string) error {
	const batchSize = 400
	for i := 0; i < len(files); i += batchSize {
		end := i + batchSize
		if end > len(files) {
			end = len(files)
		}
		batch := files[i:end]
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",")
		args := make([]any, len(batch))
		for j, f := range batch {
			args[j] = f
		}
		if _, err := repo.Exec("DELETE FROM nodes WHERE file_path IN ("+placeholders+")", args...); err != nil {
			return err
		}
	}
	return nil
}
