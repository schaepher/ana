package cli

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/schaepher/codeintel/internal/action"
	"github.com/schaepher/codeintel/internal/domain"
)

// queryUnused 未调用函数与孤立链分析（field_trace.md §16）：
//   - 无 --since：全量报告（冗余代码检查）
//   - --since <ref>：git diff 区间内新增/修改函数（流程衔接检查）
//   - --fail-on <unused|isolated>：存在未调用函数/孤立链时退出码 1
func queryUnused(acts *action.Actions, repoAbs string, f queryFlags) int {
	var since *domain.SinceInfo
	if f.since != "" {
		out, err := exec.Command("git", "-C", repoAbs, "diff", "--unified=0", f.since).Output()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: git diff %s: %v\n", f.since, err)
			return 1
		}
		since = diffToSinceInfo(f.since, parseGitDiff(string(out)))
	}
	rep, err := acts.Unused(since)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if f.json {
		encodeJSON(unusedJSON(rep))
		return failCode(f.failOn, rep)
	}
	renderUnusedTable(rep)
	return failCode(f.failOn, rep)
}

// failCode --fail-on 判定：unused=存在无调用函数；isolated=存在孤立链。
func failCode(failOn string, rep *action.UnusedReport) int {
	if failOn == "unused" && len(rep.Unused) > 0 {
		return 1
	}
	if failOn == "isolated" && len(rep.Chains) > 0 {
		return 1
	}
	return 0
}

// unusedJSON --json 结构化输出（field_trace.md §16.4）。
func unusedJSON(rep *action.UnusedReport) map[string]any {
	u := make([]map[string]any, 0, len(rep.Unused))
	for _, x := range rep.Unused {
		u = append(u, map[string]any{
			"id":        string(x.ID),
			"name":      x.Name,
			"kind":      string(x.Kind),
			"file":      x.FilePath,
			"line":      x.LineStart,
			"exported":  x.Exported,
			"referenced": x.Referenced,
			"since":     x.SinceMark,
		})
	}
	c := make([][]map[string]any, 0, len(rep.Chains))
	for _, ch := range rep.Chains {
		row := make([]map[string]any, 0, len(ch))
		for _, x := range ch {
			row = append(row, map[string]any{
				"id":   string(x.ID),
				"name": x.Name,
				"file": x.FilePath,
				"line": x.LineStart,
			})
		}
		c = append(c, row)
	}
	out := map[string]any{
		"unused":          u,
		"isolated_chains": c,
	}
	if rep.Since != nil {
		out["since"] = map[string]any{
			"ref": rep.Since.Ref,
			"new_functions": len(rep.Unused),
		}
	}
	return out
}

// renderUnusedTable 表格输出：未调用函数清单 + 孤立链分组。
func renderUnusedTable(rep *action.UnusedReport) {
	if rep.Since != nil {
		fmt.Printf("本次改动未调用函数（--since %s）:\n", rep.Since.Ref)
	} else {
		fmt.Println("未调用函数:")
	}
	if len(rep.Unused) == 0 {
		fmt.Println("  （无）")
	} else {
		sort.Slice(rep.Unused, func(i, j int) bool {
			a, b := rep.Unused[i], rep.Unused[j]
			if a.FilePath != b.FilePath {
				return a.FilePath < b.FilePath
			}
			return a.LineStart < b.LineStart
		})
		for _, u := range rep.Unused {
			flags := []string{}
			if !u.Referenced {
				flags = append(flags, "无引用")
			}
			if u.Exported {
				flags = append(flags, "exported")
			}
			if u.SinceMark != "" {
				flags = append(flags, u.SinceMark)
			}
			flagStr := ""
			if len(flags) > 0 {
				flagStr = " [" + strings.Join(flags, " ") + "]"
			}
			loc := u.FilePath
			if u.LineStart > 0 {
				loc = fmt.Sprintf("%s:%d", u.FilePath, u.LineStart)
			}
			fmt.Printf("  %s%s  %s\n", u.Name, flagStr, loc)
		}
	}
	if len(rep.Chains) == 0 {
		return
	}
	fmt.Printf("\n孤立调用链（%d）:\n", len(rep.Chains))
	for _, ch := range rep.Chains {
		names := make([]string, 0, len(ch))
		for _, u := range ch {
			names = append(names, u.Name)
		}
		head := ch[0]
		mark := ""
		if head.SinceMark != "" {
			mark = " ⚠ 新增函数在孤立链中——需求流程可能未衔接"
		}
		loc := ""
		if head.FilePath != "" && head.LineStart > 0 {
			loc = fmt.Sprintf(" (%s:%d)", head.FilePath, head.LineStart)
		}
		fmt.Printf("  %s%s%s\n", strings.Join(names, " → "), loc, mark)
	}
}

var _ = os.Stdout
