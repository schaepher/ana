package ssa

import (
	"go/types"
	"strings"

	"golang.org/x/tools/go/ssa"
)

// extractWhereCols 从 SQL 语句剩余部分提取 WHERE 子句的过滤列
// （`列 = ?` 序列，值实参按 ? 顺序映射——表关联分析的数据基础）。
// 支持 a.y = ? 表前缀（去前缀）；WHERE 缺失返回 nil。

// derefSlice 解切片（*[]Session → Session；GORM 读对象形态）。
func derefSlice(t types.Type) types.Type {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	if sl, ok := t.(*types.Slice); ok {
		t = sl.Elem()
	}
	return t
}

// derefType 解指针。
func derefType(t types.Type) types.Type {
	if p, ok := t.(*types.Pointer); ok {
		return p.Elem()
	}
	return t
}

// commonInitialisms 常见缩写表（golang/lint 同款，GORM 默认命名用）——
// 先 Title 化再转小写，保证 SessionID → session_id、SourceURL → source_url。
var commonInitialisms = []string{"API", "ASCII", "CPU", "CSS", "DNS", "EOF", "GUID", "HTML", "HTTP", "HTTPS", "ID", "IP", "JSON", "LHS", "QPS", "RAM", "RHS", "RPC", "SLA", "SMTP", "SSH", "TLS", "TTL", "UID", "UI", "UUID", "URI", "URL", "UTF8", "VM", "XML", "XSRF", "XSS"}

// snakeCase 类型/字段名 → 表/列名，与 GORM 默认命名完全一致（移植
// gorm NamingStrategy.toDBName：常见缩写 Title 化 + 大小写扫描——连续
// 大写不拆，转小写前插线）。UserProfile → user_profile、SessionID →
// session_id、SourceURL → source_url、APIKey → apikey、
// SQLiteKnowledgeGraph → sq_lite_knowledge_graph（SQL 不在缩写表，
// 与 GORM 默认一致；验证仓库 用 TableName() 定制表名时无法静态推导）。
func snakeCase(s string) string {
	value := s
	for _, in := range commonInitialisms {
		value = strings.ReplaceAll(value, in, in[:1]+strings.ToLower(in[1:]))
	}
	if value == "" {
		return ""
	}
	var sb strings.Builder
	lastCase := false
	curCase := value[0] >= 'A' && value[0] <= 'Z'
	for i := 0; i < len(value)-1; i++ {
		v := value[i]
		nextCase := value[i+1] >= 'A' && value[i+1] <= 'Z'
		nextNumber := value[i+1] >= '0' && value[i+1] <= '9'
		if curCase {
			if lastCase && (nextCase || nextNumber) {

				sb.WriteByte(v + ('a' - 'A'))
			} else {
				if i > 0 && value[i-1] != '_' && value[i+1] != '_' {
					sb.WriteByte('_')
				}
				sb.WriteByte(v + ('a' - 'A'))
			}
		} else {
			sb.WriteByte(v)
		}
		lastCase = curCase
		curCase = nextCase
	}
	if curCase {
		if !lastCase && len(value) > 1 {
			sb.WriteByte('_')
		}
		sb.WriteByte(value[len(value)-1] + ('a' - 'A'))
	} else {
		sb.WriteByte(value[len(value)-1])
	}
	return sb.String()
}

// whereColsOf 从 where 条件串提取列名：AND/OR 拆分 + 占位符剥离
// （IN (?) 先处理；其余形态截到最后一个 ? 再 TrimRight 运算符——
// 兼容 " = ?" / "=?" / " <?" / " LIKE ?" 等有无空格写法，以及多行
// 条件串（AND/OR 前后为换行/制表符——pay_order 实测整串未被拆分）。

func chainScopeObject(recv ssa.Value) *types.Named {
	c, ok := recv.(*ssa.Call)
	if !ok {
		return nil
	}
	for i := 1; i < len(c.Call.Args); i++ {
		arg := c.Call.Args[i]
		if mi, isMI := arg.(*ssa.MakeInterface); isMI {
			arg = mi.X
		}
		if named := namedStructOf(derefType(arg.Type())); named != nil {
			return named
		}
	}
	if len(c.Call.Args) > 0 {
		return chainScopeObject(c.Call.Args[0])
	}
	return nil
}

func fieldValueOf(obj ssa.Value, idx int) ssa.Value {
	refs := obj.Referrers()
	if refs == nil {
		return nil
	}
	for _, ref := range *refs {
		switch r := ref.(type) {
		case *ssa.FieldAddr:
			if r.Field == idx {

				if r.Referrers() != nil {
					for _, ref2 := range *r.Referrers() {
						if st, ok := ref2.(*ssa.Store); ok && st.Addr == r {
							return st.Val
						}
					}
				}
				return nil
			}
		case *ssa.Field:
			if r.Field == idx {
				return r
			}
		}
	}
	return nil
}

func entityTypeOf(cc *ssa.CallCommon, spec summarySpec) types.Type {
	t := cc.Value.Type()
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	if named, ok := t.(*types.Named); ok && named.TypeArgs().Len() > 0 {
		return named.TypeArgs().At(0)
	}

	switch spec.Kind {
	case "write":
		if spec.ObjArg >= 0 && spec.ObjArg < len(cc.Args) {
			// 解 MakeInterface：参数类型为 any/interface{}（xorm 的
			// Update(bean any) 等）时对象字面量被包装——与 read 一致
			arg := cc.Args[spec.ObjArg]
			if mi, ok := arg.(*ssa.MakeInterface); ok {
				arg = mi.X
			}
			return derefType(arg.Type())
		}
	case "read":

		if spec.ObjArg >= 0 && spec.ObjArg < len(cc.Args) {
			arg := cc.Args[spec.ObjArg]
			if mi, ok := arg.(*ssa.MakeInterface); ok {
				arg = mi.X
			}
			t := derefType(arg.Type())

			if sl, ok := t.(*types.Slice); ok {
				t = sl.Elem()
			}
			return t
		}
		if sig, ok := cc.Method.Type().(*types.Signature); ok && sig.Results().Len() > 0 {
			return derefType(sig.Results().At(0).Type())
		}
	}
	return nil
}

// unwrapConst 取实参常量（Q177 真实 ORM 形态）：xorm 的
// Table(tableNameOrBean interface{}) / Where(query interface{}, ...) /
// Exec(sql interface{}, ...) 参数是 interface{}——字符串字面量在 SSA
// 中为 *ssa.MakeInterface{X: *ssa.Const}，统一解包后再取常量。
func unwrapConst(value ssa.Value) (*ssa.Const, bool) {
	if wrapped, ok := value.(*ssa.MakeInterface); ok {
		value = wrapped.X
	}
	constantValue, ok := value.(*ssa.Const)
	return constantValue, ok && constantValue.Value != nil
}
