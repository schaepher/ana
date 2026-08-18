package action

import (
	"strings"

	"github.com/schaepher/codeintel/internal/domain"
	"go.uber.org/zap"
)

// Callers 返回调用 id 的边（深度 ≤ depth，置信度 ≥ MinConfidence）。
func (a *Actions) Callers(id domain.CanonicalID, depth int) ([]*domain.Fact, error) {
	logger := zap.L()
	logger.Info("enter (Actions).Callers", zap.String("id", string(id)), zap.Int("depth", depth))
	defer logger.Info("exit (Actions).Callers")
	return a.repo.GetCallers(id, depth, MinConfidence)
}

// Callees 返回 id 调用的边（深度 ≤ depth）。
func (a *Actions) Callees(id domain.CanonicalID, depth int) ([]*domain.Fact, error) {
	logger := zap.L()
	logger.Info("enter (Actions).Callees", zap.String("id", string(id)), zap.Int("depth", depth))
	defer logger.Info("exit (Actions).Callees")
	return a.repo.GetCallees(id, depth, MinConfidence)
}

// Impact 返回变更影响范围（深度 ≤ depth）。
func (a *Actions) Impact(id domain.CanonicalID, depth int) ([]*domain.CodeEntity, error) {
	logger := zap.L()
	logger.Info("enter (Actions).Impact", zap.String("id", string(id)), zap.Int("depth", depth))
	defer logger.Info("exit (Actions).Impact")
	return a.repo.GetImpact(id, depth)
}

// FunctionFields 解析函数并返回其字段读写摘要（S1，field_trace.md §6.2）。
func (a *Actions) FunctionFields(input string) (*domain.CodeEntity, []*domain.FunctionFieldSummary, error) {
	logger := zap.L()
	logger.Info("enter (Actions).FunctionFields", zap.String("input", input))
	defer logger.Info("exit (Actions).FunctionFields")
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
	logger := zap.L()
	logger.Info("enter (Actions).Table", zap.String("table", table))
	defer logger.Info("exit (Actions).Table")
	return a.repo.GetTableColumns(table)
}

// Relations 表间关联分析（query relations）：表名 → 沿数据流链关联
// 的其他表.列（代码层推断，无外键依赖）。memoryMode：--memory 参数
// （""=auto/full/sql，见 repo.GetTableRelations）。
func (a *Actions) Relations(table, memoryMode string) ([]*domain.TableRelation, error) {
	logger := zap.L()
	logger.Info("enter (Actions).Relations", zap.String("table", table), zap.String("memory_mode", memoryMode))
	defer logger.Info("exit (Actions).Relations")
	return a.repo.GetTableRelations(table, memoryMode)
}

// RelationsAll 全库表间关联聚合（query relations --all / export relations，Q160）：
// 一次遍历全部表返回所有表对关联（合并去重）。memoryMode 同 Relations。
func (a *Actions) RelationsAll(memoryMode string) ([]*domain.TableRelation, error) {
	logger := zap.L()
	logger.Info("enter (Actions).RelationsAll", zap.String("memory_mode", memoryMode))
	defer logger.Info("exit (Actions).RelationsAll")
	return a.repo.GetAllTableRelations(memoryMode)
}

// SetRelationHops 配置三类关系的跳数上限（--query-max-hops 等，Q197）：
// 0 = 不限制（--include-long-query 即 query 上限 0）；透传 repo。
func (a *Actions) SetRelationHops(h domain.RelationHops) {
	logger := zap.L()
	logger.Info("enter (Actions).SetRelationHops", zap.Any("hops", h))
	defer logger.Info("exit (Actions).SetRelationHops")
	type setter interface{ SetRelationHops(domain.RelationHops) }
	if s, ok := a.repo.(setter); ok {
		s.SetRelationHops(h)
	}
}

// ER 数据库 ER 图数据（/api/er）：全库外部表 + 各表列清单 + 表间关联。
// 列按表名聚合（列名 "users.name" → 表 users）；关系三级置信度
// （query 键关联高置信 / write 同源中置信 / read 间接低置信）。
func (a *Actions) ER() (*domain.ERData, error) {
	logger := zap.L()
	logger.Info("enter (Actions).ER")
	defer logger.Info("exit (Actions).ER")
	rels, err := a.repo.GetAllTableRelations("")
	if err != nil {
		return nil, err
	}
	tables, err := a.ertables()
	if err != nil {
		return nil, err
	}
	return &domain.ERData{Tables: tables, Relations: rels}, nil
}

// ERTables 仅表清单（Q209：首次加载不查关联——避免无缓存时全库 BFS
// 秒级阻塞；展开/全图画线时才请求完整 ER()）。relations 返回空数组。
func (a *Actions) ERTables() (*domain.ERData, error) {
	logger := zap.L()
	logger.Info("enter (Actions).ERTables")
	defer logger.Info("exit (Actions).ERTables")
	tables, err := a.ertables()
	if err != nil {
		return nil, err
	}
	return &domain.ERData{Tables: tables, Relations: []*domain.TableRelation{}}, nil
}

// ERTable 单表关系（Q210：双击展开按需加载——只 BFS 该表起点 + 单表
// 缓存，不全量）；表清单仍返回全部（前端首次已加载）。
func (a *Actions) ERTable(table string) (*domain.ERData, error) {
	logger := zap.L()
	logger.Info("enter (Actions).ERTable", zap.String("table", table))
	defer logger.Info("exit (Actions).ERTable")
	rels, err := a.repo.GetTableRelations(table, "")
	if err != nil {
		return nil, err
	}
	tables, err := a.ertables()
	if err != nil {
		return nil, err
	}
	return &domain.ERData{Tables: tables, Relations: rels}, nil
}

// ertables 表清单组装（ER/ERTables 共用）。
func (a *Actions) ertables() ([]domain.ERTable, error) {
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
	return tables, nil
}

// Counts 返回节点数与边数（构建健康检查，serve 启动校验用）。
func (a *Actions) Counts() (nodes, edges int, err error) {
	logger := zap.L()
	logger.Info("enter (Actions).Counts")
	defer logger.Info("exit (Actions).Counts")
	return a.repo.Counts()
}

// Latest 返回最近一次构建元数据（serve 启动校验用）。
func (a *Actions) Latest() (*domain.BuildMeta, error) {
	logger := zap.L()
	logger.Info("enter (Actions).Latest")
	defer logger.Info("exit (Actions).Latest")
	return a.repo.GetLatest()
}

// IndirectWriteSites 返回函数的 INDIRECT_WRITE 边（Q90 调用点回连：
// metadata 含 call_line / call_args，fields 展示用）。
func (a *Actions) IndirectWriteSites(funcID domain.CanonicalID) ([]*domain.Fact, error) {
	logger := zap.L()
	logger.Info("enter (Actions).IndirectWriteSites", zap.String("func_id", string(funcID)))
	defer logger.Info("exit (Actions).IndirectWriteSites")
	return a.repo.GetIndirectWriteEdges(funcID)
}

// DispatchCandidates 返回接口类型的候选实现（Q95：symbol 详情展示）。
func (a *Actions) DispatchCandidates(ifaceID domain.CanonicalID) ([]*domain.Fact, error) {
	logger := zap.L()
	logger.Info("enter (Actions).DispatchCandidates", zap.String("iface_id", string(ifaceID)))
	defer logger.Info("exit (Actions).DispatchCandidates")
	return a.repo.GetDispatchEdges(ifaceID)
}
