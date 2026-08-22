package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/schaepher/codeintel/internal/action"
	"github.com/schaepher/codeintel/internal/domain"
	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
	"github.com/schaepher/codeintel/internal/logging"
	"go.uber.org/zap"
)

// outputOpts 查询输出选项（--json / --compact，Q96）。
type outputOpts struct {
	json     bool // 结构化 JSON 输出（stdout 仅 JSON，日志已切文件）
	compact  bool // 树形/表格输出压缩为紧凑形式
	repoPath string // 目标仓库根（Q235-10：value-trace 源码片段读取）
}

// encodeJSON 输出结构化 JSON（stdout 唯一内容）。
func encodeJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// cmdQuery 实现 `codeintel query ...`。
func cmdQuery(args []string) int {
	logger := zap.L()
	logger.Debug("enter cmdQuery")
	defer logger.Debug("exit cmdQuery")
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "error: query 需要一个子命令（symbol/fields/trace-backward/trace-forward/value-trace/summary/path/unused/callers/callees/impact）")
		return 2
	}
	sub := args[0]
	rest := args[1:]

	f := parseQueryFlags(rest)
	target := ""

	if sub != "unused" && sub != "module-calls" && !(sub == "relations" && f.all) {
		if len(f.positional) < 1 {
			fmt.Fprintf(os.Stderr, "error: 缺少符号参数\n")
			return 2
		}
		target = f.positional[0]
	}

	abs, _, err := resolveRepo(f.repoPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		if f.repoPath == "." { // Q238：缺省 cwd 非仓库时附引导（Q13）
			printRepoHint()
		}
		return 1
	}

	if err := logging.ToFile(abs); err != nil {
		fmt.Fprintf(os.Stderr, "warning: 日志切换失败: %v\n", err)
	}
	db, err := sqlite.Open(abs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()
	acts := action.New(sqlite.NewRepo(db))

	if tip := staleInfo(abs, sqlite.NewRepo(db)); tip != "" {
		fmt.Fprintf(os.Stderr, "warning: %s\n", tip)
	}

	opts := outputOpts{json: f.json, compact: f.compact, repoPath: f.repoPath}
	// --since 标注（§17.2）：symbol/fields/callers/callees/impact 输出
	// 对函数/方法节点标注 [new]/[mod]
	var since *domain.SinceInfo
	if f.since != "" {
		since = runGitDiffSince(abs, f.since)
	}
	switch sub {
	case "symbol":
		return querySymbol(acts, target, opts, since)
	case "fields":
		return queryFields(acts, target, opts, since)
	case "trace-backward", "trace-forward":
		return queryTraceDir(acts, target, f.funcPath, f.maxDepth, sub == "trace-forward", f.followIndirect, opts)
	case "value-trace":

		mc := f.minConf
		if !f.minConfSet {
			mc = 1.0
		}
		return queryValueTrace(acts, target, f.maxDepth, mc, f.includeContainer, opts, f.format)
	case "summary":
		return querySummary(acts, target, opts, f.format)
	case "context":
		return queryContext(acts, target, opts)
	case "unused":
		return queryUnused(acts, abs, f)
	case "module-calls":
		module := ""
		if len(f.positional) >= 1 {
			module = f.positional[0]
		}
		return queryModuleCalls(acts, module, opts)
	case "table":
		return queryTable(acts, target, opts)
	case "relations":
		if f.all {
			return queryRelationsAll(acts, f.format, opts, &f)
		}
		return queryRelations(acts, target, f.format, opts, &f)
	case "path":
		return queryPath(acts, f.positional[0], f.positional[1], f)
	case "callers", "callees", "impact":
		d := f.depth
		if d <= 0 {
			switch sub {
			case "impact":
				d = 3
			default:
				d = 1
			}
		}
		return queryGraph(acts, sub, target, d, opts, since)
	default:
		fmt.Fprintf(os.Stderr, "error: unknown query subcommand %q\n", sub)
		return 2
	}
}

// parseQueryFlags 手动解析 query 子命令的参数，支持 flags 与位置参数任意顺序。
func parseQueryFlags(args []string) queryFlags {
	logger := zap.L()
	logger.Debug("enter parseQueryFlags")
	defer logger.Debug("exit parseQueryFlags")
	f := queryFlags{repoPath: "."}
	// Q197 跳数上限：-1 = 未传（用默认 4）；显式 0 = 该类型不限制
	f.queryMaxHops, f.writeMaxHops, f.readMaxHops = -1, -1, -1
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--repo" && i+1 < len(args):
			f.repoPath = resolveRepoRef(args[i+1]) // Q238：注册表短名/后缀/module
			i++
		case strings.HasPrefix(a, "--repo="):
			f.repoPath = resolveRepoRef(strings.TrimPrefix(a, "--repo="))
		case a == "--depth" && i+1 < len(args):
			f.depth, _ = strconv.Atoi(args[i+1])
			i++
		case strings.HasPrefix(a, "--depth="):
			f.depth, _ = strconv.Atoi(strings.TrimPrefix(a, "--depth="))
		case a == "--max-depth" && i+1 < len(args):
			f.maxDepth, _ = strconv.Atoi(args[i+1])
			i++
		case strings.HasPrefix(a, "--max-depth="):
			f.maxDepth, _ = strconv.Atoi(strings.TrimPrefix(a, "--max-depth="))
		case a == "--func" && i+1 < len(args):
			f.funcPath = args[i+1]
			i++
		case strings.HasPrefix(a, "--func="):
			f.funcPath = strings.TrimPrefix(a, "--func=")
		case a == "--since" && i+1 < len(args):
			f.since = args[i+1]
			i++
		case strings.HasPrefix(a, "--since="):
			f.since = strings.TrimPrefix(a, "--since=")
		case a == "--fail-on" && i+1 < len(args):
			f.failOn = args[i+1]
			i++
		case strings.HasPrefix(a, "--fail-on="):
			f.failOn = strings.TrimPrefix(a, "--fail-on=")
		case a == "--min-conf" && i+1 < len(args):
			f.minConf, _ = strconv.ParseFloat(args[i+1], 64)
			f.minConfSet = true
			i++
		case strings.HasPrefix(a, "--min-conf="):
			f.minConf, _ = strconv.ParseFloat(strings.TrimPrefix(a, "--min-conf="), 64)
			f.minConfSet = true
		case a == "--include-container":
			f.includeContainer = true
		case a == "--follow-indirect":
			f.followIndirect = true
		case a == "--all":
			f.all = true
		case a == "--type" && i+1 < len(args):
			f.relTypes = append(f.relTypes, strings.Split(args[i+1], ",")...)
			i++
		case strings.HasPrefix(a, "--type="):
			f.relTypes = append(f.relTypes, strings.Split(strings.TrimPrefix(a, "--type="), ",")...)
		case a == "--max-hops" && i+1 < len(args):
			f.maxHops, _ = strconv.Atoi(args[i+1])
			i++
		case strings.HasPrefix(a, "--max-hops="):
			f.maxHops, _ = strconv.Atoi(strings.TrimPrefix(a, "--max-hops="))
		case a == "--max-results" && i+1 < len(args):
			f.maxResults, _ = strconv.Atoi(args[i+1])
			i++
		case strings.HasPrefix(a, "--max-results="):
			f.maxResults, _ = strconv.Atoi(strings.TrimPrefix(a, "--max-results="))
		case a == "--include-long-query":
			f.includeLongQuery = true
		case a == "--query-max-hops" && i+1 < len(args):
			f.queryMaxHops, _ = strconv.Atoi(args[i+1])
			i++
		case strings.HasPrefix(a, "--query-max-hops="):
			f.queryMaxHops, _ = strconv.Atoi(strings.TrimPrefix(a, "--query-max-hops="))
		case a == "--write-max-hops" && i+1 < len(args):
			f.writeMaxHops, _ = strconv.Atoi(args[i+1])
			i++
		case strings.HasPrefix(a, "--write-max-hops="):
			f.writeMaxHops, _ = strconv.Atoi(strings.TrimPrefix(a, "--write-max-hops="))
		case a == "--read-max-hops" && i+1 < len(args):
			f.readMaxHops, _ = strconv.Atoi(args[i+1])
			i++
		case strings.HasPrefix(a, "--read-max-hops="):
			f.readMaxHops, _ = strconv.Atoi(strings.TrimPrefix(a, "--read-max-hops="))
		case a == "--memory" && i+1 < len(args):
			f.memory = args[i+1]
			i++
		case strings.HasPrefix(a, "--memory="):
			f.memory = strings.TrimPrefix(a, "--memory=")
		case a == "--json":
			f.json = true
		case a == "--compact":
			f.compact = true
		case a == "--format" && i+1 < len(args):
			f.format = args[i+1]
			i++
		case strings.HasPrefix(a, "--format="):
			f.format = strings.TrimPrefix(a, "--format=")
		case strings.HasPrefix(a, "-"):

		default:
			f.positional = append(f.positional, a)
		}
	}
	return f
}
