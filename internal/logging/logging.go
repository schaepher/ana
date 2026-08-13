// Package logging 提供 context 与 *zap.Logger 的互转，以及 OpenTelemetry
// 链路追踪初始化。entrylog 生成的日志代码使用：函数含 ctx 参数时通过
// FromContext 取出 logger（自动携带 trace_id/span_id），无 ctx 时用 zap.L()。
package logging

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// ctxKey 是 context 中 logger 的键（非导出，避免碰撞）。
type ctxKey struct{}

// WithLogger 将 logger 存入 context，供下游函数通过 FromContext 取出。
func WithLogger(ctx context.Context, l *zap.Logger) context.Context {
	return context.WithValue(ctx, ctxKey{}, l)
}

// FromContext 从 ctx 取出 *zap.Logger；ctx 为 nil 或未存入 logger 时回退
// zap.L()。若 ctx 携带有效的 span context，返回的 logger 附加
// trace_id / span_id 字段（链路追踪：日志与 trace 关联）。
func FromContext(ctx context.Context) *zap.Logger {
	var l *zap.Logger
	if ctx != nil {
		if stored, ok := ctx.Value(ctxKey{}).(*zap.Logger); ok {
			l = stored
		}
	}
	if l == nil {
		l = zap.L()
	}
	if ctx != nil {
		if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
			l = l.With(
				zap.String("trace_id", sc.TraceID().String()),
				zap.String("span_id", sc.SpanID().String()),
			)
		}
	}
	return l
}

// Setup 初始化全局日志与追踪：
//   - zap development logger，输出到 stdout；默认 Info 级，
//     debug=true（CLI --verbose/--debug）时输出 Debug 级
//   - OTel TracerProvider（stdout 导出器），并设为全局 tracer provider
//
// 返回 TracerProvider 供入口创建 root span，退出时须 Shutdown。
func Setup(serviceName string, debug bool) (*sdktrace.TracerProvider, error) {
	devCfg := zap.NewDevelopmentConfig()
	devCfg.OutputPaths = []string{"stdout"}
	level := zapcore.InfoLevel
	if debug {
		level = zapcore.DebugLevel
	}
	devCfg.Level = zap.NewAtomicLevelAt(level)
	zap.ReplaceGlobals(zap.Must(devCfg.Build()))

	exporter, err := stdouttrace.New(stdouttrace.WithPrettyPrint())
	if err != nil {
		return nil, err
	}
	res := resource.NewSchemaless(attribute.String("service.name", serviceName))
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	return tp, nil
}
