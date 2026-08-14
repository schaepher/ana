// Package action 是 CLI 与 HTTP 共享的查询用例层（应用层）：
// 命令行和 HTTP 接口本身只负责参数解析与结果展示，全部图查询
// 经此层调用仓储。依赖方向：action → Reader 窄接口 ← *sqlite.Repo。
package action

import (
	"errors"
	"fmt"
	"strings"

	"github.com/schaepher/codeintel/internal/domain"
)

// MinConfidence 调用关系查询默认置信度阈值（业务规则，TD.md 5.1：
// CALLS 边置信度 0.8；0.85 会过滤掉全部调用边）。
const MinConfidence = 0.8

// Reader 是 action 层依赖的仓储窄接口（*sqlite.Repo 实现）。
type Reader interface {
	GetSymbol(id domain.CanonicalID) (*domain.CodeEntity, error)
	GetSymbolByName(name string) ([]*domain.CodeEntity, error)
	GetCallers(id domain.CanonicalID, depth int, minConfidence float64) ([]*domain.Fact, error)
	GetCallees(id domain.CanonicalID, depth int, minConfidence float64) ([]*domain.Fact, error)
	GetImpact(id domain.CanonicalID, depth int) ([]*domain.CodeEntity, error)
	GetFunctionFields(funcID domain.CanonicalID) ([]*domain.FunctionFieldSummary, error)
	TraceBackward(field string, funcID domain.CanonicalID, maxDepth int) ([]*domain.TraceRow, error)
	TraceForward(field string, funcID domain.CanonicalID, maxDepth int) ([]*domain.TraceRow, error)
	GetValueTrace(nodeID domain.CanonicalID, maxDepth int) ([]*domain.TraceRow, error)
	GetValueTraceMulti(anchors []domain.CanonicalID, ctxField string, maxDepth int) ([]*domain.TraceRow, error)
	GetFunctionFlows(funcID domain.CanonicalID, maxDepth int) ([]*domain.TraceRow, error)
	GetRoots() ([]*domain.CodeEntity, error)
	Expand(id domain.CanonicalID) (facts []*domain.Fact, nodes []*domain.CodeEntity, err error)
	AllSummaries() ([]*domain.FunctionFieldSummary, error)
	GetIndirectWriteEdges(funcID domain.CanonicalID) ([]*domain.Fact, error)
	GetDispatchEdges(ifaceID domain.CanonicalID) ([]*domain.Fact, error)
	FindFieldReads(fullPath string) ([]*domain.CodeEntity, error)
	Counts() (nodes int, edges int, err error)
	GetLatest() (*domain.BuildMeta, error)
	RepoPath() string
}

// Actions 是 CLI 与 HTTP 共享的查询用例集合。
type Actions struct {
	repo Reader
}

// New 创建 Actions。
func New(repo Reader) *Actions {
	return &Actions{repo: repo}
}

// ResolveSymbol 将用户输入解析为符号：canonical ID 直接命中，否则按名称查找；
// 多匹配时返回错误并列出候选 ID（原 CLI 语义，供符号类 action 复用）。
func (a *Actions) ResolveSymbol(input string) (*domain.CodeEntity, error) {
	if strings.HasPrefix(input, "symbol:") || strings.HasPrefix(input, "file:") || strings.HasPrefix(input, "commit:") {
		n, err := a.repo.GetSymbol(domain.CanonicalID(input))
		if err == nil {
			return n, nil
		}
		if !errors.Is(err, domain.ErrNotFound) {
			return nil, err
		}
	}
	matches, err := a.repo.GetSymbolByName(input)
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("符号 %q 不存在", input)
	}
	if len(matches) > 1 {
		return nil, fmt.Errorf("符号 %q 有 %d 个匹配，请使用 canonical ID:\n  %s",
			input, len(matches), joinIDs(matches))
	}
	return matches[0], nil
}

// joinIDs 拼接候选 ID（多匹配错误提示用）。
func joinIDs(nodes []*domain.CodeEntity) string {
	ids := make([]string, 0, len(nodes))
	for _, n := range nodes {
		ids = append(ids, string(n.ID))
	}
	return strings.Join(ids, "\n  ")
}

// ResolveAnchor 解析摘要/生命周期锚点（③⑤）：canonical ID 直连、符号
// 名称解析，类型限定字段路径（example.com/m.T.A）回退到同字段读节点
// （FindFieldReads 首个）——此前字段路径被误报"不存在的符号"。
func (a *Actions) ResolveAnchor(input string) (domain.CanonicalID, error) {
	if strings.HasPrefix(input, "symbol:") || strings.HasPrefix(input, "file:") || strings.HasPrefix(input, "commit:") {
		if _, err := a.repo.GetSymbol(domain.CanonicalID(input)); err == nil {
			return domain.CanonicalID(input), nil
		}
	}
	if n, err := a.ResolveSymbol(input); err == nil {
		return n.ID, nil
	}
	if reads, err := a.repo.FindFieldReads(input); err == nil && len(reads) > 0 {
		return reads[0].ID, nil
	}
	return "", fmt.Errorf("符号或字段路径 %q 不存在", input)
}

// 写锚点跳板限界（④ 超时防护）：读节点数上限 + 子追溯深度——避免
// 同字段大量读节点各自跑一遍深度 8 双向全链。
const (
	trampolineMaxReads = 8
	trampolineDepth    = 4
)

// downstreamTrampoline 写锚点下游跳板（③⑤⑧）：写节点无出边——经同
// full_path 读节点接入使用链（读节点行 + dir=1 子链行）；非写锚点返回 nil。
// 读锚点的下游用单次合并 CTE 查询（GetValueTraceMulti），替代逐读节点
// 递归——读点多时累计成本大幅下降（⑧ 超时防护）。
func (a *Actions) downstreamTrampoline(anchor domain.CanonicalID) ([]*domain.TraceRow, error) {
	n, err := a.repo.GetSymbol(anchor)
	if err != nil || n.Kind != domain.KindFieldAccess || n.Property("access_kind") != "write" {
		return nil, nil
	}
	fullPath := n.Property("full_path")
	if fullPath == "" {
		return nil, nil
	}
	reads, err := a.repo.FindFieldReads(fullPath)
	if err != nil {
		return nil, nil
	}
	anchors := make([]domain.CanonicalID, 0, len(reads))
	var out []*domain.TraceRow
	for _, rn := range reads {
		if rn.ID == anchor {
			continue // 同节点跳过
		}
		anchors = append(anchors, rn.ID)
		// 读节点本身（⑤：生命周期图展示读取点）
		out = append(out, &domain.TraceRow{
			ID: rn.ID, Name: rn.Name, Kind: rn.Kind, Access: "read",
			Line: rn.LineStart, Dir: 1,
			FuncID: rn.Property("func_id"),
		})
		if len(anchors) >= trampolineMaxReads {
			break
		}
	}
	// 读锚点下游合并为单次查询（⑧）
	sub, err := a.repo.GetValueTraceMulti(anchors, fullPath, trampolineDepth)
	if err == nil {
		out = append(out, sub...)
	}
	return out, nil
}

// Lifecycle 生命周期图行（⑤）：value-trace 全链 + 写锚点的下游跳板
// （同字段读节点的使用链），行按 ID 去重（首个保留）。供
// export graph --type lifecycle 与前端展示使用。
func (a *Actions) Lifecycle(id domain.CanonicalID) ([]*domain.TraceRow, error) {
	rows, err := a.repo.GetValueTrace(id, 8)
	if err != nil {
		return nil, err
	}
	extra, err := a.downstreamTrampoline(id)
	if err != nil {
		return nil, err
	}
	seen := map[domain.CanonicalID]bool{}
	out := make([]*domain.TraceRow, 0, len(rows)+len(extra))
	for _, r := range append(rows, extra...) {
		if seen[r.ID] {
			continue
		}
		seen[r.ID] = true
		out = append(out, r)
	}
	return out, nil
}

// Symbol 按 canonical ID 查询符号（HTTP expand 的存在性检查用）。
func (a *Actions) Symbol(id domain.CanonicalID) (*domain.CodeEntity, error) {
	return a.repo.GetSymbol(id)
}

// SymbolDetail 符号详情：基本信息 + 调用者/被调用者摘要（query symbol）。
type SymbolDetail struct {
	Node    *domain.CodeEntity
	Callers []*domain.Fact
	Callees []*domain.Fact
}

// SymbolDetail 解析符号并返回其详情（调用者/被调用者深度 1）。
func (a *Actions) SymbolDetail(input string) (*SymbolDetail, error) {
	n, err := a.ResolveSymbol(input)
	if err != nil {
		return nil, err
	}
	callers, err := a.repo.GetCallers(n.ID, 1, MinConfidence)
	if err != nil {
		return nil, err
	}
	callees, err := a.repo.GetCallees(n.ID, 1, MinConfidence)
	if err != nil {
		return nil, err
	}
	return &SymbolDetail{Node: n, Callers: callers, Callees: callees}, nil
}

// Callers 返回调用 id 的边（深度 ≤ depth，置信度 ≥ MinConfidence）。
func (a *Actions) Callers(id domain.CanonicalID, depth int) ([]*domain.Fact, error) {
	return a.repo.GetCallers(id, depth, MinConfidence)
}

// Callees 返回 id 调用的边（深度 ≤ depth）。
func (a *Actions) Callees(id domain.CanonicalID, depth int) ([]*domain.Fact, error) {
	return a.repo.GetCallees(id, depth, MinConfidence)
}

// Impact 返回变更影响范围（深度 ≤ depth）。
func (a *Actions) Impact(id domain.CanonicalID, depth int) ([]*domain.CodeEntity, error) {
	return a.repo.GetImpact(id, depth)
}

// FunctionFields 解析函数并返回其字段读写摘要（S1，field_trace.md §6.2）。
func (a *Actions) FunctionFields(input string) (*domain.CodeEntity, []*domain.FunctionFieldSummary, error) {
	n, err := a.ResolveSymbol(input)
	if err != nil {
		return nil, nil, err
	}
	rows, err := a.repo.GetFunctionFields(n.ID)
	if err != nil {
		return nil, nil, err
	}
	return n, rows, nil
}

// TraceParams 字段追溯参数（S2/S3）。
type TraceParams struct {
	Field    string
	Func     string // 函数符号输入（canonical ID 或名称）
	MaxDepth int
	Forward  bool // true=trace-forward（S3 后续使用），false=trace-backward（S2 产生点）
}

// Trace 字段产生点反向追溯 / 后续使用正向追踪；返回解析后的函数符号
// 与追溯路径（符号供展示层输出函数名）。
func (a *Actions) Trace(p TraceParams) (*domain.CodeEntity, []*domain.TraceRow, error) {
	n, err := a.ResolveSymbol(p.Func)
	if err != nil {
		return nil, nil, err
	}
	if p.Forward {
		rows, err := a.repo.TraceForward(p.Field, n.ID, p.MaxDepth)
		return n, rows, err
	}
	rows, err := a.repo.TraceBackward(p.Field, n.ID, p.MaxDepth)
	return n, rows, err
}

// ValueTrace 数据值全链追踪（field_trace.md §14.2）。
func (a *Actions) ValueTrace(nodeID domain.CanonicalID, maxDepth int) ([]*domain.TraceRow, error) {
	return a.repo.GetValueTrace(nodeID, maxDepth)
}

// Roots 返回顶层入口节点（前端初始视图）。
func (a *Actions) Roots() ([]*domain.CodeEntity, error) {
	return a.repo.GetRoots()
}

// Search 全库符号搜索（名称/ID 模糊匹配，上限由仓储实现决定）。
func (a *Actions) Search(q string) ([]*domain.CodeEntity, error) {
	return a.repo.GetSymbolByName(q)
}

// Expand 返回节点的直接邻居（facts + 邻居节点）；返回当前节点供存在性检查。
func (a *Actions) Expand(id domain.CanonicalID) (cur *domain.CodeEntity, facts []*domain.Fact, nodes []*domain.CodeEntity, err error) {
	cur, err = a.repo.GetSymbol(id)
	if err != nil {
		return nil, nil, nil, err
	}
	facts, nodes, err = a.repo.Expand(id)
	if err != nil {
		return nil, nil, nil, err
	}
	return cur, facts, nodes, nil
}

// Flows 返回函数内完整字段数据流（前端 /api/flows 用）。
func (a *Actions) Flows(funcID domain.CanonicalID, maxDepth int) ([]*domain.TraceRow, error) {
	return a.repo.GetFunctionFlows(funcID, maxDepth)
}

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

// Counts 返回节点数与边数（构建健康检查，serve 启动校验用）。
func (a *Actions) Counts() (nodes, edges int, err error) {
	return a.repo.Counts()
}

// Latest 返回最近一次构建元数据（serve 启动校验用）。
func (a *Actions) Latest() (*domain.BuildMeta, error) {
	return a.repo.GetLatest()
}

// IndirectWriteSites 返回函数的 INDIRECT_WRITE 边（Q90 调用点回连：
// metadata 含 call_line / call_args，fields 展示用）。
func (a *Actions) IndirectWriteSites(funcID domain.CanonicalID) ([]*domain.Fact, error) {
	return a.repo.GetIndirectWriteEdges(funcID)
}

// DispatchCandidates 返回接口类型的候选实现（Q95：symbol 详情展示）。
func (a *Actions) DispatchCandidates(ifaceID domain.CanonicalID) ([]*domain.Fact, error) {
	return a.repo.GetDispatchEdges(ifaceID)
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
		default: // direct_write / indirect_write 均为产生者
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
	rows, err := a.repo.GetValueTrace(anchor, 8)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	// 每 dir 按 depth 分层取首个（主路径）
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
	producers := pick(0) // depth 递增：锚点 → ... → 源头
	consumers := pick(1) // depth 递增：锚点 → ... → 消费

	// 写锚点：下游经同字段读节点跳板（③）——读节点的使用链并入
	// （限界：最多 8 个读节点 × 深度 4，④ 超时防护）
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
			kind = "entry" // 源头（入口/产生点）
		case strings.HasPrefix(r.Name, "sql.") || strings.HasPrefix(r.Name, "metric"):
			kind = "write"
		case r.Kind == domain.KindFieldAccess && r.Access == "write":
			kind = "write"
		case r.Kind == domain.KindFieldAccess && r.Access == "read":
			kind = "consume" // 下游消费：字段读取点（③ 多分支场景——
			// 同字段多读节点时链末端非唯一，读点即消费）
		case i == len(chain)-1:
			kind = "consume" // 末端兜底
		}
		steps = append(steps, SummaryStep{
			Kind: kind, Name: r.Name, File: fp, Line: r.Line, Func: shortFuncNameX(r.FuncID),
		})
	}
	// 步骤去重（④）：多读节点共享同一下游时，同 Name/File/Line/Func
	// （同一节点）只保留一个——避免 N×跳板的重复噪音。同一节点出现
	// consume/write 与 compute 双分类时，保留语义更强的 consume/write。
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

// pickSub 从子追溯结果按 depth 分层取首个（dir 指定）。
func pickSub(dir int, rows []*domain.TraceRow) []*domain.TraceRow {
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

// shortFuncNameX 从 canonical ID 取函数短名（action 层展示用）。
func shortFuncNameX(id string) string {
	if i := strings.LastIndex(id, ":"); i >= 0 {
		return id[i+1:]
	}
	return id
}
