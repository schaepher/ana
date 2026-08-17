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
		if callVal != nil {
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

// parseSQLStmt 从 SQL 语句提取表名、列名与 WHERE 过滤列（Q97 启发式，
// 不做完整 SQL 解析）：
//
//	INSERT INTO t(a, b) VALUES(?, ?)  → t, [a b], nil
//	UPDATE t SET a=?, b=?             → t, [a b], nil
//	DELETE FROM t                     → t, [], nil
//	SELECT a, b FROM t                → t, [a b], nil（P0-2 读路径）
//	SELECT * FROM t                   → t, [], nil（表级）
//	... WHERE y = ?                   → ..., [], [y]（表关联：值实参按 ? 顺序映射）

func whereColsOf(where string) []string {
	var cols []string
	re := regexp.MustCompile(`\s+(AND|OR)\s+`)

	if i := regexp.MustCompile(`\s+ORDER\s+BY\s+`).FindStringIndex(where); i != nil {
		where = where[:i[0]]
	}
	for _, part := range re.Split(where, -1) {
		if i := strings.Index(part, " IN ("); i >= 0 {
			part = part[:i]
		} else if strings.HasSuffix(strings.ToLower(part), " is null") {
			part = part[:len(part)-len(" is null")]
		} else if i := strings.LastIndex(part, "?"); i >= 0 {
			part = part[:i]
		}

		if m := regexp.MustCompile(`\s*[=<>!]+\s*\S+$`).FindStringIndex(part); m != nil {
			part = part[:m[0]]
		}
		part = strings.TrimRight(part, " =<>!()")
		part = strings.TrimSpace(part)
		if part != "" {
			cols = append(cols, part)
		}
	}
	return cols
}
