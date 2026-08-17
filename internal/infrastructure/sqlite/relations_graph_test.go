package sqlite

import (
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

// TestGetTableRelationsBridge：循环读出桥（Q152）——BFS 到 ssa_value 节点
// （类型 []example.com/m.Session → Session）时，桥接同函数、同类型的字段
// 读取节点（非外部 read field_access，full_path 含类型名，下游 2 跳可达
// filter 外部节点）：对象读出的值经字段读取后进入 WHERE。
func TestGetTableRelationsBridge(t *testing.T) {
	r := newTestRepo(t)
	funcID := "symbol:go:example.com/m:find"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(funcID), Kind: domain.KindFunction, Name: "find"},
		// 表 A 读虚拟节点（起点）
		{ID: domain.CanonicalID(funcID + "#ext.sql.table_a.id.read@6"), Kind: domain.KindFieldAccess,
			Name: "table_a.id", FilePath: "a.go", LineStart: 6,
			Properties: map[string]any{"full_path": "table_a.id", "access_kind": "read",
				"type_string": "sql", "is_external": "true", "func_id": funcID}},
		// row 值（SSA value，类型 Session）
		{ID: domain.CanonicalID(funcID + "#t4"), Kind: domain.KindSSAValue, Name: "t4",
			Properties: map[string]any{"func_id": funcID, "type_string": "[]example.com/m.Session"}},
		// 桥节点：同函数读 Session 字段（非外部）
		{ID: domain.CanonicalID(funcID + "#n2"), Kind: domain.KindFieldAccess,
			Name: "st", FilePath: "a.go", LineStart: 8,
			Properties: map[string]any{"full_path": "m.Session.status", "access_kind": "read",
				"func_id": funcID, "is_external": "false"}},
		{ID: domain.CanonicalID(funcID + "#x"), Kind: domain.KindSSAValue, Name: "x",
			Properties: map[string]any{"func_id": funcID}},
		// 表 B 过滤虚拟节点（2 跳可达 filter）
		{ID: domain.CanonicalID(funcID + "#ext.sql.table_b.a_id.filter@10"), Kind: domain.KindFieldAccess,
			Name: "table_b.a_id", FilePath: "a.go", LineStart: 10,
			Properties: map[string]any{"full_path": "table_b.a_id", "access_kind": "filter",
				"type_string": "sql", "is_external": "true", "func_id": funcID}},
		// 干扰：同函数读 Session 但下游无 filter 的节点（不应被桥）
		{ID: domain.CanonicalID(funcID + "#n3"), Kind: domain.KindFieldAccess,
			Name: "other", FilePath: "a.go", LineStart: 12,
			Properties: map[string]any{"full_path": "m.Session.token", "access_kind": "read",
				"func_id": funcID, "is_external": "false"}},
	}
	edges := []*domain.Fact{
		{SourceID: domain.CanonicalID(funcID + "#ext.sql.table_a.id.read@6"), TargetID: domain.CanonicalID(funcID + "#t4"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#n2"), TargetID: domain.CanonicalID(funcID + "#x"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#x"), TargetID: domain.CanonicalID(funcID + "#ext.sql.table_b.a_id.filter@10"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
	}
	save(t, r, nodes, edges)

	rels, err := r.GetTableRelations("table_a", "")
	if err != nil {
		t.Fatalf("GetTableRelations: %v", err)
	}
	// 桥生效才有 table_b.a_id；无桥则 BFS 停在 t4（无外部命中）
	if len(rels) != 1 {
		t.Fatalf("rels = %+v, want 1（桥接 table_b.a_id）", rels)
	}
	if rels[0].ToTable != "table_b" || rels[0].ToCol != "a_id" {
		t.Errorf("relation = %+v, want table_b.a_id", rels[0])
	}
	if rels[0].Type != domain.RelationQuery {
		t.Errorf("type = %q, want query（终点 filter）", rels[0].Type)
	}
}

// TestGetTableRelationsXORM：Q175 修复——xorm 外部节点（type_string='xorm'）
// 也要参与关联终点判定（旧实现 byNode 只认 sql/gorm，xorm 表关联全丢）。
func TestGetTableRelationsXORM(t *testing.T) {
	r := newTestRepo(t)
	funcID := "symbol:go:example.com/m:find"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(funcID), Kind: domain.KindFunction, Name: "find"},
		{ID: domain.CanonicalID(funcID + "#ext.xorm.table_a.id.read@6"), Kind: domain.KindFieldAccess,
			Name: "table_a.id", FilePath: "a.go", LineStart: 6,
			Properties: map[string]any{"full_path": "table_a.id", "access_kind": "read",
				"type_string": "xorm", "is_external": "true", "func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#t4"), Kind: domain.KindSSAValue, Name: "t4",
			Properties: map[string]any{"func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#x"), Kind: domain.KindSSAValue, Name: "x",
			Properties: map[string]any{"func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#ext.xorm.table_b.a_id.filter@9"), Kind: domain.KindFieldAccess,
			Name: "table_b.a_id", FilePath: "a.go", LineStart: 9,
			Properties: map[string]any{"full_path": "table_b.a_id", "access_kind": "filter",
				"type_string": "xorm", "is_external": "true", "func_id": funcID}},
	}
	edges := []*domain.Fact{
		{SourceID: domain.CanonicalID(funcID + "#ext.xorm.table_a.id.read@6"), TargetID: domain.CanonicalID(funcID + "#t4"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#t4"), TargetID: domain.CanonicalID(funcID + "#x"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#x"), TargetID: domain.CanonicalID(funcID + "#ext.xorm.table_b.a_id.filter@9"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
	}
	save(t, r, nodes, edges)

	rels, err := r.GetTableRelations("table_a", "")
	if err != nil {
		t.Fatalf("GetTableRelations: %v", err)
	}
	if len(rels) != 1 {
		t.Fatalf("rels = %+v, want 1（xorm 表关联）", rels)
	}
	if rels[0].ToTable != "table_b" || rels[0].Type != domain.RelationQuery {
		t.Errorf("relation = %+v, want table_b.a_id query", rels[0])
	}
}

// TestGetTableRelationsBridgeDirectional：桥 2 跳检查是定向出边
// （旧 SQL EXISTS：e1.source_id = n2.id）——只有"反向边"连到 filter 的
// read 节点不应被桥（双向误桥会引入噪音关联）。
func TestGetTableRelationsBridgeDirectional(t *testing.T) {
	r := newTestRepo(t)
	funcID := "symbol:go:example.com/m:find"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(funcID), Kind: domain.KindFunction, Name: "find"},
		{ID: domain.CanonicalID(funcID + "#ext.sql.table_a.id.read@6"), Kind: domain.KindFieldAccess,
			Name: "table_a.id", FilePath: "a.go", LineStart: 6,
			Properties: map[string]any{"full_path": "table_a.id", "access_kind": "read",
				"type_string": "sql", "is_external": "true", "func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#t4"), Kind: domain.KindSSAValue, Name: "t4",
			Properties: map[string]any{"func_id": funcID, "type_string": "[]example.com/m.Session"}},
		// 正向桥：n2 → x → filter
		{ID: domain.CanonicalID(funcID + "#n2"), Kind: domain.KindFieldAccess,
			Name: "st", FilePath: "a.go", LineStart: 8,
			Properties: map[string]any{"full_path": "m.Session.status", "access_kind": "read",
				"func_id": funcID, "is_external": "false"}},
		{ID: domain.CanonicalID(funcID + "#x"), Kind: domain.KindSSAValue, Name: "x",
			Properties: map[string]any{"func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#ext.sql.table_b.a_id.filter@10"), Kind: domain.KindFieldAccess,
			Name: "table_b.a_id", FilePath: "a.go", LineStart: 10,
			Properties: map[string]any{"full_path": "table_b.a_id", "access_kind": "filter",
				"type_string": "sql", "is_external": "true", "func_id": funcID}},
		// 反向误桥陷阱：m 只有入边来自 filter（filter→m），出边到表 D——
		// 定向语义下 m 不被桥，BFS 不应到达 table_d
		{ID: domain.CanonicalID(funcID + "#m"), Kind: domain.KindFieldAccess,
			Name: "st2", FilePath: "a.go", LineStart: 12,
			Properties: map[string]any{"full_path": "m.Session.token", "access_kind": "read",
				"func_id": funcID, "is_external": "false"}},
		{ID: domain.CanonicalID(funcID + "#d"), Kind: domain.KindSSAValue, Name: "d",
			Properties: map[string]any{"func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#ext.sql.table_d.z.read@15"), Kind: domain.KindFieldAccess,
			Name: "table_d.z", FilePath: "a.go", LineStart: 15,
			Properties: map[string]any{"full_path": "table_d.z", "access_kind": "read",
				"type_string": "sql", "is_external": "true", "func_id": funcID}},
	}
	edges := []*domain.Fact{
		{SourceID: domain.CanonicalID(funcID + "#ext.sql.table_a.id.read@6"), TargetID: domain.CanonicalID(funcID + "#t4"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#n2"), TargetID: domain.CanonicalID(funcID + "#x"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#x"), TargetID: domain.CanonicalID(funcID + "#ext.sql.table_b.a_id.filter@10"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
		// 反向入边（非 data 边，BFS 不会直接走——只能经桥到达 m）：
		// filter → m。定向桥语义下 m 不被桥 → BFS 不应到达 table_d
		{SourceID: domain.CanonicalID(funcID + "#ext.sql.table_b.a_id.filter@10"), TargetID: domain.CanonicalID(funcID + "#m"),
			Kind: domain.FactKind("uses"), ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#m"), TargetID: domain.CanonicalID(funcID + "#d"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#d"), TargetID: domain.CanonicalID(funcID + "#ext.sql.table_d.z.read@15"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
	}
	save(t, r, nodes, edges)

	rels, err := r.GetTableRelations("table_a", "full")
	if err != nil {
		t.Fatalf("GetTableRelations: %v", err)
	}
	// 只有 table_b.a_id（query）；m 反向边不被桥 → 无 table_d
	if len(rels) != 1 {
		t.Fatalf("rels = %+v, want 仅 table_b.a_id（反向边不触发桥）", rels)
	}
	if rels[0].ToTable != "table_b" {
		t.Errorf("relation = %+v, want table_b.a_id", rels[0])
	}
}

// TestRelationMemoryModes：内存 BFS（full）与逐节点 SQL（sql）两路径
// 结果一致（P0④ --memory 参数：大仓库强制 SQL 防爆内存）。
func TestRelationMemoryModes(t *testing.T) {
	r := newTestRepo(t)
	funcID := "symbol:go:example.com/m:find"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(funcID), Kind: domain.KindFunction, Name: "find"},
		{ID: domain.CanonicalID(funcID + "#ext.sql.table_a.id.read@6"), Kind: domain.KindFieldAccess,
			Name: "table_a.id", FilePath: "a.go", LineStart: 6,
			Properties: map[string]any{"full_path": "table_a.id", "access_kind": "read",
				"type_string": "sql", "is_external": "true", "func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#t4"), Kind: domain.KindSSAValue, Name: "t4",
			Properties: map[string]any{"func_id": funcID, "type_string": "[]example.com/m.Session"}},
		{ID: domain.CanonicalID(funcID + "#n2"), Kind: domain.KindFieldAccess,
			Name: "st", FilePath: "a.go", LineStart: 8,
			Properties: map[string]any{"full_path": "m.Session.status", "access_kind": "read",
				"func_id": funcID, "is_external": "false"}},
		{ID: domain.CanonicalID(funcID + "#x"), Kind: domain.KindSSAValue, Name: "x",
			Properties: map[string]any{"func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#ext.sql.table_b.a_id.filter@10"), Kind: domain.KindFieldAccess,
			Name: "table_b.a_id", FilePath: "a.go", LineStart: 10,
			Properties: map[string]any{"full_path": "table_b.a_id", "access_kind": "filter",
				"type_string": "sql", "is_external": "true", "func_id": funcID}},
	}
	edges := []*domain.Fact{
		{SourceID: domain.CanonicalID(funcID + "#ext.sql.table_a.id.read@6"), TargetID: domain.CanonicalID(funcID + "#t4"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#n2"), TargetID: domain.CanonicalID(funcID + "#x"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#x"), TargetID: domain.CanonicalID(funcID + "#ext.sql.table_b.a_id.filter@10"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
	}
	save(t, r, nodes, edges)

	full, err := r.GetTableRelations("table_a", "full")
	if err != nil {
		t.Fatalf("full: %v", err)
	}
	sqlRels, err := r.GetTableRelations("table_a", "sql")
	if err != nil {
		t.Fatalf("sql: %v", err)
	}
	if len(full) != 1 || len(sqlRels) != 1 {
		t.Fatalf("full=%+v sql=%+v, want 各 1 条", full, sqlRels)
	}
	a, b := full[0], sqlRels[0]
	if a.FromCol != b.FromCol || a.ToTable != b.ToTable || a.ToCol != b.ToCol ||
		a.Hops != b.Hops || a.Type != b.Type {
		t.Errorf("full=%+v sql=%+v 不一致", a, b)
	}
	// auto 模式（fixture 无 counts 元数据 → 小库走内存路径）结果一致
	auto, err := r.GetTableRelations("table_a", "")
	if err != nil || len(auto) != 1 {
		t.Fatalf("auto: %v %v", auto, err)
	}
}

// TestBuildMetaCounts：节点/边数随构建元数据缓存（--memory auto 判断用，
// 不每次重新 COUNT）。
func TestBuildMetaCounts(t *testing.T) {
	r := newTestRepo(t)
	if err := r.Save(&domain.BuildMeta{BuildID: "b1", ToolName: "all", Status: "success", Nodes: 100, Edges: 50}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	m, err := r.GetLatest()
	if err != nil {
		t.Fatalf("GetLatest: %v", err)
	}
	if m.Nodes != 100 || m.Edges != 50 {
		t.Errorf("counts = %d/%d, want 100/50", m.Nodes, m.Edges)
	}
}

// TestGetTableRelationsTypeRank：同列多节点（read + write）时 Type 取
// rank 最高（query > write > read）——与遍历顺序无关（旧实现只升级 query，
// write 不覆盖 read，结果依赖 map 遍历顺序不确定）。
func TestGetTableRelationsTypeRank(t *testing.T) {
	r := newTestRepo(t)
	funcID := "symbol:go:example.com/m:find"
	mk := func(id, name, access string, line int) *domain.CodeEntity {
		return &domain.CodeEntity{
			ID: domain.CanonicalID(funcID + id), Kind: domain.KindFieldAccess,
			Name: name, FilePath: "a.go", LineStart: line,
			Properties: map[string]any{"full_path": name, "access_kind": access,
				"type_string": "sql", "is_external": "true", "func_id": funcID},
		}
	}
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(funcID), Kind: domain.KindFunction, Name: "find"},
		// 起点：table_a.id read
		mk("#ext.sql.table_a.id.read@6", "table_a.id", "read", 6),
		// 两条链到 table_b.a_id：一条终点 read 节点、一条终点 write 节点
		mk("#v1", "v1", "", 7), mk("#v2", "v2", "", 8), mk("#v3", "v3", "", 9),
		mk("#w1", "w1", "", 10), mk("#w2", "w2", "", 11),
		mk("#ext.sql.table_b.a_id.read@12", "table_b.a_id", "read", 12),
		mk("#ext.sql.table_b.a_id.write@13", "table_b.a_id", "write", 13),
	}
	edges := []*domain.Fact{
		{SourceID: domain.CanonicalID(funcID + "#ext.sql.table_a.id.read@6"), TargetID: domain.CanonicalID(funcID + "#v1"), Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#v1"), TargetID: domain.CanonicalID(funcID + "#v2"), Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#v2"), TargetID: domain.CanonicalID(funcID + "#ext.sql.table_b.a_id.read@12"), Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#v1"), TargetID: domain.CanonicalID(funcID + "#w1"), Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#w1"), TargetID: domain.CanonicalID(funcID + "#w2"), Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#w2"), TargetID: domain.CanonicalID(funcID + "#ext.sql.table_b.a_id.write@13"), Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
	}
	save(t, r, nodes, edges)
	rels, err := r.GetTableRelations("table_a", "")
	if err != nil {
		t.Fatalf("GetTableRelations: %v", err)
	}
	if len(rels) != 1 {
		t.Fatalf("rels = %+v, want 1（table_b.a_id）", rels)
	}
	// 终点 read 与 write 并存 → 取 write（rank 更高），与遍历顺序无关
	if rels[0].Type != domain.RelationWrite {
		t.Errorf("type = %q, want write（read+write 并存取 rank 最高）", rels[0].Type)
	}
}

// TestRelationCandidatesCache：relation_candidates 缓存语义（P0③）——
// ① 有 build_id 时单表结果写缓存，图状态变化后仍返回缓存；
// ② build_id 变化 → 缓存失效 → 现场重算；
// ③ 无 build_metadata 时跳过缓存（不写行）。
func TestRelationCandidatesCache(t *testing.T) {
	r := newTestRepo(t)
	funcID := "symbol:go:example.com/m:find"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(funcID), Kind: domain.KindFunction, Name: "find"},
		{ID: domain.CanonicalID(funcID + "#ext.sql.table_a.id.read@6"), Kind: domain.KindFieldAccess,
			Name: "table_a.id", FilePath: "a.go", LineStart: 6,
			Properties: map[string]any{"full_path": "table_a.id", "access_kind": "read",
				"type_string": "sql", "is_external": "true", "func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#t4"), Kind: domain.KindSSAValue, Name: "t4",
			Properties: map[string]any{"func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#x"), Kind: domain.KindSSAValue, Name: "x",
			Properties: map[string]any{"func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#ext.sql.table_b.a_id.filter@9"), Kind: domain.KindFieldAccess,
			Name: "table_b.a_id", FilePath: "a.go", LineStart: 9,
			Properties: map[string]any{"full_path": "table_b.a_id", "access_kind": "filter",
				"type_string": "sql", "is_external": "true", "func_id": funcID}},
	}
	edges := []*domain.Fact{
		{SourceID: domain.CanonicalID(funcID + "#ext.sql.table_a.id.read@6"), TargetID: domain.CanonicalID(funcID + "#t4"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#t4"), TargetID: domain.CanonicalID(funcID + "#x"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#x"), TargetID: domain.CanonicalID(funcID + "#ext.sql.table_b.a_id.filter@9"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
	}
	save(t, r, nodes, edges)

	// ③ 无 build_metadata：现场算，不写缓存
	rels, err := r.GetTableRelations("table_a", "")
	if err != nil || len(rels) != 1 {
		t.Fatalf("first rels = %v, %v", rels, err)
	}
	var cnt int
	if err := r.QueryRow(`SELECT COUNT(*) FROM relation_candidates`).Scan(&cnt); err != nil {
		t.Fatalf("count relation_candidates: %v", err)
	}
	if cnt != 0 {
		t.Fatalf("无 build_id 不应写缓存，got %d 行", cnt)
	}

	// ① 写 build_id → 现场算并写缓存；改图后仍返回缓存
	if err := r.Save(&domain.BuildMeta{BuildID: "b1", ToolName: "all", Status: "success"}); err != nil {
		t.Fatalf("Save build: %v", err)
	}
	rels, err = r.GetTableRelations("table_a", "")
	if err != nil || len(rels) != 1 {
		t.Fatalf("b1 rels = %v, %v", rels, err)
	}
	if _, err := r.Exec(`DELETE FROM nodes WHERE id = ?`, domain.CanonicalID(funcID+"#ext.sql.table_b.a_id.filter@9")); err != nil {
		t.Fatalf("delete node: %v", err)
	}
	rels, err = r.GetTableRelations("table_a", "")
	if err != nil || len(rels) != 1 {
		t.Fatalf("缓存命中应仍返回 1 条，got %v, %v", rels, err)
	}

	// ② build_id 变化 → 失效重算（filter 节点已删 → 0 条）
	if err := r.Save(&domain.BuildMeta{BuildID: "b2", ToolName: "all", Status: "success"}); err != nil {
		t.Fatalf("Save build b2: %v", err)
	}
	rels, err = r.GetTableRelations("table_a", "")
	if err != nil {
		t.Fatalf("b2 rels: %v", err)
	}
	if len(rels) != 0 {
		t.Fatalf("b2 缓存失效应重算为 0 条，got %+v", rels)
	}
}

// TestGetAllTableRelationsRebuildCache：--all 全量重建缓存——先算完
// 单表（缓存只有 table_a），--all 后 relation_candidates 覆盖为全部表。
func TestGetAllTableRelationsRebuildCache(t *testing.T) {
	r := newTestRepo(t)
	funcID := "symbol:go:example.com/m:find"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(funcID), Kind: domain.KindFunction, Name: "find"},
		{ID: domain.CanonicalID(funcID + "#ext.sql.table_a.id.read@6"), Kind: domain.KindFieldAccess,
			Name: "table_a.id", FilePath: "a.go", LineStart: 6,
			Properties: map[string]any{"full_path": "table_a.id", "access_kind": "read",
				"type_string": "sql", "is_external": "true", "func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#t4"), Kind: domain.KindSSAValue, Name: "t4",
			Properties: map[string]any{"func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#x"), Kind: domain.KindSSAValue, Name: "x",
			Properties: map[string]any{"func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#ext.sql.table_b.a_id.filter@9"), Kind: domain.KindFieldAccess,
			Name: "table_b.a_id", FilePath: "a.go", LineStart: 9,
			Properties: map[string]any{"full_path": "table_b.a_id", "access_kind": "filter",
				"type_string": "sql", "is_external": "true", "func_id": funcID}},
	}
	edges := []*domain.Fact{
		{SourceID: domain.CanonicalID(funcID + "#ext.sql.table_a.id.read@6"), TargetID: domain.CanonicalID(funcID + "#t4"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#t4"), TargetID: domain.CanonicalID(funcID + "#x"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#x"), TargetID: domain.CanonicalID(funcID + "#ext.sql.table_b.a_id.filter@9"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
	}
	save(t, r, nodes, edges)
	if err := r.Save(&domain.BuildMeta{BuildID: "b1", ToolName: "all", Status: "success"}); err != nil {
		t.Fatalf("Save build: %v", err)
	}
	// 先单表（缓存只写 table_a）
	if _, err := r.GetTableRelations("table_a", ""); err != nil {
		t.Fatalf("GetTableRelations: %v", err)
	}
	rels, err := r.GetAllTableRelations("")
	if err != nil {
		t.Fatalf("GetAllTableRelations: %v", err)
	}
	// 正向 query + 反向 read
	if len(rels) != 2 {
		t.Fatalf("rels = %+v, want 2", rels)
	}
	var fromTables int
	if err := r.QueryRow(`SELECT COUNT(DISTINCT from_table) FROM relation_candidates WHERE build_id = 'b1'`).Scan(&fromTables); err != nil {
		t.Fatalf("count: %v", err)
	}
	if fromTables != 2 {
		t.Fatalf("--all 后缓存应覆盖 2 张表，got %d", fromTables)
	}
}
