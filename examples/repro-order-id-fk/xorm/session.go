// Package xorm is a minimal static stand-in for the real xorm API.
package xorm

type Session struct{}

func (s *Session) Table(nameOrBean interface{}) *Session { return s }
func (s *Session) Where(query interface{}, args ...interface{}) *Session {
        return s
}
func (s *Session) Find(beans interface{}, condiBeans ...interface{}) error { return nil }