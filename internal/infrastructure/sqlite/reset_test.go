package sqlite

import (
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

// TestResetGraphTables：全量重建的清表语义——DROP+CREATE 图数据表
// （edges/nodes/function_field_summary），build_metadata 保留
// （构建记录与未来的配置表不丢）；重建后可正常写入。
func TestResetGraphTables(t *testing.T) {
	r := newTestRepo(t)
	funcID := "symbol:go:example.com/m:main"
	n := node(funcID, "function", "main", "main.go")
	n.LineStart = 1
	save(t, r, []*domain.CodeEntity{n}, []*domain.Fact{{
		SourceID: n.ID, TargetID: n.ID, Kind: domain.FactCalls,
		ToolSource: domain.ToolCodeGraph, Confidence: 0.8,
	}})
	if _, err := r.SaveBatchStats(nil, nil, []*domain.FunctionFieldSummary{{
		FunctionID: n.ID, AccessKind: domain.SummaryDirectRead,
		FieldPath: "example.com/m.T.A", InstancePath: "t.A",
	}}); err != nil {
		t.Fatalf("save summary: %v", err)
	}
	if err := r.Save(&domain.BuildMeta{BuildID: "b1", ToolName: "all", Status: "success"}); err != nil {
		t.Fatalf("save meta: %v", err)
	}

	if err := r.ResetGraphTables(); err != nil {
		t.Fatalf("ResetGraphTables: %v", err)
	}

	// 图数据清空
	nodes, edges, err := r.Counts()
	if err != nil || nodes != 0 || edges != 0 {
		t.Fatalf("清表后 nodes=%d edges=%d err=%v, want 0/0", nodes, edges, err)
	}
	summaries, err := r.AllSummaries()
	if err != nil || len(summaries) != 0 {
		t.Fatalf("清表后 summaries = %d, err=%v", len(summaries), err)
	}
	// build_metadata 保留
	meta, err := r.GetLatest()
	if err != nil || meta == nil || meta.BuildID != "b1" {
		t.Fatalf("build_metadata 应保留: %+v err=%v", meta, err)
	}
	// 重建后可正常写入（schema 完整）
	save(t, r, []*domain.CodeEntity{n}, nil)
	if got, _, err := r.Counts(); err != nil || got != 1 {
		t.Fatalf("重建后写入 nodes=%d err=%v", got, err)
	}
	// 重建后 schema 版本不变
	var v int
	if err := r.QueryRow("PRAGMA user_version").Scan(&v); err != nil || v != SchemaVersion {
		t.Errorf("schema version = %d err=%v, want %d", v, err, SchemaVersion)
	}
}
