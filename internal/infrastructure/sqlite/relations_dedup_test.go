package sqlite

import (
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

// TestDedupRelationNoise：Q195/Q196 关系降噪——
// ① write/read 按 from字段→to表 聚合（全列 INSERT 的列爆炸收敛为字段级）
// ② 跳数上限：全部类型 > MaxRelationHops 丢弃（含 query 长链）；
//    includeLongQuery=true 时 query 长链保留（--include-long-query 查看）
func TestDedupRelationNoise(t *testing.T) {
	rels := []*domain.TableRelation{
		// query：10 跳长链默认被滤，includeLongQuery=true 保留
		{FromTable: "a", FromCol: "id", ToTable: "b", ToCol: "a_id", Hops: 10, Type: domain.RelationQuery},
		// query：4 跳内默认保留
		{FromTable: "a", FromCol: "uid", ToTable: "b", ToCol: "b_uid", Hops: 3, Type: domain.RelationQuery},
		// write：同 from 字段 → 同 to 表多列（hops 2 与 6）→ 聚合为 1 条（hops 最小）
		{FromTable: "a", FromCol: "name", ToTable: "b", ToCol: "p1", Hops: 2, Type: domain.RelationWrite},
		{FromTable: "a", FromCol: "name", ToTable: "b", ToCol: "p2", Hops: 6, Type: domain.RelationWrite},
		// write：5 跳 > 4 → 丢弃
		{FromTable: "a", FromCol: "name", ToTable: "c", ToCol: "q1", Hops: 5, Type: domain.RelationWrite},
		// read：10 跳 → 丢弃
		{FromTable: "a", FromCol: "id", ToTable: "d", ToCol: "r", Hops: 10, Type: domain.RelationRead},
		// 不同 from 字段 → 同 to 表：不聚合（字段级精度保留）
		{FromTable: "a", FromCol: "age", ToTable: "b", ToCol: "p3", Hops: 3, Type: domain.RelationWrite},
	}
	// 默认：query 长链也滤
	out := dedupRelationNoise(rels, false)
	if len(out) != 3 {
		t.Fatalf("out = %d, want 3（4 跳内 query + a.name→b 聚合 + a.age→b）: %+v", len(out), out)
	}
	got := map[string]*domain.TableRelation{}
	for _, r := range out {
		got[r.FromCol+"|"+r.ToCol] = r
	}
	if _, ok := got["id|a_id"]; ok {
		t.Errorf("10 跳 query 默认应被跳数上限过滤")
	}
	if r := got["uid|b_uid"]; r == nil || r.Type != domain.RelationQuery || r.Hops != 3 {
		t.Errorf("4 跳内 query 应保留，got %+v", r)
	}
	if r := got["name|p1"]; r == nil {
		t.Errorf("a.name→b 应聚合保留 p1（hops 最小），got %+v", got)
	}
	if _, ok := got["name|p2"]; ok {
		t.Errorf("a.name→b.p2 应与 p1 聚合（同字段同表只留一条）")
	}
	if _, ok := got["name|q1"]; ok {
		t.Errorf("5 跳 write 应被跳数上限过滤")
	}
	if _, ok := got["id|r"]; ok {
		t.Errorf("10 跳 read 应被跳数上限过滤")
	}
	if r := got["age|p3"]; r == nil {
		t.Errorf("不同 from 字段不聚合，a.age→b.p3 应保留")
	}
	// --include-long-query：query 长链保留
	out2 := dedupRelationNoise(rels, true)
	got2 := map[string]*domain.TableRelation{}
	for _, r := range out2 {
		got2[r.FromCol+"|"+r.ToCol] = r
	}
	if r := got2["id|a_id"]; r == nil || r.Type != domain.RelationQuery || r.Hops != 10 {
		t.Errorf("includeLongQuery 应保留 query 长链，got %+v", r)
	}
}

// TestDedupRelationNoiseOrder：聚合后保持输入顺序（第一条保留位次，
// hops 更小的替换值但不动顺序——输出稳定排序由调用方负责）。
func TestDedupRelationNoiseOrder(t *testing.T) {
	rels := []*domain.TableRelation{
		{FromTable: "a", FromCol: "x", ToTable: "b", ToCol: "p1", Hops: 4, Type: domain.RelationWrite},
		{FromTable: "a", FromCol: "x", ToTable: "b", ToCol: "p2", Hops: 2, Type: domain.RelationWrite},
	}
	out := dedupRelationNoise(rels, false)
	if len(out) != 1 || out[0].ToCol != "p2" || out[0].Hops != 2 {
		t.Errorf("同 key 后到者 hops 更小应替换值，got %+v", out)
	}
}
