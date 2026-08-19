// Package repro demonstrates false FK inference from an ORM row read.
//
// Order.ID is a shard-local physical primary key. BusinessID is
// the stable business identifier. The account-book and task queries below use
// other fields, never Order.ID.
package repro

import "xorm.io/xorm"

type Order struct {
        ID                 uint64
        BusinessID    uint64
        PartyID         uint64
        HostID     uint64
        TargetID uint64
}

type Task struct{ ID uint64 }

// FindRelatedData intentionally reads a complete source row and then uses
// only non-ID fields in independent downstream WHERE predicates.
func FindRelatedData(session *xorm.Session, lastPhysicalID uint64) error {
        var orders []Order
        if err := session.Table("order_tab").
                Where("id > ?", lastPhysicalID).
                Find(&orders); err != nil {
                return err
        }

        for _, order := range orders {
                var accountBooks []struct{}
                if err := session.Table("book_tab").
                        Where("party_id = ?", order.PartyID).
                        Find(&accountBooks); err != nil {
                        return err
                }

                if err := session.Table("book_tab").
                        Where("top_party_id = ?", order.HostID).
                        Find(&accountBooks); err != nil {
                        return err
                }

                if err := session.Table("book_tab").
                        Where("target_id = ?", order.TargetID).
                        Find(&accountBooks); err != nil {
                        return err
                }

                // Task ID is a separate identity. Its value is intentionally sourced
                // from the business ID, not from the physical source table ID.
                task := Task{ID: order.BusinessID}
                var taskFiles []struct{}
                if err := session.Table("file_tab").
                        Where("file_id = ?", task.ID).
                        Find(&taskFiles); err != nil {
                        return err
                }

                var taskMappings []struct{}
                if err := session.Table("mapping_tab").
                        Where("file_id = ?", task.ID).
                        Find(&taskMappings); err != nil {
                        return err
                }
        }
        return nil
}
