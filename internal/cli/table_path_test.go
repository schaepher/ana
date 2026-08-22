package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
)

// Q241 table-path：表 A → 表 B 数据通路（跨 mapping 表/关联表）。
// 复用 relations 图（relation_candidates 全量）建表级邻接 → BFS 最短
// 跳数 + 类型优先级（fk>query>write>read）最优路径；--json 全列候选。

// seedTablePathFixture 建三表链 + 直接关联 + 不可达表：
//
//	table_a.id → (链) → table_map.a_id（跨跳 1）
//	table_map.m_id → (链) → table_b.b_id（跨跳 2）
//	table_a.id → (链) → table_d.d_id（直接关联）
//	table_z 无任何关联
func seedTablePathFixture(t *testing.T) string {
	t.Helper()
	dir := seedRepo(t)
	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	r := sqlite.NewRepo(db)
	funcID := domain.CanonicalID("symbol:go:example.com/m:find")
	// 外部表虚拟节点（每表一列 read/filter）
	nodes := []*domain.CodeEntity{
		{ID: funcID, Kind: domain.KindFunction, Name: "find", FilePath: "a.go"},
		{ID: funcID + "#ext.sql.table_a.id.read@6", Kind: domain.KindFieldAccess,
			Name: "table_a.id", FilePath: "a.go", LineStart: 6,
			Properties: map[string]any{"full_path": "table_a.id", "access_kind": "read",
				"type_string": "sql", "is_external": "true", "func_id": string(funcID)}},
		{ID: funcID + "#ext.sql.table_map.a_id.filter@9", Kind: domain.KindFieldAccess,
			Name: "table_map.a_id", FilePath: "a.go", LineStart: 9,
			Properties: map[string]any{"full_path": "table_map.a_id", "access_kind": "filter",
				"type_string": "sql", "is_external": "true", "func_id": string(funcID)}},
		{ID: funcID + "#ext.sql.table_map.m_id.read@12", Kind: domain.KindFieldAccess,
			Name: "table_map.m_id", FilePath: "a.go", LineStart: 12,
			Properties: map[string]any{"full_path": "table_map.m_id", "access_kind": "read",
				"type_string": "sql", "is_external": "true", "func_id": string(funcID)}},
		{ID: funcID + "#ext.sql.table_b.b_id.filter@15", Kind: domain.KindFieldAccess,
			Name: "table_b.b_id", FilePath: "a.go", LineStart: 15,
			Properties: map[string]any{"full_path": "table_b.b_id", "access_kind": "filter",
				"type_string": "sql", "is_external": "true", "func_id": string(funcID)}},
		{ID: funcID + "#ext.sql.table_d.d_id.filter@18", Kind: domain.KindFieldAccess,
			Name: "table_d.d_id", FilePath: "a.go", LineStart: 18,
			Properties: map[string]any{"full_path": "table_d.d_id", "access_kind": "filter",
				"type_string": "sql", "is_external": "true", "func_id": string(funcID)}},
		{ID: funcID + "#ext.sql.table_z.id.read@21", Kind: domain.KindFieldAccess,
			Name: "table_z.id", FilePath: "a.go", LineStart: 21,
			Properties: map[string]any{"full_path": "table_z.id", "access_kind": "read",
				"type_string": "sql", "is_external": "true", "func_id": string(funcID)}},
		// table_e 双链：a_id filter（列名呼应 fk）+ e_id filter（read 值流链）
		{ID: funcID + "#ext.sql.table_e.a_id.filter@24", Kind: domain.KindFieldAccess,
			Name: "table_e.a_id", FilePath: "a.go", LineStart: 24,
			Properties: map[string]any{"full_path": "table_e.a_id", "access_kind": "filter",
				"type_string": "sql", "is_external": "true", "func_id": string(funcID)}},
		{ID: funcID + "#ext.sql.table_e.e_id.filter@27", Kind: domain.KindFieldAccess,
			Name: "table_e.e_id", FilePath: "a.go", LineStart: 27,
			Properties: map[string]any{"full_path": "table_e.e_id", "access_kind": "filter",
				"type_string": "sql", "is_external": "true", "func_id": string(funcID)}},
	}
	// 中间 SSA 值节点（值流链端点须存在——SaveBatchStats 外键约束静默跳过缺失端点）
	fid := string(funcID)
	for _, mid := range []string{"t1", "t2", "t3", "t4", "x1", "x2", "x3", "x4"} {
		nodes = append(nodes, &domain.CodeEntity{
			ID: domain.CanonicalID(fid + "#" + mid), Kind: domain.KindSSAValue, Name: mid,
			Properties: map[string]any{"func_id": string(funcID)},
		})
	}
	// 值流链：每链 = read → tN → x → filter
	mk := func(readID, mid, xID, filterID string) []*domain.Fact {
		return []*domain.Fact{
			{SourceID: domain.CanonicalID(fid + readID), TargetID: domain.CanonicalID(fid + mid), Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
			{SourceID: domain.CanonicalID(fid + mid), TargetID: domain.CanonicalID(fid + xID), Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
			{SourceID: domain.CanonicalID(fid + xID), TargetID: domain.CanonicalID(fid + filterID), Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
		}
	}
	var facts []*domain.Fact
	for _, chain := range [][4]string{
		{"#ext.sql.table_a.id.read@6", "#t1", "#x1", "#ext.sql.table_map.a_id.filter@9"},
		{"#ext.sql.table_map.m_id.read@12", "#t2", "#x2", "#ext.sql.table_b.b_id.filter@15"},
		{"#ext.sql.table_a.id.read@6", "#t3", "#x3", "#ext.sql.table_d.d_id.filter@18"},
		// read 值流链（table_e.e_id 与 table_a 无列名呼应 → read 类型）
		{"#ext.sql.table_a.id.read@6", "#t4", "#x4", "#ext.sql.table_e.e_id.filter@27"},
	} {
		facts = append(facts, mk(chain[0], chain[1], chain[2], chain[3])...)
	}
	if _, err := r.SaveBatchStats(nodes, facts, nil); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := r.Save(&domain.BuildMeta{BuildID: "b1", ToolName: "all", Status: "success"}); err != nil {
		t.Fatalf("Save build: %v", err)
	}
	if err := r.PrecomputeAllRelations(nil); err != nil {
		t.Fatalf("precompute: %v", err)
	}
	return dir
}

// TestTablePathDirect：直接关联（table_a → table_d 一步）。
func TestTablePathDirect(t *testing.T) {
	dir := seedTablePathFixture(t)
	out := captureStdout(func() {
		if code := cmdQuery([]string{"table-path", "table_a", "table_d", "--repo", dir}); code != 0 {
			t.Errorf("table-path exit = %d", code)
		}
	})
	if !strings.Contains(out, "table_a") || !strings.Contains(out, "table_d") {
		t.Errorf("通路输出缺表名:\n%s", out)
	}
	if !strings.Contains(out, "table_d") {
		t.Errorf("通路应达 table_d:\n%s", out)
	}
}

// TestTablePathViaMapping：跨 mapping 表（table_a → table_map → table_b）。
func TestTablePathViaMapping(t *testing.T) {
	dir := seedTablePathFixture(t)
	out := captureStdout(func() {
		if code := cmdQuery([]string{"table-path", "table_a", "table_b", "--repo", dir}); code != 0 {
			t.Errorf("table-path exit = %d", code)
		}
	})
	for _, want := range []string{"table_a", "table_map", "table_b"} {
		if !strings.Contains(out, want) {
			t.Errorf("通路应经过 %s:\n%s", want, out)
		}
	}
}

// TestTablePathUnreachable：不可达（--max-hops 内无通路）。
func TestTablePathUnreachable(t *testing.T) {
	dir := seedTablePathFixture(t)
	out := captureStdout(func() {
		if code := cmdQuery([]string{"table-path", "table_a", "table_z", "--repo", dir}); code == 0 {
			t.Errorf("不可达应 exit 非零")
		}
	})
	if !strings.Contains(out, "不可达") {
		t.Errorf("不可达提示:\n%s", out)
	}
}

// TestTablePathJSON：--json 结构（path 步骤 + hops + reachable）。
func TestTablePathJSON(t *testing.T) {
	dir := seedTablePathFixture(t)
	out := captureStdout(func() {
		if code := cmdQuery([]string{"table-path", "table_a", "table_b", "--repo", dir, "--json"}); code != 0 {
			t.Errorf("table-path --json exit = %d", code)
		}
	})
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("json: %v\n%s", err, out)
	}
	if m["reachable"] != true {
		t.Errorf("reachable = %v, want true", m["reachable"])
	}
	steps, ok := m["path"].([]any)
	if !ok || len(steps) < 2 {
		t.Fatalf("path 应含 ≥2 步（跨 mapping 表）: %v", m["path"])
	}
	first := steps[0].(map[string]any)
	if first["from_table"] != "table_a" {
		t.Errorf("首步 from = %v, want table_a", first["from_table"])
	}
	last := steps[len(steps)-1].(map[string]any)
	if last["to_table"] != "table_b" {
		t.Errorf("末步 to = %v, want table_b", last["to_table"])
	}
}

// TestTablePathPriority：同跳数多路径——类型优先级选 fk（table_a →
// table_e 有 fk 呼应 + read 值流两条 1 跳链，应输出 fk）。
func TestTablePathPriority(t *testing.T) {
	dir := seedTablePathFixture(t)
	out := captureStdout(func() {
		if code := cmdQuery([]string{"table-path", "table_a", "table_e", "--repo", dir}); code != 0 {
			t.Errorf("table-path exit = %d", code)
		}
	})
	if !strings.Contains(out, "[fk]") {
		t.Errorf("同跳数多路径应优先 fk 链（当前输出）:\n%s", out)
	}
}
