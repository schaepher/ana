package action

import (
	"strings"

	"github.com/schaepher/codeintel/internal/domain"
)

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

	targets, terr := a.repo.GetDispatchTargets()
	if terr == nil && len(targets) > 0 {
		for _, s := range rows {
			for _, o := range s.Origins {
				if m, ok := targets[o.CalleeID]; ok {
					o.Origin = m.Origin
					o.Confidence = m.Confidence
				}
			}
		}
	}
	return n, rows, nil
}

// ValueTrace 数据值全链追踪（field_trace.md §14.2）。
// Table 表级数据流聚合（query table）：表名 → 列虚拟节点 + 写入方。
func (a *Actions) Table(table string) ([]*domain.TableColumn, error) {
	return a.repo.GetTableColumns(table)
}

// Relations 表间关联分析（query relations）：表名 → 沿数据流链关联
// 的其他表.列（代码层推断，无外键依赖）。memoryMode：--memory 参数
// （""=auto/full/sql，见 repo.GetTableRelations）。
func (a *Actions) Relations(table, memoryMode string) ([]*domain.TableRelation, error) {
	return a.repo.GetTableRelations(table, memoryMode)
}

// RelationsAll 全库表间关联聚合（query relations --all / export relations，Q160）：
// 一次遍历全部表返回所有表对关联（合并去重）。memoryMode 同 Relations。
func (a *Actions) RelationsAll(memoryMode string) ([]*domain.TableRelation, error) {
	return a.repo.GetAllTableRelations(memoryMode)
}

// IncludeLongQuery 开启 query 长链展示（--include-long-query，Q196）：
// 透传给 repo 的 relations 降噪——默认 query 同样受 MaxRelationHops 限制。
func (a *Actions) IncludeLongQuery(v bool) {
	type setter interface{ SetIncludeLongQuery(bool) }
	if s, ok := a.repo.(setter); ok {
		s.SetIncludeLongQuery(v)
	}
}

// ER 数据库 ER 图数据（/api/er）：全库外部表 + 各表列清单 + 表间关联。
// 列按表名聚合（列名 "users.name" → 表 users）；关系三级置信度
// （query 键关联高置信 / write 同源中置信 / read 间接低置信）。
func (a *Actions) ER() (*domain.ERData, error) {
	rels, err := a.repo.GetAllTableRelations("")
	if err != nil {
		return nil, err
	}
	cols, err := a.repo.GetAllTableColumns()
	if err != nil {
		return nil, err
	}
	byTable := map[string][]domain.TableColumn{}
	var tableOrder []string
	for _, c := range cols {
		t := c.Name
		if i := strings.Index(c.Name, "."); i > 0 {
			t = c.Name[:i]
		}
		if _, ok := byTable[t]; !ok {
			tableOrder = append(tableOrder, t)
		}
		byTable[t] = append(byTable[t], *c)
	}
	tables := make([]domain.ERTable, 0, len(tableOrder))
	for _, t := range tableOrder {
		tables = append(tables, domain.ERTable{Name: t, Columns: byTable[t]})
	}
	return &domain.ERData{Tables: tables, Relations: rels}, nil
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
