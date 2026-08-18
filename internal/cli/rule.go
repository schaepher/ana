package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
	"go.uber.org/zap"
)

// cmdRule 用户连线规则管理（Q220c）：
//
//	codeintel rule add "<expr>" --repo <path>  添加规则
//	    expr 形态（→ 或 -> 均可）：
//	      x → B.y        模式规则：所有含 x 列的表 → B.y
//	      A.x → B.y      显式列对：仅 A.x → B.y
//	      x → B          目标列省略时默认 B.id
//	codeintel rule list [--json] --repo <path>  列出规则
//	codeintel rule remove <id> --repo <path>    删除规则
//
// 规则存 relation_rules 表（数据库），clean/reindex 保留；生效时校验
// 目标表/列真实存在（不存在静默跳过）。生成关系 type=fk（ER 默认显示）。
func cmdRule(args []string) int {
	logger := zap.L()
	logger.Debug("enter cmdRule")
	defer logger.Debug("exit cmdRule")
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "用法: codeintel rule add|list|remove …")
		return 2
	}
	repoDir, rest, err := parseRepoFlag(args[1:])
	if err != nil || repoDir == "" {
		fmt.Fprintln(os.Stderr, "--repo 是必需的")
		return 2
	}
	db, err := sqlite.Open(repoDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer db.Close()
	r := sqlite.NewRepo(db)

	switch args[0] {
	case "add":
		return ruleAdd(r, rest)
	case "list":
		return ruleList(r, rest)
	case "remove":
		return ruleRemove(r, rest)
	default:
		fmt.Fprintf(os.Stderr, "unknown rule subcommand: %s\n", args[0])
		return 2
	}
}

// ruleAdd 解析表达式并添加规则。
func ruleAdd(r *sqlite.Repo, args []string) int {
	logger := zap.L()
	logger.Debug("enter ruleAdd")
	defer logger.Debug("exit ruleAdd")
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "用法: codeintel rule add \"a_id → table_b.id\" --repo <path>")
		return 2
	}
	var exprParts []string
	for _, a := range args {
		if a == "--json" {
			continue
		}
		exprParts = append(exprParts, a)
	}
	expr := strings.Join(exprParts, " ")
	from, to, ok := splitRuleExpr(expr)
	if !ok {
		fmt.Fprintf(os.Stderr, "规则表达式无效: %q（形态：x → B.y 或 A.x → B.y）\n", expr)
		return 2
	}
	var rule sqlite.RelationRule
	if i := strings.Index(from, "."); i >= 0 {
		rule.FromTable, rule.FromCol = from[:i], from[i+1:]
	} else {
		rule.FromCol = from
	}
	if i := strings.Index(to, "."); i >= 0 {
		rule.ToTable, rule.ToCol = to[:i], to[i+1:]
	} else {
		rule.ToTable, rule.ToCol = to, "id"
	}
	id, err := r.AddRelationRule(rule)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	kind := "模式"
	if rule.FromTable != "" {
		kind = "显式"
	}
	fmt.Printf("已添加%s规则 #%d: %s.%s → %s.%s（生效时校验表/列存在）\n",
		kind, id, rule.FromTable, rule.FromCol, rule.ToTable, rule.ToCol)
	return 0
}

// ruleList 列出规则（--json 输出 RelationRule 数组）。
func ruleList(r *sqlite.Repo, args []string) int {
	logger := zap.L()
	logger.Debug("enter ruleList")
	defer logger.Debug("exit ruleList")
	asJSON := false
	for _, a := range args {
		if a == "--json" {
			asJSON = true
		}
	}
	rules, err := r.ListRelationRules()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if asJSON {
		b, err := json.Marshal(rules)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Println(string(b))
		return 0
	}
	if len(rules) == 0 {
		fmt.Println("（无规则）")
		return 0
	}
	for _, ru := range rules {
		scope := ru.FromTable
		if scope == "" {
			scope = "*"
		}
		fmt.Printf("#%d  %s.%s → %s.%s\n", ru.ID, scope, ru.FromCol, ru.ToTable, ru.ToCol)
	}
	return 0
}

// ruleRemove 删除规则。
func ruleRemove(r *sqlite.Repo, args []string) int {
	logger := zap.L()
	logger.Debug("enter ruleRemove")
	defer logger.Debug("exit ruleRemove")
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "用法: codeintel rule remove <id> --repo <path>")
		return 2
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "规则 id 无效: %q\n", args[0])
		return 2
	}
	if err := r.RemoveRelationRule(id); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("已删除规则 #%d\n", id)
	return 0
}

// splitRuleExpr 拆分规则表达式 "A.x → B.y"（→ 或 ->）。
func splitRuleExpr(expr string) (from, to string, ok bool) {
	arrow := "->"
	if i := strings.Index(expr, "→"); i >= 0 {
		arrow = "→"
	}
	i := strings.Index(expr, arrow)
	if i < 0 {
		return "", "", false
	}
	from = strings.TrimSpace(expr[:i])
	to = strings.TrimSpace(expr[i+len(arrow):])
	if from == "" || to == "" {
		return "", "", false
	}
	if strings.Count(from, ".") > 1 || strings.Count(to, ".") > 1 {
		return "", "", false
	}
	return from, to, true
}

// parseRepoFlag 提取 --repo <path>（或 --repo=path）并返回剩余参数。
func parseRepoFlag(args []string) (string, []string, error) {
	var repo string
	var rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--repo" && i+1 < len(args):
			repo = args[i+1]
			i++
		case strings.HasPrefix(a, "--repo="):
			repo = strings.TrimPrefix(a, "--repo=")
		case a == "--json":
			rest = append(rest, a)
		default:
			rest = append(rest, a)
		}
	}
	return repo, rest, nil
}
