// gorm 本地模拟（真实包路径 gorm.io/gorm）：具体类型 *gorm.DB 静态调用
// 形态（真实仓库形态——GORM 链式 Table→Where→Find/Create）。
package gorm

// DB 链式数据库句柄。
type DB struct{}

func (d *DB) Table(name string) *DB { return d }

func (d *DB) Where(cond string, args ...any) *DB { return d }

func (d *DB) Not(cond string, args ...any) *DB { return d }

func (d *DB) Find(out any) error { return nil }

func (d *DB) Scan(out any) error { return nil }

func (d *DB) Create(v any) error { return nil }

func (d *DB) Updates(v any) error { return nil }

func (d *DB) Delete(v any) error { return nil }

func (d *DB) Exec(sql string, args ...any) error { return nil }

// Tx 事务。
type Tx struct{}

func (d *DB) Begin() *Tx { return &Tx{} }

func (t *Tx) Commit() error { return nil }

func (t *Tx) Rollback() error { return nil }
