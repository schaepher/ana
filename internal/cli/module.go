package cli

import (
	"os"
	"fmt"
	"sort"
	"strings"

	"github.com/schaepher/codeintel/internal/action"
)

// queryModuleCalls 模块间调用表（field_trace.md §18.4）：
// query module-calls [--module <name>] [--json]
func queryModuleCalls(acts *action.Actions, module string, opts outputOpts) int {
	calls, err := acts.ModuleCalls(module)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if opts.json {
		encodeJSON(map[string]any{"calls": calls})
		return 0
	}
	if len(calls) == 0 {
		fmt.Println("无模块间 gRPC 调用")
		return 0
	}
	fmt.Printf("模块间 gRPC 调用（%d）:\n", len(calls))
	for _, c := range calls {
		to := c.ToModule
		if to == "" {
			to = "[外部服务]"
		}
		caller := shortFuncName(c.Caller)
		loc := ""
		if c.Line > 0 {
			loc = fmt.Sprintf(" :%d", c.Line)
		}
		fmt.Printf("  %s → %s: %s.%s  (%s%s)\n", c.FromModule, to, c.Service, c.Method, caller, loc)
	}
	return 0
}

// renderModulesMermaid 模块调用图 mermaid（export graph --type modules）。
func renderModulesMermaid(calls []action.ModuleCall) string {
	var sb strings.Builder
	sb.WriteString("flowchart LR\n")
	// 模块集合（含外部服务）
	mods := map[string]bool{}
	for _, c := range calls {
		mods[c.FromModule] = true
		if c.ToModule != "" {
			mods[c.ToModule] = true
		}
	}
	var names []string
	for m := range mods {
		names = append(names, m)
	}
	sort.Strings(names)
	for _, m := range names {
		sb.WriteString(fmt.Sprintf("  %q[%s]\n", m, m))
	}
	// 边（模块对聚合，标注服务.方法计数）
	type edgeKey struct{ from, to string }
	edges := map[edgeKey][]string{}
	for _, c := range calls {
		to := c.ToModule
		if to == "" {
			to = "[外部服务]"
		}
		k := edgeKey{c.FromModule, to}
		edges[k] = append(edges[k], c.Service+"."+c.Method)
	}
	for k, svcs := range edges {
		// 去重服务标注
		seen := map[string]bool{}
		var labels []string
		for _, svc := range svcs {
			if !seen[svc] {
				seen[svc] = true
				labels = append(labels, svc)
			}
		}
		label := strings.Join(labels, "<br/>")
		sb.WriteString(fmt.Sprintf("  %q -->|%s| %q\n", k.from, label, k.to))
	}
	return sb.String()
}
