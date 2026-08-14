package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
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
	json    bool // 结构化 JSON 输出（stdout 仅 JSON，日志已切文件）
	compact bool // 树形/表格输出压缩为紧凑形式
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

	// 手动解析 flags（flag 包遇到位置参数即停止，无法支持 "query symbol X --repo Y" 形式）
	f := parseQueryFlags(rest)
	target := ""
	if sub != "unused" {
		if len(f.positional) < 1 {
			fmt.Fprintf(os.Stderr, "error: 缺少符号参数\n")
			return 2
		}
		target = f.positional[0]
	}

	abs, _, err := resolveRepo(f.repoPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	// 日志切换到 .codeintel/codeintel.log（stdout 只留查询结果，Q88）
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

	opts := outputOpts{json: f.json, compact: f.compact}
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
		return queryTraceDir(acts, target, f.funcPath, f.maxDepth, sub == "trace-forward", opts)
	case "value-trace":
		return queryValueTrace(acts, target, f.maxDepth, opts)
	case "summary":
		return querySummary(acts, target, opts, f.format)
	case "unused":
		return queryUnused(acts, abs, f)
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

// queryFlags 是 query 子命令的手动解析结果。
type queryFlags struct {
	repoPath   string
	depth      int
	maxDepth   int
	funcPath   string
	positional []string
	json       bool
	compact    bool
	format     string // summary 的 mermaid 输出（Q100）
	since      string // unused 的 --since <ref>（git diff 区间）
	failOn     string // unused 的 --fail-on unused|isolated（CI 退出码）
}

// parseQueryFlags 手动解析 query 子命令的参数，支持 flags 与位置参数任意顺序。
func parseQueryFlags(args []string) queryFlags {
	logger := zap.L()
	logger.Debug("enter parseQueryFlags")
	defer logger.Debug("exit parseQueryFlags")
	f := queryFlags{repoPath: "."}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--repo" && i+1 < len(args):
			f.repoPath = args[i+1]
			i++
		case strings.HasPrefix(a, "--repo="):
			f.repoPath = strings.TrimPrefix(a, "--repo=")
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
			// 未知 flag：忽略
		default:
			f.positional = append(f.positional, a)
		}
	}
	return f
}

// queryFields 输出函数的字段读写摘要（S1，field_trace.md §6.2），
// 按 direct_read / direct_write / indirect_write 分组。
func queryFields(acts *action.Actions, input string, opts outputOpts, since *domain.SinceInfo) int {
	logger := zap.L()
	logger.Debug("enter queryFields")
	defer logger.Debug("exit queryFields")
	n, rows, err := acts.FunctionFields(input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if opts.json {
		type fieldRow struct {
			AccessKind   string `json:"access_kind"`
			FieldPath    string `json:"field_path"`
			InstancePath string `json:"instance_path"`
			Line         int    `json:"line"`
			CodeSnippet  string `json:"code_snippet"`
		}
		jrows := make([]fieldRow, 0, len(rows))
		for _, r := range rows {
			jrows = append(jrows, fieldRow{r.AccessKind, r.FieldPath, r.InstancePath, r.LineStart, r.CodeSnippet})
		}
		encodeJSON(map[string]any{"name": n.Name, "rows": jrows})
		return 0
	}
	fmt.Printf("字段读写（%s%s）:\n", n.Name, sinceFlag(n, since))
	if len(rows) == 0 {
		fmt.Println("  无字段访问（SSA 字段追溯未产出，或该函数无字段读写）")
		return 0
	}
	groups := map[string][]*domain.FunctionFieldSummary{
		domain.SummaryDirectRead:    nil,
		domain.SummaryDirectWrite:   nil,
		domain.SummaryIndirectWrite: nil,
	}
	for _, r := range rows {
		groups[r.AccessKind] = append(groups[r.AccessKind], r)
	}
	for _, kind := range []string{domain.SummaryDirectRead, domain.SummaryDirectWrite, domain.SummaryIndirectWrite} {
		items := groups[kind]
		if len(items) == 0 {
			continue
		}
		fmt.Printf("  [%s] %d 个字段\n", kind, len(items))
		// 调用点级回连（Q90）：间接写展示调用位置与实参（INDIRECT_WRITE 边 metadata）
		if kind == domain.SummaryIndirectWrite && !opts.json {
			if sites, err := acts.IndirectWriteSites(n.ID); err == nil {
				for _, f := range sites {
					line, _ := f.Metadata["call_line"].(float64)
					args, _ := f.Metadata["call_args"].(string)
					callee := shortFuncName(string(f.TargetID))
					fmt.Printf("    调用点: :%d %s(%s)\n", int(line), callee, args)
				}
			}
		}
		for _, it := range items {
			line := ""
			if it.LineStart > 0 {
				line = fmt.Sprintf(":%d", it.LineStart)
			}
			fmt.Printf("    %-60s %-24s %-6s %s\n",
				it.FieldPath, it.InstancePath, line, it.CodeSnippet)
		}
	}
	return 0
}

// queryTraceDir 输出字段追溯路径（S2/S3，field_trace.md §6.3/6.4）。
// 树形渲染：缩进 + 边类型 + 节点名 + (行号)（Q28）；--compact 去缩进。
func queryTraceDir(acts *action.Actions, field, funcPath string, maxDepth int, forward bool, opts outputOpts) int {
	logger := zap.L()
	logger.Debug("enter queryTraceDir")
	defer logger.Debug("exit queryTraceDir")
	if funcPath == "" {
		fmt.Fprintln(os.Stderr, "error: trace 需要 --func <函数>（canonical ID 或名称）")
		return 2
	}
	n, rows, err := acts.Trace(action.TraceParams{Field: field, Func: funcPath, MaxDepth: maxDepth, Forward: forward})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	// 路径条件标注（Q92 查询期计算）
	rows, err = acts.TraceConditions(rows)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: 条件标注: %v\n", err)
	}
	if opts.json {
		type traceRow struct {
			ID         string   `json:"id"`
			Depth      int      `json:"depth"`
			Name       string   `json:"name"`
			Edge       string   `json:"edge"`
			Line       int      `json:"line"`
			IsUsage    bool     `json:"is_usage"`
			Conditions []string `json:"conditions,omitempty"`
		}
		jrows := make([]traceRow, 0, len(rows))
		for _, r := range rows {
			jrows = append(jrows, traceRow{string(r.ID), r.Depth, r.Name, lastEdgeKind(r.EdgeKinds), r.Line, r.IsUsage, r.Conditions})
		}
		encodeJSON(map[string]any{"field": field, "func": n.Name, "rows": jrows})
		return 0
	}
	if len(rows) == 0 {
		fmt.Printf("无追溯路径：%s 在 %s 中无匹配的字段访问点（--max-depth %d）\n",
			field, n.Name, maxDepth)
		return 0
	}
	direction := "←"
	title := "产生点（反向追溯）"
	if forward {
		direction = "→"
		title = "使用点（正向追踪）"
	}
	fmt.Printf("%s: %s @ %s\n", title, field, n.Name)
	for _, r := range rows {
		edge := lastEdgeKind(r.EdgeKinds)
		mark := ""
		if forward && r.IsUsage {
			mark = " [使用点]"
		}
		line := ""
		if r.Line > 0 {
			line = fmt.Sprintf(" (%d)", r.Line)
		}
		cond := ""
		if len(r.Conditions) > 0 {
			cond = " [条件: " + strings.Join(r.Conditions, "; ") + "]"
		}
		indent := strings.Repeat("  ", r.Depth)
		if opts.compact {
			indent = ""
		}
		fmt.Printf("%s%s %s %s%s%s%s\n", indent, direction, edge, r.Name, line, mark, cond)
	}
	return 0
}

// lastEdgeKind 取路径上最后一段边类型（进入当前节点的边）。
func lastEdgeKind(kinds string) string {
	if i := strings.LastIndex(kinds, ","); i >= 0 {
		return kinds[i+1:]
	}
	return kinds
}

// sinceFlag 函数/方法节点的 --since 标注（§17.2）：[new]/[mod]/空。
func sinceFlag(n *domain.CodeEntity, since *domain.SinceInfo) string {
	if since == nil || (n.Kind != domain.KindFunction && n.Kind != domain.KindMethod) {
		return ""
	}
	if m := action.MarkSince(n.FilePath, n.LineStart, n.LineEnd, since); m != "" {
		return " [" + m + "]"
	}
	return ""
}

// sinceMarks 对 ID 列表批量计算 --since 标注（callers/callees 邻居用）。
func sinceMarks(acts *action.Actions, ids []domain.CanonicalID, since *domain.SinceInfo) map[string]string {
	out := map[string]string{}
	if since == nil {
		return out
	}
	for _, id := range ids {
		n, err := acts.Symbol(id)
		if err != nil || (n.Kind != domain.KindFunction && n.Kind != domain.KindMethod) {
			continue
		}
		if m := action.MarkSince(n.FilePath, n.LineStart, n.LineEnd, since); m != "" {
			out[string(id)] = m
		}
	}
	return out
}

// querySymbol 输出符号摘要（对齐 TD.md 7.1 explore_symbol 摘要层）。
func querySymbol(acts *action.Actions, input string, opts outputOpts, since *domain.SinceInfo) int {
	logger := zap.L()
	logger.Debug("enter querySymbol")
	defer logger.Debug("exit querySymbol")
	d, err := acts.SymbolDetail(input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	n := d.Node
	// 动态派发候选（Q95）：接口类型 → 候选实现（置信度/注册点）
	var dispatch []*domain.Fact
	if n.Kind == domain.KindInterface {
		dispatch, _ = acts.DispatchCandidates(n.ID)
	}
	if opts.json {
		out := map[string]any{
			"id":      string(n.ID),
			"name":    n.Name,
			"kind":    string(n.Kind),
			"file":    n.FilePath,
			"line":    n.LineStart,
			"signature": n.Signature(),
			"doc":     n.DocComment(),
			"callers": factIDs(d.Callers, "source"),
			"callees": factIDs(d.Callees, "target"),
		}
		if len(dispatch) > 0 {
			cands := make([]map[string]any, 0, len(dispatch))
			for _, f := range dispatch {
				cands = append(cands, map[string]any{
					"id":               string(f.TargetID),
					"method":           f.Metadata["interface_method"],
					"origin":           f.Metadata["origin"],
					"confidence":       f.Metadata["confidence"],
					"register_site":    f.Metadata["register_site"],
				})
			}
			out["candidates"] = cands
		}
		encodeJSON(out)
		return 0
	}
	fmt.Printf("ID:         %s\n", n.ID)
	fmt.Printf("名称:       %s%s\n", n.Name, sinceFlag(n, since))
	fmt.Printf("种类:       %s\n", n.Kind)
	if n.FilePath != "" {
		fmt.Printf("文件:       %s", n.FilePath)
		if n.LineStart > 0 {
			fmt.Printf(":%d", n.LineStart)
		}
		fmt.Println()
	}
	if sig := n.Signature(); sig != "" {
		fmt.Printf("签名:       %s\n", sig)
	}
	if doc := n.DocComment(); doc != "" {
		fmt.Printf("文档:       %s\n", strings.Split(doc, "\n")[0])
	}
	fmt.Printf("调用者数:   %d\n", len(d.Callers))
	fmt.Printf("被调用数:   %d\n", len(d.Callees))

	// 详情层：列出调用者与被调用者（上限 50，TD.md 7.1）
	callerIDs := make([]domain.CanonicalID, 0, len(d.Callers))
	for _, f := range d.Callers {
		callerIDs = append(callerIDs, f.SourceID)
	}
	calleeIDs := make([]domain.CanonicalID, 0, len(d.Callees))
	for _, f := range d.Callees {
		calleeIDs = append(calleeIDs, f.TargetID)
	}
	callerMarks := sinceMarks(acts, callerIDs, since)
	calleeMarks := sinceMarks(acts, calleeIDs, since)
	if len(d.Callers) > 0 {
		fmt.Println("调用者:")
		printFacts(d.Callers, "source", 50, callerMarks)
	}
	if len(d.Callees) > 0 {
		fmt.Println("被调用者:")
		printFacts(d.Callees, "target", 50, calleeMarks)
	}
	// 动态派发候选（Q95）：接口类型的候选实现 + 置信度 + 注册点
	if len(dispatch) > 0 {
		fmt.Printf("候选实现:   %d 个\n", len(dispatch))
		for _, f := range dispatch {
			conf, _ := f.Metadata["confidence"].(float64)
			origin, _ := f.Metadata["origin"].(string)
			method, _ := f.Metadata["interface_method"].(string)
			site, _ := f.Metadata["register_site"].(float64)
			line := ""
			if int(site) > 0 {
				line = fmt.Sprintf(" 注册点:%d", int(site))
			}
			fmt.Printf("    %-40s %s.%s [%s %.1f]%s\n",
				shortID(f.TargetID), shortFuncName(string(f.SourceID)), method, origin, conf, line)
		}
	}
	return 0
}

// queryGraph 输出 callers/callees/impact 查询结果。
func queryGraph(acts *action.Actions, sub, input string, depth int, opts outputOpts, since *domain.SinceInfo) int {
	logger := zap.L()
	logger.Debug("enter queryGraph")
	defer logger.Debug("exit queryGraph")
	n, err := acts.ResolveSymbol(input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if opts.json {
		switch sub {
		case "callers":
			facts, err := acts.Callers(n.ID, depth)
			if err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				return 1
			}
			encodeJSON(map[string]any{"target": input, "rows": factIDs(facts, "source")})
		case "callees":
			facts, err := acts.Callees(n.ID, depth)
			if err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				return 1
			}
			encodeJSON(map[string]any{"target": input, "rows": factIDs(facts, "target")})
		case "impact":
			nodes, err := acts.Impact(n.ID, depth)
			if err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				return 1
			}
			encodeJSON(map[string]any{"target": input, "nodes": nodeBriefs(nodes)})
		}
		return 0
	}

	switch sub {
	case "callers":
		facts, err := acts.Callers(n.ID, depth)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		marks := sinceMarks(acts, factEndpointIDs(facts, "source"), since)
		fmt.Printf("调用者（深度 %d，置信度 ≥%.2f）: %d 个\n", depth, action.MinConfidence, len(facts))
		printFacts(facts, "source", 100, marks)
	case "callees":
		facts, err := acts.Callees(n.ID, depth)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		marks := sinceMarks(acts, factEndpointIDs(facts, "target"), since)
		fmt.Printf("被调用者（深度 %d，置信度 ≥%.2f）: %d 个\n", depth, action.MinConfidence, len(facts))
		printFacts(facts, "target", 100, marks)
	case "impact":
		nodes, err := acts.Impact(n.ID, depth)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		fmt.Printf("影响范围（深度 %d）: %d 个节点\n", depth, len(nodes))
		printNodes(nodes)
	}
	return 0
}

// factEndpointIDs 提取边的端点 ID 列表（--since 标注用）。
func factEndpointIDs(facts []*domain.Fact, endpoint string) []domain.CanonicalID {
	out := make([]domain.CanonicalID, 0, len(facts))
	for _, f := range facts {
		if endpoint == "target" {
			out = append(out, f.TargetID)
		} else {
			out = append(out, f.SourceID)
		}
	}
	return out
}

// factIDs 提取边的端点 ID 列表（endpoint=source/target，JSON 输出用）。
func factIDs(facts []*domain.Fact, endpoint string) []map[string]any {
	out := make([]map[string]any, 0, len(facts))
	for _, f := range facts {
		id := f.SourceID
		if endpoint == "target" {
			id = f.TargetID
		}
		out = append(out, map[string]any{
			"id":         string(id),
			"tool":       f.ToolSource,
			"confidence": f.Confidence,
		})
	}
	return out
}

// nodeBriefs 提取节点摘要（JSON 输出用）。
func nodeBriefs(nodes []*domain.CodeEntity) []map[string]any {
	out := make([]map[string]any, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, map[string]any{
			"id":   string(n.ID),
			"name": n.Name,
			"kind": string(n.Kind),
			"file": n.FilePath,
			"line": n.LineStart,
		})
	}
	return out
}

// printFacts 打印边列表；endpoint 为 "source" 时显示边左端（调用者场景），
// 否则显示右端（被调用者场景）。
func printFacts(facts []*domain.Fact, endpoint string, limit int, marks map[string]string) {
	logger := zap.L()
	logger.Debug("enter printFacts")
	defer logger.Debug("exit printFacts")
	truncated := len(facts) > limit
	if truncated {
		facts = facts[:limit]
	}
	for _, f := range facts {
		id := f.SourceID
		if endpoint == "target" {
			id = f.TargetID
		}
		flag := ""
		if m, ok := marks[string(id)]; ok {
			flag = " [" + m + "]"
		}
		fmt.Printf("  %s%s  (%s, conf=%.2f)\n", shortID(id), flag, f.ToolSource, f.Confidence)
	}
	if truncated {
		fmt.Printf("  ...（已截断，共 %d 条）\n", len(facts)+1)
	}
}

// shortID 压缩 canonical ID 显示：保留 pkg 末段与符号名。
func shortID(id domain.CanonicalID) string {
	logger := zap.L()
	logger.Debug("enter shortID")
	defer logger.Debug("exit shortID")
	s := string(id)
	prefix := "symbol:go:"
	if !strings.HasPrefix(s, prefix) {
		return s
	}
	rest := strings.TrimPrefix(s, prefix)
	if i := strings.LastIndex(rest, ":"); i >= 0 {
		pkg := rest[:i]
		name := rest[i+1:]
		if j := strings.LastIndex(pkg, "/"); j >= 0 {
			pkg = pkg[j+1:]
		}
		return pkg + ":" + name
	}
	return rest
}

// printNodes 打印节点列表。
func printNodes(nodes []*domain.CodeEntity) {
	logger := zap.L()
	logger.Debug("enter printNodes")
	defer logger.Debug("exit printNodes")
	sorted := make([]*domain.CodeEntity, len(nodes))
	copy(sorted, nodes)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Kind != sorted[j].Kind {
			return sorted[i].Kind < sorted[j].Kind
		}
		return sorted[i].Name < sorted[j].Name
	})
	for _, n := range sorted {
		loc := ""
		if n.FilePath != "" {
			loc = " " + n.FilePath
			if n.LineStart > 0 {
				loc += fmt.Sprintf(":%d", n.LineStart)
			}
		}
		fmt.Printf("  %s %s%s\n", n.Kind, n.Name, loc)
	}
}

// queryValueTrace 输出数据值在整条链路上的处理过程，按函数上下文分组
// （field_trace.md §14.2 数据值全链追踪）。
func queryValueTrace(acts *action.Actions, nodeID string, maxDepth int, opts outputOpts) int {
	logger := zap.L()
	logger.Debug("enter queryValueTrace")
	defer logger.Debug("exit queryValueTrace")
	rows, err := acts.ValueTrace(domain.CanonicalID(nodeID), maxDepth)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	// 路径条件标注（Q92 查询期计算）：节点所在分支的 if/类型条件
	rows, err = acts.TraceConditions(rows)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: 条件标注: %v\n", err)
	}
	if opts.json {
		type flowRow struct {
			ID         string   `json:"id"`
			Name       string   `json:"name"`
			Depth      int      `json:"depth"`
			Dir        int      `json:"dir"`
			Edge       string   `json:"edge"`
			Line       int      `json:"line"`
			Kind       string   `json:"kind"`
			Access     string   `json:"access"`
			FuncID     string   `json:"func_id"`
			FuncName   string   `json:"func_name"`
			Conditions []string `json:"conditions,omitempty"`
		}
		jrows := make([]flowRow, 0, len(rows))
		for _, r := range rows {
			jrows = append(jrows, flowRow{string(r.ID), r.Name, r.Depth, r.Dir,
				lastEdgeKind(r.EdgeKinds), r.Line, string(r.Kind), r.Access, r.FuncID, shortFuncName(r.FuncID), r.Conditions})
		}
		encodeJSON(map[string]any{"flows": jrows})
		return 0
	}
	if len(rows) == 0 {
		fmt.Println("无数据流（节点不存在或无数据流边）")
		return 0
	}
	fmt.Printf("数据值追踪（%s，--max-depth %d）:\n", nodeID, maxDepth)
	var curFunc string
	for _, r := range rows {
		// 函数上下文分组
		if r.FuncID != curFunc {
			curFunc = r.FuncID
			group := shortFuncName(curFunc)
			if group == "" {
				group = "（未知函数）"
			}
			fmt.Printf("\n【%s】\n", group)
		}
		arrow := "→"
		if r.Dir == 0 {
			arrow = "←"
		}
		edge := lastEdgeKind(r.EdgeKinds)
		acc := ""
		if r.Kind == domain.KindFieldAccess {
			if r.Access == "read" {
				acc = " [读]"
			} else {
				acc = " [写]"
			}
		}
		line := ""
		if r.Line > 0 {
			line = fmt.Sprintf(":%d", r.Line)
		}
		cond := ""
		if len(r.Conditions) > 0 {
			cond = " [条件: " + strings.Join(r.Conditions, "; ") + "]"
		}
		indent := strings.Repeat("  ", r.Depth)
		if opts.compact {
			indent = ""
		}
		fmt.Printf("%s%s %s %s%s%s\n", indent, arrow, edge, r.Name+acc, line, cond)
	}
	return 0
}

// shortFuncName 从函数 canonical ID 提取短名（symbol:go:<pkg>:<name> → <name>）。
func shortFuncName(id string) string {
	if i := strings.LastIndex(id, ":"); i >= 0 {
		return id[i+1:]
	}
	return id
}

// querySummary 跨层摘要（Q100）：字段生命周期主链
// （入口 → 计算 → 写入 → 消费），每步带 file:line。
func querySummary(acts *action.Actions, input string, opts outputOpts, format string) int {
	logger := zap.L()
	logger.Debug("enter querySummary")
	defer logger.Debug("exit querySummary")
	// 锚点：节点 ID 直连、符号名称解析、类型限定字段路径回退到
	// 同字段读节点（③：字段路径不再误报"不存在的符号"）
	anchor, err := acts.ResolveAnchor(input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	steps, err := acts.SummaryChain(anchor)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if len(steps) == 0 {
		fmt.Println("无生命周期链路（节点不存在或无数据流边）")
		return 0
	}
	if opts.json {
		encodeJSON(map[string]any{"steps": steps})
		return 0
	}
	if format == "mermaid" {
		var sb strings.Builder
		sb.WriteString("flowchart LR\n")
		for _, st := range steps {
			label := st.Name
			if st.Line > 0 {
				label += fmt.Sprintf(":%d", st.Line)
			}
			sb.WriteString(fmt.Sprintf("    %q --> %q\n", st.Func+"|"+label, st.Kind+"|"+st.Name))
		}
		fmt.Println(sb.String())
		return 0
	}
	fmt.Printf("字段生命周期: %s（%d 步）\n", steps[0].Name, len(steps))
	for _, st := range steps {
		loc := ""
		if st.File != "" {
			loc = st.File
			if st.Line > 0 {
				loc += fmt.Sprintf(":%d", st.Line)
			}
		}
		fmt.Printf("  %-8s %-40s %s %s\n", "["+st.Kind+"]", st.Name, loc, st.Func)
	}
	return 0
}
