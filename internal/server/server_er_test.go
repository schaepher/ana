package server

import (
	"context"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/schaepher/codeintel/internal/action"
	"github.com/schaepher/codeintel/internal/domain"
	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
)

// TestHandleER：/api/er——数据库 ER 图数据（表 + 列清单 + 表间关联）。
// 关系按置信度分三级：query 键关联（高置信）/ write 同源（中）/ read 间接（低）。
func TestHandleER(t *testing.T) {
	dir := t.TempDir()
	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	r := sqlite.NewRepo(db)

	funcID := "symbol:go:example.com/m:find"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(funcID), Kind: domain.KindFunction, Name: "find", FilePath: "a.go"},
		// table_a：id 读出 + name 写入
		{ID: domain.CanonicalID(funcID + "#ext.sql.table_a.id.read@6"), Kind: domain.KindFieldAccess,
			Name: "table_a.id", FilePath: "a.go", LineStart: 6,
			Properties: map[string]any{"full_path": "table_a.id", "instance_path": "table_a.id",
				"access_kind": "read", "type_string": "sql", "is_external": "true", "func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#ext.sql.table_a.name.write@7"), Kind: domain.KindFieldAccess,
			Name: "table_a.name", FilePath: "a.go", LineStart: 7,
			Properties: map[string]any{"full_path": "table_a.name", "instance_path": "table_a.name",
				"access_kind": "write", "type_string": "sql", "is_external": "true", "func_id": funcID}},
		// table_b：a_id 过滤（键关联终点）
		{ID: domain.CanonicalID(funcID + "#ext.sql.table_b.a_id.filter@9"), Kind: domain.KindFieldAccess,
			Name: "table_b.a_id", FilePath: "a.go", LineStart: 9,
			Properties: map[string]any{"full_path": "table_b.a_id", "instance_path": "table_b.a_id",
				"access_kind": "filter", "type_string": "sql", "is_external": "true", "func_id": funcID}},
		// 数据流中间值：table_a.id 读出 → table_b.a_id 过滤（query 键关联）
		{ID: domain.CanonicalID(funcID + "#t4"), Kind: domain.KindSSAValue, Name: "t4",
			Properties: map[string]any{"func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#x"), Kind: domain.KindSSAValue, Name: "x",
			Properties: map[string]any{"func_id": funcID}},
		// 干扰：非外部字段访问不得入 ER 图
		{ID: domain.CanonicalID(funcID + "#u.Name.write@8"), Kind: domain.KindFieldAccess,
			Name: "u.Name", FilePath: "a.go", LineStart: 8,
			Properties: map[string]any{"full_path": "example.com/m.User.Name", "access_kind": "write"}},
	}
	edges := []*domain.Fact{
		{SourceID: domain.CanonicalID(funcID + "#ext.sql.table_a.id.read@6"), TargetID: domain.CanonicalID(funcID + "#t4"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#t4"), TargetID: domain.CanonicalID(funcID + "#x"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#x"), TargetID: domain.CanonicalID(funcID + "#ext.sql.table_b.a_id.filter@9"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
	}
	if _, err := r.SaveBatchStats(nodes, edges, nil); err != nil {
		t.Fatalf("save: %v", err)
	}
	// Q228：全量查询要求计算完成——预计算（进度 done + 写缓存）
	if err := r.Save(&domain.BuildMeta{BuildID: "b1", ToolName: "all", Status: "success"}); err != nil {
		t.Fatalf("Save build: %v", err)
	}
	if err := r.PrecomputeAllRelations(nil); err != nil {
		t.Fatalf("precompute: %v", err)
	}

	web := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<html></html>")}}
	srv := New(context.Background(), action.New(r), web, dir)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	resp, m := get(t, ts, "/api/er")
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, body=%v", resp.StatusCode, m)
	}
	// 表清单：table_a / table_b（非外部字段不产生表）
	tables, _ := m["tables"].([]any)
	if len(tables) != 2 {
		t.Fatalf("tables = %v", tables)
	}
	ta := tables[0].(map[string]any)
	if ta["name"] != "table_a" {
		t.Errorf("tables[0].name = %v", ta["name"])
	}
	taCols, _ := ta["columns"].([]any)
	if len(taCols) != 2 {
		t.Fatalf("table_a columns = %v", taCols)
	}
	c0 := taCols[0].(map[string]any)
	if c0["name"] != "table_a.id" || c0["access"] != "read" {
		t.Errorf("column[0] = %v", c0)
	}
	tb := tables[1].(map[string]any)
	tbCols, _ := tb["columns"].([]any)
	if len(tbCols) != 1 {
		t.Fatalf("table_b columns = %v", tbCols)
	}
	// 表间关联：正向 query（键关联，高置信）必须存在；反向 read
	// （table_b.a_id → table_a.id，低置信）是 GetAllTableRelations 的
	// 既有语义（各表分别 BFS），前端按置信度分级样式呈现
	rels, _ := m["relations"].([]any)
	var found *map[string]any
	for _, r := range rels {
		rm := r.(map[string]any)
		if rm["from_table"] == "table_a" && rm["from_col"] == "id" &&
			rm["to_table"] == "table_b" && rm["to_col"] == "a_id" {
			found = &rm
			break
		}
	}
	if found == nil {
		t.Fatalf("relations 缺正向 query（table_a.id → table_b.a_id），got %v", rels)
	}
	if (*found)["type"] != "fk" {
		t.Errorf("正向关系 type = %v, want fk", (*found)["type"])
	}
}

// TestHandleERHopsParam：/api/er 跳数上限参数（Q197，网页版可配置）——
// 默认滤 >4 跳 query；?q_hops=0 不限制（长链可见）。
func TestHandleERHopsParam(t *testing.T) {
	dir := t.TempDir()
	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	r := sqlite.NewRepo(db)

	funcID := "symbol:go:example.com/m:find"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(funcID), Kind: domain.KindFunction, Name: "find", FilePath: "a.go"},
		// 5 跳 query 链：a.id read → t1 → t2 → t3 → t4 → b.a_id filter
		{ID: domain.CanonicalID(funcID + "#ext.sql.table_a.id.read@6"), Kind: domain.KindFieldAccess,
			Name: "table_a.id", FilePath: "a.go", LineStart: 6,
			Properties: map[string]any{"full_path": "table_a.id", "instance_path": "table_a.id",
				"access_kind": "read", "type_string": "sql", "is_external": "true", "func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#ext.sql.table_b.a_id.filter@9"), Kind: domain.KindFieldAccess,
			Name: "table_b.a_id", FilePath: "a.go", LineStart: 9,
			Properties: map[string]any{"full_path": "table_b.a_id", "instance_path": "table_b.a_id",
				"access_kind": "filter", "type_string": "sql", "is_external": "true", "func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#t1"), Kind: domain.KindSSAValue, Name: "t1",
			Properties: map[string]any{"func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#t2"), Kind: domain.KindSSAValue, Name: "t2",
			Properties: map[string]any{"func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#t3"), Kind: domain.KindSSAValue, Name: "t3",
			Properties: map[string]any{"func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#t4"), Kind: domain.KindSSAValue, Name: "t4",
			Properties: map[string]any{"func_id": funcID}},
	}
	edges := []*domain.Fact{
		{SourceID: domain.CanonicalID(funcID + "#ext.sql.table_a.id.read@6"), TargetID: domain.CanonicalID(funcID + "#t1"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#t1"), TargetID: domain.CanonicalID(funcID + "#t2"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#t2"), TargetID: domain.CanonicalID(funcID + "#t3"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#t3"), TargetID: domain.CanonicalID(funcID + "#t4"),
			Kind: domain.FactDataFlowsTo, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#t4"), TargetID: domain.CanonicalID(funcID + "#ext.sql.table_b.a_id.filter@9"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
	}
	if _, err := r.SaveBatchStats(nodes, edges, nil); err != nil {
		t.Fatalf("save: %v", err)
	}
	// Q228：全量查询要求计算完成——预计算（进度 done + 写缓存）
	if err := r.Save(&domain.BuildMeta{BuildID: "b1", ToolName: "all", Status: "success"}); err != nil {
		t.Fatalf("Save build: %v", err)
	}
	if err := r.PrecomputeAllRelations(nil); err != nil {
		t.Fatalf("precompute: %v", err)
	}

	web := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<html></html>")}}
	srv := New(context.Background(), action.New(r), web, dir)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	relsOf := func(path string) int {
		_, m := get(t, ts, path)
		rels, _ := m["relations"].([]any)
		return len(rels)
	}
	// 默认：5 跳链值流验证通过 → fk（Q218：fk 默认不限跳，直接保留）
	if n := relsOf("/api/er"); n != 1 {
		t.Errorf("默认应保留 5 跳 fk（fk 不限跳），got %d 条", n)
	}
	// q_hops=0（不限制）：5 跳 fk 可见
	if n := relsOf("/api/er?q_hops=0"); n != 1 {
		t.Errorf("q_hops=0 应保留 5 跳 fk，got %d 条", n)
	}
	// q_hops=6：5 跳 ≤ 6 保留
	if n := relsOf("/api/er?q_hops=6"); n != 1 {
		t.Errorf("q_hops=6 应保留 5 跳 fk，got %d 条", n)
	}
	// 非法参数（负数/非数字）回退默认
	if n := relsOf("/api/er?q_hops=-1"); n != 1 {
		t.Errorf("负数参数应回退默认（fk 保留），got %d 条", n)
	}
	if n := relsOf("/api/er?q_hops=abc"); n != 1 {
		t.Errorf("非法参数应回退默认（fk 保留），got %d 条", n)
	}
}

// TestHandleERSkipRelations：Q209 首次加载不查关联——?skip_relations=1
// 只返回表清单（relations 空），避免首次加载触发全库 BFS（无缓存时
// 秒级）；展开/全图画线时才请求完整数据。
func TestHandleERSkipRelations(t *testing.T) {
	dir := t.TempDir()
	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	r := sqlite.NewRepo(db)

	funcID := "symbol:go:example.com/m:find"
	nodes := []*domain.CodeEntity{
		{ID: domain.CanonicalID(funcID), Kind: domain.KindFunction, Name: "find", FilePath: "a.go"},
		{ID: domain.CanonicalID(funcID + "#ext.sql.table_a.id.read@6"), Kind: domain.KindFieldAccess,
			Name: "table_a.id", FilePath: "a.go", LineStart: 6,
			Properties: map[string]any{"full_path": "table_a.id", "instance_path": "table_a.id",
				"access_kind": "read", "type_string": "sql", "is_external": "true", "func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#ext.sql.table_b.a_id.filter@9"), Kind: domain.KindFieldAccess,
			Name: "table_b.a_id", FilePath: "a.go", LineStart: 9,
			Properties: map[string]any{"full_path": "table_b.a_id", "instance_path": "table_b.a_id",
				"access_kind": "filter", "type_string": "sql", "is_external": "true", "func_id": funcID}},
		{ID: domain.CanonicalID(funcID + "#t1"), Kind: domain.KindSSAValue, Name: "t1",
			Properties: map[string]any{"func_id": funcID}},
	}
	edges := []*domain.Fact{
		{SourceID: domain.CanonicalID(funcID + "#ext.sql.table_a.id.read@6"), TargetID: domain.CanonicalID(funcID + "#t1"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
		{SourceID: domain.CanonicalID(funcID + "#t1"), TargetID: domain.CanonicalID(funcID + "#ext.sql.table_b.a_id.filter@9"),
			Kind: domain.FactSummaryIO, ToolSource: domain.ToolSSA, Confidence: 1},
	}
	if _, err := r.SaveBatchStats(nodes, edges, nil); err != nil {
		t.Fatalf("save: %v", err)
	}
	// Q228：全量查询要求计算完成——预计算（进度 done + 写缓存）
	if err := r.Save(&domain.BuildMeta{BuildID: "b1", ToolName: "all", Status: "success"}); err != nil {
		t.Fatalf("Save build: %v", err)
	}
	if err := r.PrecomputeAllRelations(nil); err != nil {
		t.Fatalf("precompute: %v", err)
	}

	web := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<html></html>")}}
	srv := New(context.Background(), action.New(r), web, dir)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	// skip_relations=1：表齐全、relations 空
	_, m := get(t, ts, "/api/er?skip_relations=1")
	tables, _ := m["tables"].([]any)
	if len(tables) != 2 {
		t.Fatalf("skip_relations 表数 = %d, want 2", len(tables))
	}
	rels, _ := m["relations"].([]any)
	if len(rels) != 0 {
		t.Errorf("skip_relations=1 应返回空 relations，got %d", len(rels))
	}
	// 不带参数：正常返回关系
	_, m2 := get(t, ts, "/api/er")
	rels2, _ := m2["relations"].([]any)
	if len(rels2) == 0 {
		t.Errorf("正常请求应返回 relations")
	}
}
