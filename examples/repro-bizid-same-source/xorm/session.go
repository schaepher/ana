package xorm

type Session struct{}

func (s *Session) Table(name string) *Session { return s }

func (s *Session) Where(cond string, args ...any) *Session { return s }

func (s *Session) Insert(bean any) (int64, error) { return 0, nil }

func (s *Session) Update(bean any) (int64, error) { return 0, nil }
