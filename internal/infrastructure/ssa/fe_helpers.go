package ssa

import (
	"fmt"
	"go/token"
	"go/types"
	"strings"

	"go.uber.org/zap"
	"golang.org/x/tools/go/ssa"
)

// faUses 扫描 FieldAddr/IndexAddr 的使用方式：是否被 Store 写入、
// 是否被 UnOp(MUL) 解引用读、是否作为调用实参（取址传参）流出。
func faUses(addr ssa.Value) (hasStore, hasDeref bool) {
	logger := zap.L()
	logger.Debug("enter faUses")
	defer logger.Debug("exit faUses")
	refs := addr.Referrers()
	if refs == nil {
		return false, false
	}
	for _, u := range *refs {
		if st, ok := u.(*ssa.Store); ok && st.Addr == addr {
			hasStore = true
		}
		if un, ok := u.(*ssa.UnOp); ok && un.Op == token.MUL && un.X == addr {
			hasDeref = true
		}

		if call, ok := u.(*ssa.Call); ok && callArg(call, addr) {
			hasDeref = true
		}
	}
	return hasStore, hasDeref
}

// callArg 判断指令调用实参（含接收者）是否包含指定值。
func callArg(call *ssa.Call, v ssa.Value) bool {
	for _, a := range call.Call.Args {
		if a == v {
			return true
		}
	}
	return false
}

// fieldAddrUse 判定 FieldAddr 的最终读写用途，内层 FieldAddr（仅被其他
// FieldAddr 作为取址中间层引用）的用途从外层递归传播：
//   - m.cfg.APIKey 读 → 内层 m.cfg 也是读（而非"无用途默认 write"）
//   - x.a.b = v 写 → 内层 x.a 也是写
//   - x.a = x.a + 1 → 内层同时 read+write
func fieldAddrUse(fa *ssa.FieldAddr) (hasStore, hasDeref bool) {
	refs := fa.Referrers()
	if refs == nil {
		return false, false
	}
	for _, u := range *refs {
		switch uu := u.(type) {
		case *ssa.Store:
			if uu.Addr == fa {
				hasStore = true
			}
		case *ssa.UnOp:
			if uu.Op == token.MUL && uu.X == fa {
				hasDeref = true
			}
		case *ssa.FieldAddr:
			if uu.X == fa {
				s, d := fieldAddrUse(uu)
				hasStore, hasDeref = hasStore || s, hasDeref || d
			}
		case *ssa.Call:

			if callArg(uu, fa) {
				hasDeref = true
			}
		}
	}
	return hasStore, hasDeref
}

// fieldNameOf 取类型第 idx 个字段名（derefStruct 失败返回空）。
func fieldNameOf(t types.Type, idx int) string {
	_, st := derefStruct(t)
	if st == nil {
		return ""
	}
	return st.Field(idx).Name()
}

// derefStruct 解指针/取底层结构体；返回具名类型（可空）与结构体（可空）。
// 匿名 struct 类型：named 为 nil、st 可用（full_path 回退场景，§6.1）。
func derefStruct(t types.Type) (*types.Named, *types.Struct) {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	if named, ok := t.(*types.Named); ok {
		if st, ok2 := named.Underlying().(*types.Struct); ok2 {
			return named, st
		}
		return nil, nil
	}
	if st, ok := t.(*types.Struct); ok {
		return nil, st
	}
	return nil, nil
}

// originKind 区分 SSA 值来源（field_trace.md §4.1）。
func originKind(v ssa.Value) string {
	switch x := v.(type) {
	case *ssa.Parameter:
		parent := x.Parent()
		if parent != nil && parent.Signature.Recv() != nil {
			if params := parent.Params; len(params) > 0 && params[0] == x {
				return "receiver"
			}
		}
		return "param"
	case *ssa.Alloc:
		if x.Heap {
			return "alloc"
		}
		return "local"
	case *ssa.FreeVar:
		return "local"
	case *ssa.Global:
		return "global"
	case *ssa.Const:
		return "literal"
	case *ssa.Phi:
		return "phi"
	case *ssa.Call:
		return "call_result"
	}
	return "local"
}

// ssaOp 返回 SSA 指令类型名（如 field / fieldaddr / store）。
func ssaOp(v ssa.Value) string {
	return strings.TrimPrefix(fmt.Sprintf("%T", v), "*ssa.")
}

// isSSAName 判断是否为 SSA 临时名（t0、t91 等），用于决定展示名回退。
func isSSAName(name string) bool {
	if len(name) < 2 || name[0] != 't' {
		return false
	}
	for _, c := range name[1:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
