//go:build integration

package integration

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
)

// TestLoadValueTraceSelfContained：举一反三——Load 值起点（rec := *ptr
// 解引用赋值后传参）。

// TestValueTraceInterfaceSelfContained：继续查——value-trace 经接口
// argument 边进入候选实现（⑮ 只测了 trace-forward）。

// TestValueTraceDedupSelfContained：Q155 集成固化——value-trace 递归
// CTE 按 (id, dir) 去重。phi 汇聚（x = phi(a, b)，两分支 alloc 汇入）：
// 从 FinalFee.write 反向，每个节点恰好一行、深度正确，两分支都出现。

// TestValueTraceContainerBoundarySelfContained：Q163 集成固化——从
// Payment 分支写点（SettledFee.write）追踪，默认（候选边剪枝）不出现
// RefundSource 实现；显式 --min-conf 0 时经候选 returns 边可达且标注
// 候选（路径累计）。
