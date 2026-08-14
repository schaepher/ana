package logging

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/sdk/trace"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// TestToFile：日志切换到文件后，zap 日志写入指定文件（.codeintel/ 同目录）。
func TestToFile(t *testing.T) {
	if _, err := Setup("test", false, ""); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	dir := t.TempDir()
	if err := ToFile(dir); err != nil {
		t.Fatalf("ToFile: %v", err)
	}
	zap.L().Info("hello-file")

	data, err := os.ReadFile(filepath.Join(dir, ".codeintel", "codeintel.log"))
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	if !strings.Contains(string(data), "hello-file") {
		t.Errorf("log file 应含日志行: %q", data)
	}
}

// TestToFileTwice：重复调用 ToFile 不报错（幂等重建 logger）。
func TestToFileTwice(t *testing.T) {
	if _, err := Setup("test", false, ""); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	dir := t.TempDir()
	if err := ToFile(dir); err != nil {
		t.Fatalf("ToFile: %v", err)
	}
	if err := ToFile(dir); err != nil {
		t.Fatalf("ToFile twice: %v", err)
	}
	zap.L().Info("twice")
	if _, err := os.Stat(filepath.Join(dir, ".codeintel", "codeintel.log")); err != nil {
		t.Errorf("log file missing: %v", err)
	}
}

func TestWithLoggerFromContext(t *testing.T) {
	// nil ctx 回退 zap.L()，不 panic
	if FromContext(nil) == nil {
		t.Error("FromContext(nil) returned nil")
	}
	// 未存入 logger 的 ctx 回退 zap.L()
	if FromContext(context.Background()) == nil {
		t.Error("FromContext(empty ctx) returned nil")
	}
	// 存入后取出应是同一个实例
	l := zap.NewNop()
	ctx := WithLogger(context.Background(), l)
	if got := FromContext(ctx); got != l {
		t.Error("FromContext should return the stored logger")
	}
}

func TestFromContextSpanFields(t *testing.T) {
	core, observed := observer.New(zapcore.DebugLevel)
	logger := zap.New(core)
	ctx := WithLogger(context.Background(), logger)

	// 无 span 的 ctx：不附加 trace 字段
	FromContext(ctx).Info("no span")
	if len(observed.TakeAll()) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(observed.All()))
	}
	for _, e := range observed.All() {
		if _, ok := e.ContextMap()["trace_id"]; ok {
			t.Error("entry without span should not carry trace_id")
		}
	}
	observed.TakeAll()

	// 带 span context 的 ctx：附加 trace_id / span_id（tr.Start 返回的
	// ctx 已携带 span context）
	tp := trace.NewTracerProvider()
	tr := tp.Tracer("test")
	sctx, span := tr.Start(ctx, "op")
	defer span.End()
	FromContext(sctx).Info("with span")

	entries := observed.TakeAll()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	m := entries[0].ContextMap()
	if m["trace_id"] == "" || m["span_id"] == "" {
		t.Errorf("entry should carry trace_id/span_id, got %v", m)
	}
}

func TestSetup(t *testing.T) {
	tp, err := Setup("test-service", false, "")
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if tp == nil {
		t.Fatal("Setup returned nil TracerProvider")
	}
	if err := tp.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown: %v", err)
	}
}
