// xorm 本地模拟（真实包路径 xorm.io/xorm）：供 fixtureapp 以具体类型
// *xorm.Session 静态调用形态使用（真实仓库形态——与接口模拟相对）。
package xorm

// Session 链式会话：Table 记表名，条件/读/写方法沿链返回自身。
type Session struct{}

func (s *Session) Table(name string) *Session { return s }

func (s *Session) Where(cond string, args ...any) *Session { return s }

func (s *Session) And(cond string, args ...any) *Session { return s }

func (s *Session) Or(cond string, args ...any) *Session { return s }

func (s *Session) In(cond string, args ...any) *Session { return s }

func (s *Session) NotIn(cond string, args ...any) *Session { return s }

func (s *Session) Find(out any) error { return nil }

func (s *Session) Get(bean any) (bool, error) { return false, nil }

func (s *Session) Iterate(out any) error { return nil }

func (s *Session) Update(bean any) (int64, error) { return 0, nil }

func (s *Session) Insert(bean any) (int64, error) { return 0, nil }

func (s *Session) Delete(bean any) (int64, error) { return 0, nil }

func (s *Session) Exec(sql string, args ...any) (int64, error) { return 0, nil }

// Engine 引擎：Table 返回链式 Session。
type Engine struct{}

func (e *Engine) Table(name string) *Session { return &Session{} }

func (e *Engine) Exec(sql string, args ...any) (int64, error) { return 0, nil }
