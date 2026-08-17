// settlement DAO：XORM 链式真实形态（具体类型 *xorm.Session 静态调用——
// Table 记表名 → 条件 → 读/写，覆盖 filter/read/write 三类节点）。
package settlement

import "xorm.io/xorm"

// Settlement 结算单（表名 settlement，字段 snake_case 列名）。
type Settlement struct {
	OrderID int64
	Amount  int64
	Status  int
}

// FindSettlements 链式读：Table→Where→And→Find。
func FindSettlements(s *xorm.Session, minAmount int64) ([]Settlement, error) {
	var list []Settlement
	err := s.Table("settlement").Where("status = ?", 1).And("amount > ?", minAmount).Find(&list)
	return list, err
}

// FindByOrder 链式读：Table→In→Get（多条件 + 主键形态）。
func FindByOrder(s *xorm.Session, orderID int64) (*Settlement, error) {
	var v Settlement
	_, err := s.Table("settlement").In("order_id", orderID).Get(&v)
	return &v, err
}

// UpdateStatus 链式写：Table→Where→Update。
func UpdateStatus(s *xorm.Session, orderID int64, status int) error {
	_, err := s.Table("settlement").Where("order_id = ?", orderID).Update(&Settlement{Status: status})
	return err
}

// InsertSettlement 链式写：Table→Insert。
func InsertSettlement(s *xorm.Session, v *Settlement) error {
	_, err := s.Table("settlement").Insert(v)
	return err
}

// DeleteSettlement 链式写：Table→Or→Delete（多条件）。
func DeleteSettlement(s *xorm.Session, orderID int64) error {
	_, err := s.Table("settlement").Or("order_id = ?", orderID).Delete(&Settlement{})
	return err
}
