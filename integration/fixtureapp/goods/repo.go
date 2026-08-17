// goods DAO：GORM 真实形态（具体类型 *gorm.DB 静态调用——链式
// Table→Where→Find/Create，覆盖 filter/read/write 节点）。
package goods

import "gorm.io/gorm"

// Goods 商品（表名 goods）。
type Goods struct {
	ID    int64
	Title string
	Price int64
}

// FindGoods 链式读：Table→Where→Find。
func FindGoods(db *gorm.DB, minPrice int64) ([]Goods, error) {
	var list []Goods
	err := db.Table("goods").Where("price > ?", minPrice).Find(&list)
	return list, err
}

// CreateGoods 链式写：Table→Create。
func CreateGoods(db *gorm.DB, g *Goods) error {
	return db.Table("goods").Create(g)
}

// UpdateGoodsPrice 链式写：Table→Where→Updates。
func UpdateGoodsPrice(db *gorm.DB, id, price int64) error {
	return db.Table("goods").Where("id = ?", id).Updates(&Goods{Price: price})
}
