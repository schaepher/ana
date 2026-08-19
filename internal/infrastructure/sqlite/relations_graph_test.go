package sqlite

import (
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

// TestGetTableRelationsBridge：循环读出桥（Q152）——BFS 到 ssa_value 节点
// （类型 []example.com/m.Session → Session）时，桥接同函数、同类型的字段
// 读取节点（非外部 read field_access，full_path 含类型名，下游 2 跳可达
// filter 外部节点）：对象读出的值经字段读取后进入 WHERE。

// TestGetTableRelationsXORM：Q175 修复——xorm 外部节点（type_string='xorm'）
// 也要参与关联终点判定（旧实现 byNode 只认 sql/gorm，xorm 表关联全丢）。

// TestGetTableRelationsBridgeDirectional：桥 2 跳检查是定向出边
// （旧 SQL EXISTS：e1.source_id = n2.id）——只有"反向边"连到 filter 的
// read 节点不应被桥（双向误桥会引入噪音关联）。

// TestRelationMemoryModes：内存 BFS（full）与逐节点 SQL（sql）两路径
// 结果一致（P0④ --memory 参数：大仓库强制 SQL 防爆内存）。

// TestBuildMetaCounts：节点/边数随构建元数据缓存（--memory auto 判断用，
// 不每次重新 COUNT）。

// TestGetTableRelationsTypeRank：同列多节点（read + write）时 Type 取
// rank 最高（query > write > read）——与遍历顺序无关（旧实现只升级 query，
// write 不覆盖 read，结果依赖 map 遍历顺序不确定）。

// TestRelationCandidatesCache：relation_candidates 缓存语义（P0③）——
// ① 有 build_id 时单表结果写缓存，图状态变化后仍返回缓存；
// ② build_id 变化 → 缓存失效 → 现场重算；
// ③ 无 build_metadata 时跳过缓存（不写行）。

// TestGetAllTableRelationsRebuildCache：--all 全量重建缓存——先算完
// 单表（缓存只有 table_a），--all 后 relation_candidates 覆盖为全部表。

// TestGetAllTableRelationsCacheHit：--all 缓存优先（Q177）——完整计算
// 一次后改图（删节点），再次 --all 直接读 relation_candidates 返回
// （不重新加载全图/BFS）；build_id 变化后缓存失效重算。
func TestGetAllTableRelationsCacheHit(t *testing.T) {
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
	// Q228：全量查询要求计算完成——先预计算（进度 done + 写缓存）
	if err := r.PrecomputeAllRelations(nil); err != nil {
		t.Fatalf("precompute: %v", err)
	}
	// 第一次 --all：命中缓存（正向 fk + 反向 read = 2 条）
	rels1, err := r.GetAllTableRelations("")
	if err != nil || len(rels1) != 2 {
		t.Fatalf("first --all = %+v, %v; want 2", rels1, err)
	}
	// 改图：删 filter 节点（级联删边）——缓存命中时应忽略图变化
	if _, err := r.Exec(`DELETE FROM nodes WHERE id = ?`,
		domain.CanonicalID(funcID+"#ext.sql.table_b.a_id.filter@9")); err != nil {
		t.Fatalf("delete: %v", err)
	}
	rels2, err := r.GetAllTableRelations("")
	if err != nil || len(rels2) != len(rels1) {
		t.Fatalf("缓存命中应返回与首次一致，got %d, %v", len(rels2), err)
	}
	// build_id 变化 → 缓存失效 → 重算（filter 已删 → 无关联）
	if err := r.Save(&domain.BuildMeta{BuildID: "b2", ToolName: "all", Status: "success"}); err != nil {
		t.Fatalf("Save b2: %v", err)
	}
	// Q228：新 build_id 无进度——需重新预计算（filter 已删 → 空结果）
	if err := r.PrecomputeAllRelations(nil); err != nil {
		t.Fatalf("precompute b2: %v", err)
	}
	rels3, err := r.GetAllTableRelations("")
	if err != nil {
		t.Fatalf("b2 --all: %v", err)
	}
	if len(rels3) != 0 {
		t.Fatalf("b2 失效应重算为空，got %+v", rels3)
	}
}
