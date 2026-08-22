package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/schaepher/codeintel/internal/domain"
	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
	"go.uber.org/zap"
)

// cmdRule 用户连线规则管理（Q220c/Q220d）：
//
//	codeintel rule add <from> <to> --repo <path>  添加规则（两个位置参数）：
//	      member_id mm_member.id        模式规则：所有含 member_id 列的表 → mm_member.id
//	      mm_relation.member_id mm_member.id  显式列对（单对）
//	      目标列省略时默认 id；输出用箭头（→）；单参形态兼容旧解析
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
	// Q237：--repo 缺省当前目录（parseRepoFlag 默认 "."）
	repoDir, rest, err := parseRepoFlag(args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
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

// ruleAdd 添加规则（Q220d：from/to 分为两个位置参数，输出用箭头）：
//
//	codeintel rule add member_id mm_member.id           模式规则（所有含 member_id 列的表 → mm_member.id）
//	codeintel rule add mm_relation.member_id mm_member.id   显式列对（单对）
//
// 目标列省略时默认 id（to 传 mm_member 亦可）。单参数形态兼容旧解析
// （"from x to y" / "x → y"）。输出用箭头（→）。
func ruleAdd(r *sqlite.Repo, args []string) int {
	logger := zap.L()
	logger.Debug("enter ruleAdd")
	defer logger.Debug("exit ruleAdd")
	var positional []string
	for _, a := range args {
		if a == "--json" {
			continue
		}
		positional = append(positional, a)
	}
	var from, to string
	switch {
	case len(positional) >= 2:
		from, to = positional[0], positional[1]
	case len(positional) == 1:
		// 兼容旧单参形态（"from x to y" / "x → y"）
		var ok bool
		from, to, ok = splitRuleExpr(positional[0])
		if !ok {
			fmt.Fprintf(os.Stderr, "规则表达式无效: %q（用法：rule add <from> <to>，如 rule add member_id mm_member.id）\n", positional[0])
			return 2
		}
	default:
		fmt.Fprintln(os.Stderr, "用法: codeintel rule add <from> <to> --repo <path>（如 rule add member_id mm_member.id）")
		return 2
	}
	var rule domain.RelationRule
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

// splitRuleExpr 拆分规则表达式（Q220d：主语法 from/to）：
//
//	"from A.x to B.y"   显式列对
//	"from x to B.y"     模式规则
//	"from x to B"       目标列省略默认 id
//
// 大小写不敏感（FROM/TO 亦可）；兼容旧箭头形态 "A.x → B.y"（-> 亦可）。
func splitRuleExpr(expr string) (from, to string, ok bool) {
	up := strings.ToUpper(strings.TrimSpace(expr))
	from, to, ok = "", "", false
	if i := strings.Index(up, " TO "); i >= 0 && strings.HasPrefix(up, "FROM ") {
		from = strings.TrimSpace(expr[len("FROM "):i])
		to = strings.TrimSpace(expr[i+len(" TO "):])
		ok = from != "" && to != ""
	} else {
		// 兼容旧箭头形态
		arrow := "->"
		if j := strings.Index(expr, "→"); j >= 0 {
			arrow = "→"
		}
		if j := strings.Index(expr, arrow); j >= 0 {
			from = strings.TrimSpace(expr[:j])
			to = strings.TrimSpace(expr[j+len(arrow):])
			ok = from != "" && to != ""
		}
	}
	if !ok || strings.Count(from, ".") > 1 || strings.Count(to, ".") > 1 {
		return "", "", false
	}
	return from, to, true
}

// parseRepoFlag 提取 --repo <path>（或 --repo=path）并返回剩余参数。
// Q237：未指定 --repo 默认当前工作目录（"."）；Q238：指定值经注册表
// 短名/后缀/module 解析。
func parseRepoFlag(args []string) (string, []string, error) {
	repo := "."
	var rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--repo" && i+1 < len(args):
			repo = ResolveRepoRef(args[i+1])
			i++
		case strings.HasPrefix(a, "--repo="):
			repo = ResolveRepoRef(strings.TrimPrefix(a, "--repo="))
		case a == "--json":
			rest = append(rest, a)
		default:
			rest = append(rest, a)
		}
	}
	return repo, rest, nil
}
