// member DAO：接口动态派发真实形态——AccountRepo 接口 + 具体实现 +
// 编译期注册断言（MakeInterface 注册点），覆盖动态调用候选边。
package member

// Account 账户（表名 mm_account，主键 member_id）。
type Account struct {
	MemberID int64
	Balance  int64
}

// AccountRepo 账户仓储接口。
type AccountRepo interface {
	Get(id int64) (*Account, error)
	Save(a *Account) error
}

// accountRepoImpl 具体实现。
type accountRepoImpl struct{}

func (r *accountRepoImpl) Get(id int64) (*Account, error) { return &Account{}, nil }

func (r *accountRepoImpl) Save(a *Account) error { return nil }

// 编译期注册断言（MakeInterface 注册点——动态派发候选识别）。
var _ AccountRepo = (*accountRepoImpl)(nil)

// NewAccountRepo 工厂：返回接口值（MakeInterface）。
func NewAccountRepo() AccountRepo { return &accountRepoImpl{} }

// GetBalance 动态派发调用：经接口候选边进入实现。
func GetBalance(r AccountRepo, id int64) (int64, error) {
	a, err := r.Get(id)
	if err != nil {
		return 0, err
	}
	return a.Balance, nil
}
