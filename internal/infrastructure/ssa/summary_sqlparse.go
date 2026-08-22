package ssa

import (
	"regexp"
	"strings"
)

var whereColRe = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_.]*)\s*=\s*(\?|\$\d+)`)

// sqlJoinPair JOIN ON 等值键对（Q239）：JOIN 表.列 = 主表.列
// （SQL 书写顺序：左 = From，右 = To；别名已映射回真实表名）。
type sqlJoinPair struct {
	FromTable, FromCol string
	ToTable, ToCol     string
}

// sqlKwdSet SQL 关键字集合（别名判定：JOIN 段表名后的单词若是关键字
// 则不是别名——ON/WHERE/ORDER 等）。
var sqlKwdSet = map[string]bool{
	"INNER": true, "LEFT": true, "RIGHT": true, "CROSS": true, "FULL": true,
	"OUTER": true, "JOIN": true, "ON": true, "WHERE": true, "AND": true,
	"OR": true, "ORDER": true, "BY": true, "LIMIT": true, "GROUP": true,
	"HAVING": true, "UNION": true, "SELECT": true, "FROM": true, "SET": true,
	"VALUES": true, "AS": true, "WHEN": true, "THEN": true, "ELSE": true,
	"END": true, "CASE": true, "NOT": true, "IN": true, "IS": true,
	"NULL": true, "BETWEEN": true, "LIKE": true, "EXISTS": true, "DISTINCT": true,
}

// nextToken 取下一 token（空白/括号/逗号分隔）与剩余部分。
func nextToken(s string) (tok, rest string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", ""
	}
	i := 0
	for i < len(s) {
		c := s[i]
		if c == ' ' || c == '\t' || c == '\n' || c == ',' || c == '(' || c == ')' || c == ';' {
			break
		}
		i++
	}
	return s[:i], strings.TrimSpace(s[i:])
}

// splitSQLCond 按 AND/OR 拆分 ON 条件（顶层；括号内不拆——简单扫描）。
func splitSQLCond(cond string) []string {
	var parts []string
	depth := 0
	start := 0
	up := strings.ToUpper(cond)
	for i := 0; i < len(cond); i++ {
		switch cond[i] {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		}
		if depth == 0 && (up[i] == ' ' || up[i] == '\n' || up[i] == '\t') {
			rest := up[i:]
			for _, k := range []string{" AND ", " OR "} {
				if strings.HasPrefix(rest, k) {
					parts = append(parts, strings.TrimSpace(cond[start:i]))
					start = i + len(k)
					i += len(k) - 1
					break
				}
			}
		}
	}
	parts = append(parts, strings.TrimSpace(cond[start:]))
	return parts
}

// splitQualified `a.code` → (a, code)；无点 → ("", token)。
func splitQualified(s string) (string, string) {
	s = strings.Trim(s, "`\"[]")
	if i := strings.LastIndex(s, "."); i >= 0 {
		return s[:i], s[i+1:]
	}
	return "", s
}

// extractJoinPairs 从 FROM 之后剩余段提取 JOIN ON 等值键对（Q239）：
//
//	FROM sys_sub_station s INNER JOIN sys_district a ON a.code = s.city_code
//	  → {sys_district.code → sys_sub_station.city_code}
//
// 覆盖 INNER/LEFT/RIGHT/CROSS JOIN + 别名映射 + AND 多键对；逗号连接 /
// 子查询 JOIN / 无 ON 的 CROSS JOIN 放弃（无键信号）。
func extractJoinPairs(rest string) []sqlJoinPair {
	up := strings.ToUpper(rest)
	if !strings.Contains(up, " JOIN ") {
		return nil
	}
	// 别名映射：FROM 首表 + 首表别名 + 各 JOIN 表
	aliases := map[string]string{}
	firstTok, firstRest := nextToken(rest)
	if firstTok != "" {
		aliases[firstTok] = firstTok
		if a, _ := nextToken(firstRest); a != "" && !sqlKwdSet[strings.ToUpper(a)] && !strings.HasPrefix(a, "(") {
			aliases[a] = firstTok
		}
	}
	var pairs []sqlJoinPair
	for {
		j := strings.Index(up, " JOIN ")
		if j < 0 {
			break
		}
		seg := rest[j+len(" JOIN "):]
		// JOIN 表名（跳过 INNER/LEFT 等已在 JOIN 前）
		tbl, afterTbl := nextToken(seg)
		if tbl == "" {
			break
		}
		aliases[tbl] = tbl
		// 可选别名（非关键字且非 ON 起始）
		if a, afterAlias := nextToken(afterTbl); a != "" && !sqlKwdSet[strings.ToUpper(a)] {
			aliases[a] = tbl
			afterTbl = afterAlias
		}
		// ON 段（兼容 "ON ..." 开头形态——ON 位于段首无前导空格，
		// 偏移 3 而非 4）
		onUp := strings.ToUpper(afterTbl)
		oi := -1
		skip := len(" ON ")
		if strings.HasPrefix(onUp, "ON ") {
			oi = 0
			skip = len("ON ")
		} else {
			oi = strings.Index(onUp, " ON ")
		}
		if oi < 0 {
			break
		}
		onSeg := afterTbl[oi+skip:]
		end := len(onSeg)
		// 修饰符形态在前（更长先匹配——" INNER JOIN " 的 end 比 " JOIN " 早）
		for _, stop := range []string{" INNER JOIN ", " LEFT JOIN ", " RIGHT JOIN ", " CROSS JOIN ",
			" FULL JOIN ", " OUTER JOIN ", " JOIN ", " WHERE ", " ORDER BY ", " LIMIT ",
			" GROUP BY ", " HAVING ", " UNION "} {
			if k := strings.Index(strings.ToUpper(onSeg), stop); k >= 0 && k < end {
				end = k
			}
		}
		onPart := onSeg[:end]
		for _, cond := range splitSQLCond(onPart) {
			eq := strings.Index(cond, "=")
			if eq < 0 {
				continue
			}
			l := strings.TrimSpace(cond[:eq])
			r := strings.TrimSpace(cond[eq+1:])
			lt, lc := splitQualified(l)
			rt, rc := splitQualified(r)
			if lc == "" || rc == "" {
				continue
			}
			lT := aliases[lt]
			if lT == "" {
				lT = lt
			}
			rT := aliases[rt]
			if rT == "" {
				rT = rt
			}
			pairs = append(pairs, sqlJoinPair{lT, lc, rT, rc})
		}
		// 前进防死循环
		if j+len(" JOIN ") >= len(rest) {
			break
		}
		rest = afterTbl
		up = strings.ToUpper(rest)
	}
	return pairs
}

// extractWhereCols 从 SQL 语句剩余部分提取 WHERE 子句的过滤列
// （`列 = ?` 序列，值实参按 ? 顺序映射——表关联分析的数据基础）。
// 支持 a.y = ? 表前缀（去前缀）；WHERE 缺失返回 nil。
func extractWhereCols(rest string) []string {
	up := strings.ToUpper(rest)
	wi := strings.Index(up, " WHERE ")
	if wi < 0 {
		return nil
	}
	wherePart := rest[wi+len(" WHERE "):]
	upPart := strings.ToUpper(wherePart)
	for _, stop := range []string{" ORDER BY ", " LIMIT ", " GROUP BY ", " HAVING ", " UNION "} {
		if j := strings.Index(upPart, stop); j >= 0 {
			wherePart = wherePart[:j]
			break
		}
	}
	var out []string
	for _, m := range whereColRe.FindAllStringSubmatch(wherePart, -1) {
		c := m[1]
		if i := strings.LastIndex(c, "."); i >= 0 {
			c = c[i+1:]
		}
		c = strings.Trim(c, "`\"[]")
		if c != "" {
			out = append(out, c)
		}
	}
	return out
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

func parseSQLStmt(sql string) (table string, cols []string, whereCols []string, joinPairs []sqlJoinPair) {
	upper := strings.ToUpper(sql)
	rest := ""
	switch {
	case strings.Contains(upper, "INSERT INTO"):
		rest = sql[strings.Index(upper, "INSERT INTO")+len("INSERT INTO"):]
	case strings.Contains(upper, "UPDATE"):
		rest = sql[strings.Index(upper, "UPDATE")+len("UPDATE"):]
	case strings.Contains(upper, "DELETE FROM"):
		rest = sql[strings.Index(upper, "DELETE FROM")+len("DELETE FROM"):]
	case strings.Contains(upper, " FROM "):

		fromIdx := strings.Index(upper, " FROM ")
		rest = sql[fromIdx+len(" FROM "):]
		joinPairs = extractJoinPairs(rest)
		if strings.Contains(upper, "SELECT ") {
			selPart := strings.TrimSpace(sql[strings.Index(upper, "SELECT ")+len("SELECT ") : fromIdx])
			if selPart != "" && !strings.Contains(strings.ToUpper(selPart), "*") {

				for _, c := range strings.Split(selPart, ",") {
					c = strings.TrimSpace(c)
					if i := strings.Index(c, " "); i >= 0 {
						c = c[:i]
					}
					if i := strings.LastIndex(c, "."); i >= 0 {
						c = c[i+1:]
					}
					c = strings.Trim(c, "`\"[]")
					if c != "" {
						if c != "" && !strings.Contains(c, "(") {
							cols = append(cols, c)
						}
					}
				}
			}
		}
	default:
		return "", nil, nil, nil
	}
	rest = strings.TrimSpace(rest)

	tableEnd := len(rest)
	for i := 0; i < len(rest); i++ {
		if rest[i] == '(' || rest[i] == ' ' || rest[i] == '\t' || rest[i] == '\n' || rest[i] == ';' {
			tableEnd = i
			break
		}
	}
	table = strings.TrimSpace(rest[:tableEnd])
	table = strings.Trim(table, "`\"[]")
	if table == "" {
		return "", nil, nil, nil
	}

	after := strings.TrimSpace(rest[tableEnd:])
	if strings.HasPrefix(after, "(") {

		inner := after[1:]
		if i := strings.Index(inner, ")"); i >= 0 {
			inner = inner[:i]
		}
		for _, c := range strings.Split(inner, ",") {
			c = strings.TrimSpace(c)
			c = strings.Trim(c, "`\"[]")
			if c != "" {
				cols = append(cols, c)
			}
		}
	} else if strings.Contains(upper, " SET ") {

		up := strings.ToUpper(rest)
		if i := strings.Index(up, " SET "); i >= 0 {
			setPart := rest[i+len(" SET "):]
			if j := strings.Index(setPart, " WHERE"); j >= 0 {
				setPart = setPart[:j]
			}
			for _, c := range strings.Split(setPart, ",") {
				c = strings.TrimSpace(c)
				if k := strings.Index(c, "="); k >= 0 {
					c = strings.TrimSpace(c[:k])
					c = strings.Trim(c, "`\"[]")
					if c != "" {
						cols = append(cols, c)
					}
				}
			}
		}
	}

	whereCols = extractWhereCols(rest)
	return table, cols, whereCols, joinPairs
}
