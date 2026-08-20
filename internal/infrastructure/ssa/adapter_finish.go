package ssa

import (
	"fmt"
	"go/token"
	"go/types"
	"os"
	"sync/atomic"

	"github.com/schaepher/codeintel/internal/domain"
	"go.uber.org/zap"
	"golang.org/x/tools/go/ssa"
)

var stderr = os.Stderr

// finishIndex 构建收尾（Q231 拆分自 Adapter.Index 尾部）：alias 分析 +
// 摘要/全局初始化/动态派发发射 + 内存释放。
func finishIndex(repo *domain.Repository, prog *ssa.Program, idents map[token.Pos]string,
	a *Adapter, typePkgs []*types.Package, fallbackTotal *atomic.Int64, emit domain.EmitFunc) error {
	logger := zap.L()
	logger.Debug("enter finishIndex")
	defer logger.Debug("exit finishIndex")
	if n := fallbackTotal.Load(); n > 0 {
		fmt.Fprintf(stderr, "warning: %d 个字段访问静态类型解析失败（匿名 struct 等），已回退源码字面量路径\n", n)
	}
	aliasRes, err := computeAliases(repo, prog, idents, a.fd, emit)
	if err != nil {
		return fmt.Errorf("alias analysis: %w", err)
	}
	idents = nil

	if err := emitSummaries(a.fd, aliasRes, emit); err != nil {
		return err
	}
	a.fd, aliasRes = nil, nil

	if err := emitGlobalInit(repo, prog, emit); err != nil {
		return err
	}
	if err := emitDispatches(repo, prog, typePkgs, emit); err != nil {
		return err
	}
	return nil
}
