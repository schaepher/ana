package sqlite

import (
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

// TestTraceForwardIntermediateReads：① 跨函数闭环——目标字段的写入经
// "其他字段的读"（如 dest.Field = src.Field 的 struct 拷贝）为中间跳板时，
// 前向追踪须穿过中间读，连到目标字段的写入；其他字段的写入仍被过滤
// （避免参数全部使用入链的噪音）。
func TestTraceForwardIntermediateReads(t *testing.T) {
	r := newTestRepo(t)
	runID := "symbol:go:example.com/m:run"
	fillID := "symbol:go:example.com/m:fill"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(runID), Kind: domain.KindFunction, Name: "run"},
		{ID: domain.CanonicalID(fillID), Kind: domain.KindFunction, Name: "fill"},
		{ID: domain.CanonicalID(runID + "#c"), Kind: domain.KindSSAValue, Name: "c",
			Properties: map[string]any{"func_id": runID, "origin_kind": "param", "type_string": "*example.com/m.Dst"}},
		{ID: domain.CanonicalID(fillID + "#c"), Kind: domain.KindSSAValue, Name: "c",
			Properties: map[string]any{"func_id": fillID, "origin_kind": "param", "type_string": "*example.com/m.Dst"}},

		{ID: domain.CanonicalID(fillID + "#c.Src.Key.read@8"), Kind: domain.KindFieldAccess, Name: "c.Src.Key",
			Properties: map[string]any{"func_id": fillID, "full_path": "example.com/m.Src.Key",
				"access_kind": "read"}},

		{ID: domain.CanonicalID(fillID + "#c.Dst.Key.write@9"), Kind: domain.KindFieldAccess, Name: "c.Dst.Key",
			Properties: map[string]any{"func_id": fillID, "full_path": "example.com/m.Dst.Key",
				"access_kind": "write"}},

		{ID: domain.CanonicalID(fillID + "#c.Dst.Title.write@10"), Kind: domain.KindFieldAccess, Name: "c.Dst.Title",
			Properties: map[string]any{"func_id": fillID, "full_path": "example.com/m.Dst.Title",
				"access_kind": "write"}},
	}
	edges := []*domain.Fact{
		{SourceID: domain.CanonicalID(runID + "#c"), TargetID: domain.CanonicalID(fillID + "#c"),
			Kind: domain.FactArgument, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(fillID + "#c"), TargetID: domain.CanonicalID(fillID + "#c.Src.Key.read@8"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(fillID + "#c.Src.Key.read@8"), TargetID: domain.CanonicalID(fillID + "#c.Dst.Key.write@9"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},

		{SourceID: domain.CanonicalID(fillID + "#c"), TargetID: domain.CanonicalID(fillID + "#c.Dst.Title.write@10"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
	}
	save(t, r, nodes, edges)

	rows, err := r.TraceForward("example.com/m.Dst.Key", domain.CanonicalID(runID), 8)
	if err != nil {
		t.Fatal(err)
	}
	var hasWrite, hasHop bool
	for _, row := range rows {
		if string(row.ID) == fillID+"#c.Dst.Key.write@9" {
			hasWrite = true
		}
		if string(row.ID) == fillID+"#c.Src.Key.read@8" {
			hasHop = true
			if row.IsUsage {
				t.Error("中间读节点不应标记为使用点")
			}
		}
		if string(row.ID) == fillID+"#c.Dst.Title.write@10" {
			t.Errorf("其他字段的写入不应入链: %s", row.ID)
		}
	}
	if !hasWrite {
		t.Errorf("TraceForward 未连到目标字段写入（中间读被拦）: %+v", rows)
	}
	if !hasHop {
		t.Errorf("TraceForward 应含中间读跳板节点: %+v", rows)
	}
}

// TestTraceForwardStartTypeFiltered：B2 回归——trace-forward 起点必须与
// 目标字段所属结构体类型匹配（T / *T）；无关类型参数与全局变量不得成为
// 起点。此前 origin_kind IN ('param','receiver','alloc','global') 无条件
// 放行全部参数与全局变量，起点行直接输出造成噪音（gitCommit 等无关
// 全局、string 参数全入链）。
func TestTraceForwardStartTypeFiltered(t *testing.T) {
	r := newTestRepo(t)
	runID := "symbol:go:example.com/m:run"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(runID), Kind: domain.KindFunction, Name: "run"},

		{ID: domain.CanonicalID(runID + "#c"), Kind: domain.KindSSAValue, Name: "c",
			Properties: map[string]any{"func_id": runID, "origin_kind": "param", "type_string": "*example.com/m.Cfg"}},

		{ID: domain.CanonicalID(runID + "#name"), Kind: domain.KindSSAValue, Name: "name",
			Properties: map[string]any{"func_id": runID, "origin_kind": "param", "type_string": "string"}},

		{ID: domain.CanonicalID("symbol:go:example.com/m:var.gitCommit"), Kind: domain.KindSSAValue, Name: "gitCommit",
			Properties: map[string]any{"origin_kind": "global", "type_string": "string"}},

		{ID: domain.CanonicalID("symbol:go:example.com/m:var.gCfg"), Kind: domain.KindSSAValue, Name: "gCfg",
			Properties: map[string]any{"origin_kind": "global", "type_string": "*example.com/m.Cfg"}},

		{ID: domain.CanonicalID(runID + "#t1"), Kind: domain.KindSSAValue, Name: "t1",
			Properties: map[string]any{"func_id": runID, "origin_kind": "local", "type_string": "string"}},
		{ID: domain.CanonicalID(runID + "#t2"), Kind: domain.KindSSAValue, Name: "t2",
			Properties: map[string]any{"func_id": runID, "origin_kind": "local", "type_string": "string"}},

		{ID: domain.CanonicalID(runID + "#c.Key.write@8"), Kind: domain.KindFieldAccess, Name: "c.Key",
			Properties: map[string]any{"func_id": runID, "full_path": "example.com/m.Cfg.Key", "access_kind": "write"}},
	}
	edges := []*domain.Fact{
		{SourceID: domain.CanonicalID(runID + "#c"), TargetID: domain.CanonicalID(runID + "#c.Key.write@8"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(runID + "#name"), TargetID: domain.CanonicalID(runID + "#t1"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID("symbol:go:example.com/m:var.gitCommit"), TargetID: domain.CanonicalID(runID + "#t2"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
	}
	save(t, r, nodes, edges)

	rows, err := r.TraceForward("example.com/m.Cfg.Key", domain.CanonicalID(runID), 8)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		switch string(row.ID) {
		case runID + "#name", runID + "#t1", "symbol:go:example.com/m:var.gitCommit", runID + "#t2":
			t.Errorf("无关类型节点不应入链（起点类型过滤）: %s", row.ID)
		}
	}
	var hasC, hasWrite bool
	for _, row := range rows {
		if string(row.ID) == runID+"#c" {
			hasC = true
		}
		if string(row.ID) == runID+"#c.Key.write@8" {
			hasWrite = true
		}
	}
	if !hasC || !hasWrite {
		t.Errorf("目标类型起点/字段写缺失: %+v", rows)
	}
}
