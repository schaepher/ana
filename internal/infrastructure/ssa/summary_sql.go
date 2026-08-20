package ssa

import (
	"fmt"
	"go/constant"
	"regexp"
	"strings"

	"github.com/schaepher/codeintel/internal/domain"
	"go.uber.org/zap"
	"golang.org/x/tools/go/ssa"
)

// applySQLSummary 处理 SQL 语句调用（Q97）：SQL 字符串（第 0 实参）解析
// 表名与列名 → 虚拟节点（Name=表.列）；后续值实参按 ? 顺序映射列，
// 发 summary_io 边（字段值 → 虚拟节点）。
// applySQLSummary 处理 SQL 语句摘要：SQL 字符串在 Args[sqlArg]（database/sql
// 的 receiver 后 Args[1]；gof Connector 接口无 receiver 在 Args[0]，Q158），
// 值实参在 sqlArg+1 起（variadic 解包按 ?/$N 顺序映射）。
func (ext *fieldExtractor) applySQLSummary(cc *ssa.CallCommon, calleeID domain.CanonicalID, spec summarySpec, callVal ssa.Value, sqlArg int) error {
	logger := zap.L()
	logger.Debug("enter (fieldExtractor).applySQLSummary")
	defer logger.Debug("exit (fieldExtractor).applySQLSummary")
	if sqlArg < 0 || sqlArg >= len(cc.Args) {
		return nil
	}
	sqlStr := ""
	if c, ok := unwrapConst(cc.Args[sqlArg]); ok {
		// Q177 真实形态：Exec(sql interface{}) 常量被 MakeInterface 包装
		sqlStr = constant.StringVal(c.Value)
	}
	table, cols, whereCols := parseSQLStmt(sqlStr)
	line := ext.prog.Fset.PositionFor(cc.Pos(), false).Line

	if !spec.SQLWrite {
		if table == "" {
			return nil
		}
		if len(cols) == 0 {
			cols = []string{""}
		}
		var callID domain.CanonicalID
		// Q201：Query(sql, callback) 形态——读出值进入回调闭包的形参
		// （rows），read 节点边指向闭包形参（归属父函数）而非调用返回值
		// （返回值与回调形参静态无连接，链断在闭包；go2o 实测
		// settleRiseData 的 pf_riseinfo.person_id 读出值因此断链）。
		// 闭包内 rows.Scan(&i) 后 i 参与后续值流，跨函数链贯通
		if cb := callbackClosureParam(cc); cb != nil {
			callID, _ = ext.emitValue(cb)
		} else if callVal != nil {
			callID, _ = ext.emitValue(callVal)
		}
		for _, col := range cols {
			name := table
			if col != "" {
				name = table + "." + col
			}
			id := domain.CanonicalID(string(ext.funcID) + "#ext.sql." + name + ".read@" + fmt.Sprintf("%d", line))
			if err := ext.emit(domain.Item{Node: &domain.CodeEntity{
				ID:        id,
				Kind:      domain.KindFieldAccess,
				Name:      name,
				FilePath:  ext.currentFile,
				LineStart: line,
				Properties: map[string]any{
					"full_path":     name,
					"instance_path": name,
					"access_kind":   "read",
					"code_snippet":  sqlStr,
					"type_string":   "sql",
					"is_external":   "true",
					"func_id":       string(ext.funcID),
				},
			}}); err != nil {
				return err
			}
			if callID != "" {

				if err := ext.emitEdgeKindLine(id, domain.CanonicalID(callID), domain.FactSummaryIO, line); err != nil {
					return err
				}
			}
		}

		if len(whereCols) > 0 {
			values := []ssa.Value{}
			for i := sqlArg + 1; i < len(cc.Args); i++ {
				values = append(values, variadicElems(cc.Args[i])...)
			}
			for i, arg := range values {
				if i >= len(whereCols) {
					break
				}
				name := table + "." + whereCols[i]
				id := domain.CanonicalID(string(ext.funcID) + "#ext.sql." + name + ".filter@" + fmt.Sprintf("%d", line))
				if err := ext.emit(domain.Item{Node: &domain.CodeEntity{
					ID:        id,
					Kind:      domain.KindFieldAccess,
					Name:      name,
					FilePath:  ext.currentFile,
					LineStart: line,
					Properties: map[string]any{
						"full_path":     name,
						"instance_path": name,
						"access_kind":   "filter",
						"code_snippet":  sqlStr,
						"type_string":   "sql",
						"is_external":   "true",
						"func_id":       string(ext.funcID),
					},
				}}); err != nil {
					return err
				}
				realArg := arg
				if mi, ok := arg.(*ssa.MakeInterface); ok {
					realArg = mi.X
				}
				argID, err := ext.emitValue(realArg)
				if err != nil || argID == "" {
					continue
				}
				if err := ext.emitEdgeKindLine(argID, id, domain.FactSummaryIO, line); err != nil {
					return err
				}
			}
		}
		return nil
	}

	access := "write"
	values := []ssa.Value{}
	for i := sqlArg + 1; i < len(cc.Args); i++ {
		values = append(values, variadicElems(cc.Args[i])...)
	}
	for i, arg := range values {
		col := ""
		if i < len(cols) {
			col = cols[i]
		}
		name := table
		if col != "" {
			name = table + "." + col
		}
		if name == "" {
			continue
		}
		realArg := arg
		if mi, ok := arg.(*ssa.MakeInterface); ok {
			realArg = mi.X
		}
		argID, err := ext.emitValue(realArg)
		if err != nil || argID == "" {
			continue
		}
		id := domain.CanonicalID(string(ext.funcID) + "#ext.sql." + name + "." + access + "@" + fmt.Sprintf("%d", line))
		if err := ext.emit(domain.Item{Node: &domain.CodeEntity{
			ID:        id,
			Kind:      domain.KindFieldAccess,
			Name:      name,
			FilePath:  ext.currentFile,
			LineStart: line,
			Properties: map[string]any{
				"full_path":     name,
				"instance_path": name,
				"access_kind":   access,
				"code_snippet":  sqlStr,
				"type_string":   "sql",
				"is_external":   "true",
				"func_id":       string(ext.funcID),
			},
		}}); err != nil {
			return err
		}
		if err := ext.emitEdgeKindLine(argID, id, domain.FactSummaryIO, line); err != nil {
			return err
		}
	}

	if len(whereCols) > 0 {
		for i, col := range whereCols {
			vi := len(cols) + i
			if vi >= len(values) {
				break
			}
			name := table + "." + col
			id := domain.CanonicalID(string(ext.funcID) + "#ext.sql." + name + ".filter@" + fmt.Sprintf("%d", line))
			if err := ext.emit(domain.Item{Node: &domain.CodeEntity{
				ID:        id,
				Kind:      domain.KindFieldAccess,
				Name:      name,
				FilePath:  ext.currentFile,
				LineStart: line,
				Properties: map[string]any{
					"full_path":     name,
					"instance_path": name,
					"access_kind":   "filter",
					"code_snippet":  sqlStr,
					"type_string":   "sql",
					"is_external":   "true",
					"func_id":       string(ext.funcID),
				},
			}}); err != nil {
				return err
			}
			realArg := values[vi]
			if mi, ok := realArg.(*ssa.MakeInterface); ok {
				realArg = mi.X
			}
			argID, err := ext.emitValue(realArg)
			if err != nil || argID == "" {
				continue
			}
			if err := ext.emitEdgeKindLine(argID, id, domain.FactSummaryIO, line); err != nil {
				return err
			}
		}
	}
	return nil
}

// callbackClosureParam SQL 调用实参中的回调闭包首参（Query(sql,
// func(rows){...}) 的 rows）：读出值经回调形参进入闭包，闭包内
// Scan(&i) 后 i 参与后续值流。无回调实参返回 nil。
func callbackClosureParam(cc *ssa.CallCommon) ssa.Value {
	for _, a := range cc.Args {
		var fn *ssa.Function
		switch x := a.(type) {
		case *ssa.MakeClosure:
			fn, _ = x.Fn.(*ssa.Function)
		case *ssa.MakeInterface:
			if mc, ok := x.X.(*ssa.MakeClosure); ok {
				fn, _ = mc.Fn.(*ssa.Function)
			}
		}
		if fn != nil && len(fn.Params) > 0 {
			return fn.Params[0]
		}
	}
	return nil
}

// whereColsOf 从 where 条件串提取列名：AND/OR 拆分 + 占位符剥离
// （IN (?) 先处理；其余形态截到最后一个 ? 再 TrimRight 运算符——
// 兼容 " = ?" / "=?" / " <?" / " LIKE ?" 等有无空格写法，以及多行
// 条件串（AND/OR 前后为换行/制表符——pay_order 实测整串未被拆分）。

func whereColsOf(where string) []string {
	var cols []string
	up := strings.ToUpper(where)
	// 尾部子句清理（LIMIT/OFFSET/ORDER BY/GROUP BY/HAVING）——此前残留
	// 进列名（"alias = $1 LIMIT 1" → 整串当列名）
	for _, stop := range []string{" ORDER BY ", " GROUP BY ", " LIMIT ", " OFFSET ", " HAVING "} {
		if j := strings.Index(up, stop); j >= 0 {
			where = where[:j]
			up = strings.ToUpper(where)
			// whereColsOf 从 where 条件串提取列名：AND/OR 拆分 + 占位符剥离
			// （IN (?) 先处理；其余形态截到最后一个 ? 再 TrimRight 运算符——
			// 兼容 " = ?" / "=?" / " <?" / " LIKE ?" 等有无空格写法，以及多行
			// 条件串（AND/OR 前后为换行/制表符——pay_order 实测整串未被拆分）。

		}
	}
	// Q220：AND/OR 拆分大小写不敏感（此前区分大小写——go2o 的 lowercase
	// " and " 整串未拆分 → 列名含 " = ? and ..." 垃圾：pay_merchant.user_type
	// = ? and user_id、ad_data.ad_id=$1 and id 等）
	for _, part := range whereCondRe.Split(where, -1) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if m := whereColLeadRe.FindStringSubmatch(part); m != nil {
			cols = append(cols, m[1])
			continue
		}
		// 无操作符裸列名形态（In("amount")/NotIn("status")）；占位符/
		// 字面量残留（BETWEEN $1 and $2 的 $2）跳过
		if part[0] < 'A' || (part[0] > 'Z' && part[0] < 'a') || part[0] > 'z' {
			continue
		}
		if i := strings.IndexAny(part, " \t\n"); i >= 0 {
			part = part[:i]
		}
		part = strings.Trim(part, "`\"[]()")
		if part != "" {
			cols = append(cols, part)
		}
	}
	return cols
}

// whereCondRe AND/OR 条件拆分（大小写不敏感，\s 覆盖换行/制表符）。
var whereCondRe = regexp.MustCompile(`(?i)\s+(AND|OR)\s+`)

// whereColLeadRe 条件串首列名提取：列名 + 操作符（= / <> / < / > /
// LIKE / BETWEEN / IS / IN），操作符后须接空白或占位符/数字（兼容
// "ad_id=$1" 无空格、"? " 与 "0" 字面量）。列名支持表前缀（b.id）。
var whereColLeadRe = regexp.MustCompile(`(?i)^([A-Za-z_][A-Za-z0-9_.]*)\s*(?:=|<>|<=|>=|<|>|LIKE|BETWEEN|IS|IN)(?:\s|[$\?0-9])`)
