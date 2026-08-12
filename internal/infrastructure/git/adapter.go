// Package git 实现 Git 历史适配器（TD.md 5.1：历史权威，置信度 1.0）。
// 通过 git log 生成：
//   - COMMIT 节点（kind=commit）
//   - MODIFIED_BY 边：file 节点 → commit 节点
package git

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/schaepher/codeintel/internal/domain"
	"github.com/schaepher/codeintel/internal/logging"
	"go.uber.org/zap"
)

// Adapter 是 Git 历史适配器。
type Adapter struct {
	MaxCommits int // 单文件最多追踪的提交数（默认 200），控制图规模
}

var _ domain.IndexerPort = (*Adapter)(nil)

// Name 实现 IndexerPort。
func (a *Adapter) Name() string {
	logger := zap.L()
	logger.Debug("enter (Adapter).Name")
	defer logger.Debug("exit (Adapter).Name")
	return "git"
}

// Index 扫描仓库最近提交，为每个变更文件建立 MODIFIED_BY 边。
func (a *Adapter) Index(ctx context.Context, repo *domain.Repository, emit domain.EmitFunc) error {
	logger := logging.FromContext(ctx)
	logger.Debug("enter (Adapter).Index")
	defer logger.Debug("exit (Adapter).Index")
	max := a.MaxCommits
	if max <= 0 {
		max = 200
	}
	// git log --name-only --pretty=format:%H%x09%ad%x09%s --date=short -n <max>
	const logFormat = "--pretty=format:%H%x09%ad%x09%s"
	cmd := exec.CommandContext(ctx, "git", "-C", repo.Path,
		"log", "--name-only", logFormat, "--date=short", "-n", fmt.Sprint(max))
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("git log failed: %w", err)
	}

	// 逐条解析提交块
	var cur *domain.CodeEntity
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		if strings.Contains(line, "\t") {
			parts := strings.SplitN(line, "\t", 3)
			sha := parts[0]
			date := parts[1]
			msg := ""
			if len(parts) > 2 {
				msg = parts[2]
			}
			cur = &domain.CodeEntity{
				ID:        domain.CanonicalID("commit:" + sha),
				Kind:      domain.KindCommit,
				Name:      shortSHA(sha),
				LineStart: 0,
				LineEnd:   0,
			}
			if date != "" {
				cur.Properties = map[string]any{"date": date}
			}
			if msg != "" {
				if cur.Properties == nil {
					cur.Properties = map[string]any{}
				}
				cur.Properties["message"] = msg
			}
			if err := emit(domain.Item{Node: cur}); err != nil {
				return err
			}
			continue
		}
		// 文件行（可能有 rename 前缀如 {old => new}，取右侧路径）
		filePath := strings.TrimSpace(line)
		if filePath == "" || cur == nil {
			continue
		}
		filePath = normalizePath(filePath)
		if err := emit(domain.Item{Fact: &domain.Fact{
			SourceID:   domain.CanonicalID("file:" + filePath),
			TargetID:   cur.ID,
			Kind:       domain.FactModifiedBy,
			ToolSource: domain.ToolGit,
			Confidence: 1.0,
		}}); err != nil {
			return err
		}
	}
	return nil
}

func shortSHA(sha string) string {
	logger := zap.L()
	logger.Debug("enter shortSHA")
	defer logger.Debug("exit shortSHA")
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// normalizePath 处理 git 输出的 rename 标记（"old => new" 与引号包裹）。
func normalizePath(p string) string {
	logger := zap.L()
	logger.Debug("enter normalizePath")
	defer logger.Debug("exit normalizePath")
	if i := strings.Index(p, "=>"); i >= 0 {
		p = p[i+2:]
	}
	return strings.Trim(p, "\" ")
}
