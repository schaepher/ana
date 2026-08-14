// Package logging 提供 context 与 *zap.Logger 的互转，以及 OpenTelemetry
// 链路追踪初始化。entrylog 生成的日志代码使用：函数含 ctx 参数时通过
// FromContext 取出 logger（自动携带 trace_id/span_id），无 ctx 时用 zap.L()。
package logging

import (
	"context"
	"os"
	"path/filepath"

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

// debugEnabled 记录 Setup 的 debug 标志，ToFile 重建 logger 时沿用
// （zap.Logger 无公开的级别读取接口）。
var debugEnabled bool

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
//   - zap development logger，默认输出到 stdout（logDir 非空时写入
//     logDir/.codeintel/codeintel.log，与 db 同目录，Q88——root span
//     与早期日志从创建起即走文件，避免 stdout 混流）；默认 Info 级，
//     debug=true（CLI --verbose/--debug）时输出 Debug 级
//   - OTel TracerProvider（stdout 或文件导出器），并设为全局 tracer provider
//
// 返回 TracerProvider 供入口创建 root span，退出时须 Shutdown。
func Setup(serviceName string, debug bool, logDir string) (*sdktrace.TracerProvider, error) {
	debugEnabled = debug
	logPath := ""
	if logDir != "" {
		if err := os.MkdirAll(filepath.Join(logDir, ".codeintel"), 0o755); err != nil {
			return nil, err
		}
		logPath = filepath.Join(logDir, ".codeintel", "codeintel.log")
	}
	buildLogger(logPath, debug)

	exporter, err := stdouttrace.New(stdouttrace.WithPrettyPrint())
	if err != nil {
		return nil, err
	}
	if logPath != "" {
		f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return nil, err
		}
		exporter, err = stdouttrace.New(stdouttrace.WithWriter(f), stdouttrace.WithPrettyPrint())
		if err != nil {
			f.Close()
			return nil, err
		}
	}
	res := resource.NewSchemaless(attribute.String("service.name", serviceName))
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	return tp, nil
}

// buildLogger 重建全局 zap logger（logPath 为空时写 stdout，否则写文件）。
func buildLogger(logPath string, debug bool) {
	level := zapcore.InfoLevel
	if debug {
		level = zapcore.DebugLevel
	}
	devCfg := zap.NewDevelopmentConfig()
	devCfg.Level = zap.NewAtomicLevelAt(level)
	if logPath != "" {
		devCfg.OutputPaths = []string{logPath}
	}
	zap.ReplaceGlobals(zap.Must(devCfg.Build()))
}

// ToFile 将全局日志（zap）与 OTel 追踪切换到 dir 下的 codeintel.log
// （与 codeintel.db 同目录，Q88：stdout 只承载查询结果）。
// 保留当前日志级别（debug 标志随 Setup 传入）；幂等，可重复调用。
// 调用时机：各命令解析 --repo 后。
func ToFile(dir string) error {
	logDir := filepath.Join(dir, ".codeintel")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return err
	}
	logPath := filepath.Join(logDir, "codeintel.log")
	buildLogger(logPath, debugEnabled)

	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	exporter, err := stdouttrace.New(stdouttrace.WithWriter(f), stdouttrace.WithPrettyPrint())
	if err != nil {
		f.Close()
		return err
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(resource.NewSchemaless(attribute.String("service.name", "codeintel"))),
	)
	otel.SetTracerProvider(tp)
	return nil
}
