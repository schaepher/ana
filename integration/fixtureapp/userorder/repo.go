// userorder DAO：跨表键关联形态（Q177 验收标准）——用 order.UserID
// 查 users（外键→主键：orders.user_id → users.id 键关联）。
package userorder

import "xorm.io/xorm"

// Order 订单（表名 orders）。
type Order struct {
	ID     int64
	UserID int64
	Status int
}

// User 用户（表名 users）。
type User struct {
	ID   int64
	Name string
}

// FindUserForOrder 读订单 → 用订单的 UserID 查用户（变参值链：
// 对象字段读 → users.id filter）。
func FindUserForOrder(session *xorm.Session, orderID int64) error {
	var order Order
	if _, err := session.Table("orders").
		Where("id = ?", orderID).
		Get(&order); err != nil {
		return err
	}
	var user User
	_, err := session.Table("users").
		Where("id = ?", order.UserID).
		Get(&user)
	return err
}
