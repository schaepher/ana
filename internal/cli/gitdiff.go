package cli

import (
	"strings"

	"github.com/schaepher/codeintel/internal/domain"
)

// gitDiffInfo --since 的 diff 解析结果（field_trace.md §16.5）。
type gitDiffInfo struct {
	NewFiles   map[string]bool
	AddedLines map[string]map[int]bool
}

// parseGitDiff 解析 `git diff --unified=0 <ref>` 输出：
//   - new file mode → 新增文件（文件内全部函数 [new]）
//   - deleted file → 跳过
//   - rename（similarity index）→ 按修改处理（新路径）
//   - @@ -a,b +c,d @@ → + 侧新增行号（c..c+d-1，跳过上下文行）
//
// 返回：新增文件集合 + 每文件新增行号集合。
func parseGitDiff(out string) *gitDiffInfo {
	info := &gitDiffInfo{
		NewFiles:   map[string]bool{},
		AddedLines: map[string]map[int]bool{},
	}
	var curFile string
	var curIsNew bool // 当前文件段是否 new file
	var curAdded []int
	inHunk := false
	hunkLine := 0 // + 侧当前行号（hunk 内逐行累加）
	lines := strings.Split(out, "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		switch {
		case strings.HasPrefix(line, "diff --git "):
			// 新文件段开始
			if curFile != "" {
				info.AddedLines[curFile] = toSet(curAdded)
			}
			curFile = ""
			curAdded = nil
			curIsNew = false
			inHunk = false
		case strings.HasPrefix(line, "new file mode"):
			curIsNew = true
		case strings.HasPrefix(line, "deleted file mode"):
			curFile = "-deleted-"
		case strings.HasPrefix(line, "+++ b/"):
			curFile = strings.TrimPrefix(line, "+++ b/")
			if curIsNew {
				info.NewFiles[curFile] = true
				curIsNew = false
			}
		case strings.HasPrefix(line, "@@ "):
			// @@ -a,b +c,d @@：+ 侧起始行号 c；@@ 行尾内联内容为
			// hunk 首行上下文（git 单行格式）
			rest := line[3:]
			if idx := strings.Index(rest, "+"); idx >= 0 {
				rest = rest[idx+1:]
			} else {
				inHunk = false
				continue
			}
			inlineCtx := false
			if idx := strings.Index(rest, "@@"); idx >= 0 {
				inlineCtx = strings.TrimSpace(rest[idx+2:]) != ""
				rest = rest[:idx]
			}
			var c, d int
			if _, err := fmtSscanf(rest, &c, &d); err != nil {
				inHunk = false
				continue
			}
			hunkLine = c
			if inlineCtx {
				hunkLine = c + 1 // 首行上下文已在 @@ 行内
			}
			inHunk = true
		case inHunk:
			switch {
			case strings.HasPrefix(line, "+"):
				// 新增行（+++ 头不会出现在 hunk 内）
				curAdded = append(curAdded, hunkLine)
				hunkLine++
			case strings.HasPrefix(line, " "):
				hunkLine++ // 上下文行：占 + 侧行号
			case strings.HasPrefix(line, "-"), strings.HasPrefix(line, "\\"):
				// 删除行 / 无换行标记：不占 + 侧行号
			default:
				inHunk = false // hunk 结束（空行分隔等）
			}
		}
	}
	if curFile != "" {
		info.AddedLines[curFile] = toSet(curAdded)
	}
	return info
}

// toSet 去重行号。
func toSet(lines []int) map[int]bool {
	out := map[int]bool{}
	for _, l := range lines {
		out[l] = true
	}
	return out
}

// fmtSscanf 简易解析 "@@ -a,b +c,d @@" 的 + 侧 c,d。
func fmtSscanf(rest string, c, d *int) (int, error) {
	// rest 形如 "c,d @@" 或 "c @@"
	rest = strings.TrimSpace(rest)
	comma := strings.Index(rest, ",")
	end := strings.Index(rest, " ")
	if end < 0 {
		end = len(rest)
	}
	numPart := rest
	if comma >= 0 {
		numPart = rest[:comma]
	} else if end >= 0 {
		numPart = rest[:end]
	}
	if numPart == "" {
		return 0, errParseDiff
	}
	n := 0
	for _, ch := range numPart {
		if ch < '0' || ch > '9' {
			return 0, errParseDiff
		}
		n = n*10 + int(ch-'0')
	}
	*c = n
	*d = 1
	if comma >= 0 {
		dpart := rest[comma+1:]
		if e := strings.Index(dpart, " "); e >= 0 {
			dpart = dpart[:e]
		}
		m := 0
		for _, ch := range dpart {
			if ch < '0' || ch > '9' {
				return 0, errParseDiff
			}
			m = m*10 + int(ch-'0')
		}
		*d = m
	}
	return 0, nil
}

// errParseDiff diff 行解析失败。
var errParseDiff = &parseErr{}

type parseErr struct{}

func (*parseErr) Error() string { return "parse diff" }

// diffToSinceInfo 转 domain.SinceInfo（含新增文件标记）。
func diffToSinceInfo(ref string, info *gitDiffInfo) *domain.SinceInfo {
	since := &domain.SinceInfo{
		Ref:        ref,
		NewFiles:   info.NewFiles,
		AddedLines: info.AddedLines,
	}
	return since
}
