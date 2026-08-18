package sqlite

import (
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

// TestFilterFKNoiseKeepsQueryID：Q205 filterFKNoise 修复——同目标列
// 多起点时 FromCol="id" 的起点此前被一律滤掉（Q159 主键起点=对象值
// 桥接噪音的假设），但 attr.id → attr_item.attr_id 这类真实键关联
// （query/filter 终点，attr.id 读出 → GetItems 查 attr_item.attr_id）
// 因此漏报。修复：query 类型不参与 id 起点过滤（键关联列级独立
// 有意义）；read/write 保持原语义（id 起点=桥接噪音）。
func TestFilterFKNoiseKeepsQueryID(t *testing.T) {
	all := []*domain.TableRelation{
		// 同目标 attr_item.attr_id 的三个起点
		{FromTable: "attr", FromCol: "id", ToTable: "attr_item", ToCol: "attr_id", Hops: 8, Type: domain.RelationQuery},
		{FromTable: "attr", FromCol: "is_filter", ToTable: "attr_item", ToCol: "attr_id", Hops: 6, Type: domain.RelationRead},
		{FromTable: "attr", FromCol: "name", ToTable: "attr_item", ToCol: "attr_id", Hops: 6, Type: domain.RelationRead},
		// id→id 依旧滤（两表互查自增主键无意义）
		{FromTable: "a", FromCol: "id", ToTable: "b", ToCol: "id", Hops: 2, Type: domain.RelationQuery},
	}
	out := filterFKNoise(all)
	got := map[string]bool{}
	for _, r := range out {
		got[r.FromCol+"->"+r.ToCol] = true
	}
	if !got["id->attr_id"] {
		t.Errorf("query 类型 id 起点被误滤（attr.id → attr_item.attr_id 漏报根因）：%v", got)
	}
	if got["id->id"] {
		t.Errorf("id→id 应保留过滤语义")
	}
	// read 的 id 起点仍滤（原语义不回退）
	all2 := []*domain.TableRelation{
		{FromTable: "a", FromCol: "id", ToTable: "b", ToCol: "x_id", Hops: 4, Type: domain.RelationRead},
		{FromTable: "a", FromCol: "name", ToTable: "b", ToCol: "x_id", Hops: 4, Type: domain.RelationRead},
	}
	out2 := filterFKNoise(all2)
	for _, r := range out2 {
		if r.FromCol == "id" {
			t.Errorf("read 类型 id 起点不应保留（原过滤语义）：%s.%s", r.FromTable, r.FromCol)
		}
	}
}
