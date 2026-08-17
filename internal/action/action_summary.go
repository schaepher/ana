package action

import (
	"fmt"
	"strings"

	"github.com/schaepher/codeintel/internal/domain"
)

// ExportField 双层索引中的一个字段条目（S4，field_trace.md §2）。
type ExportField struct {
	Producers []ExportEntry `json:"producers"`
	Consumers []ExportEntry `json:"consumers"`
}

// ExportEntry 单个产生者/消费者条目。
type ExportEntry struct {
	Function string `json:"function"`
	Access   string `json:"access,omitempty"` // producers 的写类型（direct/indirect）
	Line     int    `json:"line"`
	Instance string `json:"instance"`
	Code     string `json:"code"`
}

// ExportIndex 生成 字段 → 产生者/消费者 的双层索引（S4）。
// direct_read 为消费者；direct_write / indirect_write 均为产生者。
func (a *Actions) ExportIndex() (map[string]*ExportField, error) {
	rows, err := a.repo.AllSummaries()
	if err != nil {
		return nil, err
	}
	index := map[string]*ExportField{}
	for _, s := range rows {
		ef := index[s.FieldPath]
		if ef == nil {
			ef = &ExportField{Producers: []ExportEntry{}, Consumers: []ExportEntry{}}
			index[s.FieldPath] = ef
		}
		entry := ExportEntry{
			Function: string(s.FunctionID),
			Line:     s.LineStart,
			Instance: s.InstancePath,
			Code:     s.CodeSnippet,
		}
		switch s.AccessKind {
		case domain.SummaryDirectRead:
			ef.Consumers = append(ef.Consumers, entry)
		default:
			entry.Access = s.AccessKind
			ef.Producers = append(ef.Producers, entry)
		}
	}
	return index, nil
}

// SummaryStep 跨层摘要的一步（Q100）。
type SummaryStep struct {
	Kind string `json:"kind"` // entry / compute / write / consume
	Name string `json:"name"`
	File string `json:"file"`
	Line int    `json:"line"`
	Func string `json:"func"`
}

// SummaryChain 提取字段生命周期主链（Q100）：从锚点双向取最长路径
// （产生链到源头 + 使用链到消费），每 depth 层取首个节点（value-trace
// 结果按 dir/depth/id 有序）。步骤类型标注：源头=entry、sql/metric/
// 字段写=write、末端=consume、其余=compute。
// 写锚点的下游（③）：写节点无出边——经"同 full_path 的读节点"跳板
// 接入读的使用链（字段级关联：写入 → 后续读取消费）。
func (a *Actions) SummaryChain(anchor domain.CanonicalID) ([]SummaryStep, error) {
	rows, err := a.repo.GetValueTrace(anchor, 8, 0, false)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}

	pick := func(dir int) []*domain.TraceRow {
		var out []*domain.TraceRow
		maxDepth := -1
		for _, r := range rows {
			if r.Dir != dir {
				continue
			}
			if r.Depth > maxDepth {
				maxDepth = r.Depth
				out = append(out, r)
			}
		}
		return out
	}
	producers := pick(0)
	consumers := pick(1)

	if len(consumers) <= 1 {
		if extra, err := a.downstreamTrampoline(anchor); err == nil {
			consumers = append(consumers, extra...)
		}
	}

	// 主链（正向）：源头 → ... → 锚点 → ... → 消费
	var chain []*domain.TraceRow
	for i := len(producers) - 1; i >= 0; i-- {
		chain = append(chain, producers[i])
	}
	chain = append(chain, consumers...)

	steps := make([]SummaryStep, 0, len(chain))
	fileOf := map[string]string{}
	for i, r := range chain {
		fp, ok := fileOf[string(r.ID)]
		if !ok {
			if n, err := a.repo.GetSymbol(r.ID); err == nil {
				fp = n.FilePath
			}
			fileOf[string(r.ID)] = fp
		}
		kind := "compute"
		switch {
		case i == 0:
			kind = "entry"
		case strings.HasPrefix(r.Name, "sql.") || strings.HasPrefix(r.Name, "metric"):
			kind = "write"
		case r.Kind == domain.KindFieldAccess && r.Access == "write":
			kind = "write"
		case r.Kind == domain.KindFieldAccess && r.Access == "read":
			kind = "consume"

		case i == len(chain)-1:
			kind = "consume"
		}
		steps = append(steps, SummaryStep{
			Kind: kind, Name: r.Name, File: fp, Line: r.Line, Func: shortFuncNameX(r.FuncID),
		})
	}

	idx := map[string]int{}
	dedup := make([]SummaryStep, 0, len(steps))
	for _, st := range steps {
		k := st.Name + "|" + st.File + "|" + fmt.Sprint(st.Line) + "|" + st.Func
		if i, ok := idx[k]; ok {
			if (st.Kind == "consume" || st.Kind == "write") && dedup[i].Kind == "compute" {
				dedup[i] = st
			}
			continue
		}
		idx[k] = len(dedup)
		dedup = append(dedup, st)
	}
	return dedup, nil
}

// UnusedReport 未调用分析报告（field_trace.md §16.4）。
type UnusedReport struct {
	Unused []*domain.UnusedFunc   // 未调用函数（--since 时只含 [new]/[mod]）
	Chains [][]*domain.UnusedFunc // 孤立调用链（按链头分组）
	Since  *domain.SinceInfo
}

// Unused 未调用函数与孤立链分析（field_trace.md §16）：
//   - 未调用 = 无 calls/passes_result 入边（Called=false）
//   - 无引用 = 且无 passes_to/dispatch_to/initializes/var 初始化引用
//   - 孤立链：链头无 caller，链内 caller ⊆ 链，有链外 caller 断开，环整环孤立
//   - --since：标注 [new]（声明行在新增行）/ [mod]（行号区间命中新增行）
//     并只保留标注过的函数（流程衔接检查）；since 为 nil 时全量报告
func (a *Actions) Unused(since *domain.SinceInfo) (*UnusedReport, error) {
	all, err := a.repo.GetUncalledFunctions()
	if err != nil {
		return nil, err
	}
	chains, err := a.repo.GetIsolatedChains()
	if err != nil {
		return nil, err
	}
	rep := &UnusedReport{Since: since}
	for _, u := range all {
		if u.Called {
			continue
		}
		if since != nil {
			u.SinceMark = sinceMark(u, since)
			if u.SinceMark == "" {
				continue
			}
		}
		rep.Unused = append(rep.Unused, u)
	}

	for _, ch := range chains {
		if since != nil {
			keep := false
			for _, u := range ch {
				u.SinceMark = sinceMark(u, since)
				if u.SinceMark != "" {
					keep = true
				}
			}
			if !keep {
				continue
			}
		}
		rep.Chains = append(rep.Chains, ch)
	}
	return rep, nil
}

// MarkSince 标注函数在 --since 中的状态：new（声明行命中 diff 新增行或
// 新增文件）/ mod（行号区间命中新增行）/ ""（未改动）。纯函数，
// UnusedFunc 与 CodeEntity（--since 标注推广，§17.2）共用。
func MarkSince(file string, start, end int, since *domain.SinceInfo) string {
	if since.NewFiles[file] {
		return "new"
	}
	added := since.AddedLines[file]
	if len(added) == 0 {
		return ""
	}
	if added[start] {
		return "new"
	}
	if end < start {
		end = start
	}
	for l := start; l <= end; l++ {
		if added[l] {
			return "mod"
		}
	}
	return ""
}

// sinceMark UnusedFunc 版（--since 标注）。
func sinceMark(u *domain.UnusedFunc, since *domain.SinceInfo) string {
	return MarkSince(u.FilePath, u.LineStart, u.LineEnd, since)
}
