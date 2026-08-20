package ssa

import (
	"regexp"
	"strings"
)

var whereColRe = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_.]*)\s*=\s*(\?|\$\d+)`)

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
func parseSQLStmt(sql string) (table string, cols []string, whereCols []string) {
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
		return "", nil, nil
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
		return "", nil, nil
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
	return table, cols, whereCols
}
