package cli

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/schaepher/codeintel/internal/action"
	"github.com/schaepher/codeintel/internal/domain"
)

// queryPath 节点间最短路径（field_trace.md §17.3）：
// query path <from> <to> [--max-depth N] [--kind data|calls] [--json]
func queryPath(acts *action.Actions, from, to string, f queryFlags) int {
	viaCalls := false
	if f.format == "calls" {
		viaCalls = true
	}
	maxDepth := f.maxDepth
	if maxDepth <= 0 {
		maxDepth = 50
	}
	rows, err := acts.Path(from, to, maxDepth, viaCalls)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	kindName := "数据流"
	if viaCalls {
		kindName = "调用"
	}
	if f.json {
		encodeJSON(pathJSON(rows, from, to))
		return 0
	}
	if len(rows) == 0 {
		fmt.Printf("无路径：%s → %s（%s边集下不可达）\n", from, to, kindName)
		return 0
	}
	fmt.Printf("路径（%s边集，%d 步）:\n", kindName, len(rows)-1)
	for i, r := range rows {
		edge := ""
		if i > 0 {
			edge = " ← " + r.EdgeKinds
		}
		loc := ""
		if r.Line > 0 {
			loc = fmt.Sprintf(" :%d", r.Line)
		}
		fmt.Printf("  %s%s%s\n", r.Name, edge, loc)
	}
	return 0
}

// pathJSON --json 输出（§17.3）：{path, length, reachable}。
func pathJSON(rows []*domain.TraceRow, from, to string) map[string]any {
	path := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		path = append(path, map[string]any{
			"id":       string(r.ID),
			"name":     r.Name,
			"kind":     string(r.Kind),
			"edgeKind": r.EdgeKinds,
			"line":     r.Line,
		})
	}
	length := len(rows) - 1
	if length < 0 {
		length = 0
	}
	return map[string]any{
		"from":      from,
		"to":        to,
		"path":      path,
		"length":    length,
		"reachable": len(rows) > 0,
	}
}

// runGitDiffSince 执行 git diff --unified=0 <ref> 并解析为 SinceInfo。
func runGitDiffSince(repoAbs, ref string) *domain.SinceInfo {
	out, err := exec.Command("git", "-C", repoAbs, "diff", "--unified=0", ref).Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: git diff %s: %v（--since 标注跳过）\n", ref, err)
		return nil
	}
	return diffToSinceInfo(ref, parseGitDiff(string(out)))
}

var _ = os.Stdout
