package ssa

import (
	"strings"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

// TestORMWriteSameSourceBizID 业务 id 同源双写场景：
// 业务 id 先创建 → 与数据一起 insert 表 A（a.BizID = bizID）→
// 再更新到表 B（b.BizID = bizID）。期望识别 a_tab.biz_id →
// b_tab.biz_id 同源 write 关联（同一值流写入两表）。
func TestORMWriteSameSourceBizID(t *testing.T) {
	src := `package mtest

import "xorm.io/xorm"

type ATab struct {
	ID    uint64
	BizID uint64
}

type BTab struct {
	ID    uint64
	BizID uint64
}

// 业务 id 先创建 → 与数据一起 insert 表 A → 再更新到表 B
func syncBiz(s *xorm.Session, bizID uint64) error {
	a := &ATab{ID: 1}
	a.BizID = bizID
	if _, err := s.Table("a_tab").Insert(a); err != nil {
		return err
	}
	b := &BTab{ID: 2}
	b.BizID = bizID
	if _, err := s.Table("b_tab").Update(b); err != nil {
		return err
	}
	return nil
}
`
	repo := indexFixtureRepo(t, map[string]string{
		"go.mod": `module example.com/mtest

go 1.21

require xorm.io/xorm v0.0.0

replace xorm.io/xorm => ./xorm
`,
		"xorm/go.mod": "module xorm.io/xorm\n\ngo 1.21\n",
		"xorm/session.go": `package xorm

type Session struct{}

func (s *Session) Table(name string) *Session { return s }
func (s *Session) Update(bean any) (int64, error) { return 0, nil }
func (s *Session) Insert(bean any) (int64, error) { return 0, nil }
`,
		"main.go": src,
	})
	rels, err := repo.GetTableRelations("a_tab", "full")
	if err != nil {
		t.Fatal(err)
	}
	// 期望：a_tab.biz_id → b_tab.biz_id（write 同源——同一 bizID 值流
	// 写入两表同名列）
	got := []string{}
	for _, r := range rels {
		got = append(got, r.FromTable+"."+r.FromCol+" → "+r.ToTable+"."+r.ToCol+" ["+r.Type+"]")
		if r.FromTable == "a_tab" && r.FromCol == "biz_id" &&
			r.ToTable == "b_tab" && r.ToCol == "biz_id" {
			t.Logf("命中：%s → %s.%s [%s] hops=%d", r.FromTable+"."+r.FromCol, r.ToTable, r.ToCol, r.Type, r.Hops)
			return
		}
	}
	t.Fatalf("a_tab.biz_id → b_tab.biz_id 未识别（同源双写），现有：%v", got)
}

// TestORMWriteSameSourceBizIDChain 同场景链路检查：bizID 值节点与
// a_tab.biz_id / b_tab.biz_id 两个虚拟列节点均应连通（值流链闭环）。
func TestORMWriteSameSourceBizIDChain(t *testing.T) {
	src := `package mtest

import "xorm.io/xorm"

type ATab struct {
	ID    uint64
	BizID uint64
}

type BTab struct {
	ID    uint64
	BizID uint64
}

func syncBiz(s *xorm.Session, bizID uint64) error {
	a := &ATab{ID: 1}
	a.BizID = bizID
	if _, err := s.Table("a_tab").Insert(a); err != nil {
		return err
	}
	b := &BTab{ID: 2}
	b.BizID = bizID
	if _, err := s.Table("b_tab").Update(b); err != nil {
		return err
	}
	return nil
}
`
	nodes, facts, _ := indexFixtureFull(t, map[string]string{
		"go.mod": `module example.com/mtest

go 1.21

require xorm.io/xorm v0.0.0

replace xorm.io/xorm => ./xorm
`,
		"xorm/go.mod": "module xorm.io/xorm\n\ngo 1.21\n",
		"xorm/session.go": `package xorm

type Session struct{}

func (s *Session) Table(name string) *Session { return s }
func (s *Session) Update(bean any) (int64, error) { return 0, nil }
func (s *Session) Insert(bean any) (int64, error) { return 0, nil }
`,
		"main.go": src,
	})
	// 两个虚拟列节点存在（Insert 表 A / Update 表 B 字段展开）
	aCol, bCol := false, false
	for _, n := range nodes {
		if strings.Contains(string(n.ID), "a_tab.biz_id") {
			aCol = true
		}
		if strings.Contains(string(n.ID), "b_tab.biz_id") {
			bCol = true
		}
	}
	if !aCol || !bCol {
		t.Fatalf("a_tab.biz_id=%v b_tab.biz_id=%v 虚拟列节点应存在", aCol, bCol)
	}
	// bizID 参数节点存在（签名发射）
	paramNode := false
	for _, n := range nodes {
		if strings.HasSuffix(string(n.ID), "#param.bizID") {
			paramNode = true
		}
	}
	if !paramNode {
		t.Fatalf("bizID 参数节点应存在")
	}
	// 值流边：bizID → a.BizID.write 与 bizID → b.BizID.write
	paramID := ""
	for _, n := range nodes {
		if strings.HasSuffix(string(n.ID), "#param.bizID") {
			paramID = string(n.ID)
		}
	}
	writes := 0
	for _, f := range facts {
		if string(f.SourceID) == paramID && f.Kind == domain.FactDataFlowsTo {
			writes++
		}
	}
	if writes < 2 {
		t.Fatalf("bizID → 两表字段写边应 ≥2，实际 %d", writes)
	}
}

// TestORMWriteSameSourceCrossFunc 同场景跨函数变体：insert 表 A 与
// update 表 B 拆到独立函数（argument 边跨函数传递 bizID）。Q202
// 「对象→字段写节点清空 taint」规则对 ext.gorm 虚拟列节点（is_external）
// 不应生效——虚拟列的值来源就是对象字段映射，对象整体携带字段值。
func TestORMWriteSameSourceCrossFunc(t *testing.T) {
	src := `package mtest

import "xorm.io/xorm"

type ATab struct {
	ID    uint64
	BizID uint64
}

type BTab struct {
	ID    uint64
	BizID uint64
}

func insertA(s *xorm.Session, a *ATab) error {
	if _, err := s.Table("a_tab").Insert(a); err != nil {
		return err
	}
	return nil
}

func updateB(s *xorm.Session, b *BTab) error {
	if _, err := s.Table("b_tab").Update(b); err != nil {
		return err
	}
	return nil
}

func syncBiz(s *xorm.Session, bizID uint64) error {
	a := &ATab{ID: 1}
	a.BizID = bizID
	if err := insertA(s, a); err != nil {
		return err
	}
	b := &BTab{ID: 2}
	b.BizID = bizID
	if err := updateB(s, b); err != nil {
		return err
	}
	return nil
}
`
	repo := indexFixtureRepo(t, map[string]string{
		"go.mod": `module example.com/mtest

go 1.21

require xorm.io/xorm v0.0.0

replace xorm.io/xorm => ./xorm
`,
		"xorm/go.mod": "module xorm.io/xorm\n\ngo 1.21\n",
		"xorm/session.go": `package xorm

type Session struct{}

func (s *Session) Table(name string) *Session { return s }
func (s *Session) Update(bean any) (int64, error) { return 0, nil }
func (s *Session) Insert(bean any) (int64, error) { return 0, nil }
`,
		"main.go": src,
	})
	rels, err := repo.GetTableRelations("a_tab", "full")
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, r := range rels {
		got = append(got, r.FromTable+"."+r.FromCol+" → "+r.ToTable+"."+r.ToCol+" ["+r.Type+"]")
		if r.FromTable == "a_tab" && r.FromCol == "biz_id" &&
			r.ToTable == "b_tab" && r.ToCol == "biz_id" {
			t.Logf("命中：%s → %s.%s [%s] hops=%d", r.FromTable+"."+r.FromCol, r.ToTable, r.ToCol, r.Type, r.Hops)
			// Q225：跨函数 write 目标列须外键形态或同名列强呼应——
			// b_tab.id（对象展开噪声）应被 Q202c 丢弃（id 非外键形态、
			// taint={biz_id} 非 exact）
			for _, r2 := range rels {
				if r2.FromTable == "a_tab" && r2.FromCol == "biz_id" &&
					r2.ToTable == "b_tab" && r2.ToCol == "id" {
					t.Fatalf("跨函数 b_tab.id 应被 Q202c 丢弃（对象展开噪声）：%v", got)
				}
			}
			return
		}
	}
	t.Fatalf("跨函数同源双写 a_tab.biz_id → b_tab.biz_id 未识别，现有：%v", got)
}
