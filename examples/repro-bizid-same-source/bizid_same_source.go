// Package bizidsame demonstrates same-source dual-write of a business id:
// a business id is created first, then inserted together with its row into
// table a_tab, and later the same value is updated into table b_tab.
//
// codeintel should infer a_tab.biz_id -> b_tab.biz_id as a same-source
// write relation (the same value flow writes two tables' columns).
package bizidsame

import "xorm.io/xorm"

// ATab / BTab share the same business-id column name (ORM snake_case:
// BizID -> biz_id).
type ATab struct {
	ID    uint64
	BizID uint64
}

type BTab struct {
	ID    uint64
	BizID uint64
}

// SyncBizSameFunc: same-function variant — the whole flow lives in one
// function (recognized since the same-function write path, no taint check).
func SyncBizSameFunc(s *xorm.Session, bizID uint64) error {
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

// InsertATab / UpdateBTab: cross-function variant — the insert and the
// update live in separate functions; the bizID value crosses the call
// boundary through the object arguments. Requires the Q225 taint fix
// (object -> field-write carries the field-name-matched taint, and the
// exact-name dual write passes the cross-function write gate).
func InsertATab(s *xorm.Session, a *ATab) error {
	if _, err := s.Table("a_tab").Insert(a); err != nil {
		return err
	}
	return nil
}

func UpdateBTab(s *xorm.Session, b *BTab) error {
	if _, err := s.Table("b_tab").Update(b); err != nil {
		return err
	}
	return nil
}

// SyncBizCrossFunc wires the two halves together.
func SyncBizCrossFunc(s *xorm.Session, bizID uint64) error {
	a := &ATab{ID: 1}
	a.BizID = bizID
	if err := InsertATab(s, a); err != nil {
		return err
	}
	b := &BTab{ID: 2}
	b.BizID = bizID
	if err := UpdateBTab(s, b); err != nil {
		return err
	}
	return nil
}
