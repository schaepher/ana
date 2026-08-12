// Package logging 提供 context 与 *zap.Logger 的互转工具。
// 由 scripts/entrylog 生成的日志代码使用：函数含 ctx 参数时，
// 通过 FromContext 从 ctx 取出 logger（缺失回退 zap.L()）。
package logging

import (
	"context"

	"go.uber.org/zap"
)

// ctxKey 是 context 中 logger 的键（非导出，避免碰撞）。
type ctxKey struct{}

// WithLogger 将 logger 存入 context，供下游函数通过 FromContext 取出。
func WithLogger(ctx context.Context, l *zap.Logger) context.Context {
	return context.WithValue(ctx, ctxKey{}, l)
}

// FromContext 从 ctx 取出 *zap.Logger；ctx 为 nil 或未存入 logger 时回退 zap.L()。
func FromContext(ctx context.Context) *zap.Logger {
	if ctx != nil {
		if l, ok := ctx.Value(ctxKey{}).(*zap.Logger); ok {
			return l
		}
	}
	return zap.L()
}
