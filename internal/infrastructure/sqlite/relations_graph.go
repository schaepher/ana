package sqlite

// 表关联分析（query relations）P0②：一次加载全库图（边 + 节点元数据）
// 到内存，BFS 纯内存进行——替代原逐节点 SQL 查询。go2o 实测规模
// （节点 ~15 万、边 ~15 万、外部列 ~3.6 千）全内存无压力，单次加载
// ~1-2s，之后全部表 BFS 都在毫秒级。
//
// 关系图结构：
//   - dataAdj：数据流边（data_flows_to/argument/returns/summary_io/
//     alias/phi_operand）双向邻接——BFS 主边
//   - allAdj：全部边双向邻接——循环读出桥（Q152）的 2 跳可达检查
//     （桥 SQL 的 EXISTS 不限定边 kind，须用全量边）
//   - readsByFunc：函数 → 该函数全部 field_access read 节点（桥候选，
//     避免每次 BFS 全表扫描）

// relationsMaxDepth BFS 最大深度（与旧实现一致）。
const relationsMaxDepth = 12

// --memory 模式（P0④）：auto 按规模判断；full 强制内存图；sql 强制
// 逐节点 SQL（大仓库防爆内存的逃生口）。
const (
	relationMemoryAuto = "" // 默认
	relationMemoryFull = "full"
	relationMemorySQL  = "sql"
)

// 内存图安全阈值：节点或边超过时 auto 走 SQL 路径。内存占用估计
// 节点 ×~200B + 边 ×~100B（go2o 15 万节点实测 ~40MB）；50 万节点 +
// 80 万边约 180MB，3.5G 机器安全。
const (
	relationGraphMaxNodes = 500_000
	relationGraphMaxEdges = 800_000
)

// useMemoryGraph 判定是否走内存图路径。mode 显式时跟随；auto 用
// build_metadata 缓存的规模（构建时写入，不每次 COUNT；无元数据时
// 按小库处理走内存路径）。
func (r *Repo) useMemoryGraph(mode string) bool {
	switch mode {
	case relationMemoryFull:
		return true
	case relationMemorySQL:
		return false
	}
	if m, err := r.GetLatest(); err == nil {
		if m.Nodes > relationGraphMaxNodes || m.Edges > relationGraphMaxEdges {
			return false
		}
	}
	return true
}

// relTypeStrings 参与关联终点判定的虚拟节点类型（byNode 查询条件；
// Q175 后含 xorm——旧实现漏 xorm 导致 xorm 表关联全丢，已修复）。
var relTypeStrings = map[string]bool{"sql": true, "gorm": true, "xorm": true}

type relationGraph struct {
	dataAdj map[string][]string // 数据流边双向邻接（BFS 主边，与旧 SQL OR 双向等价）
	allOut  map[string][]string // 全部边定向邻接（出边——桥 2 跳检查须定向，
	//                              与旧 SQL EXISTS 的 e1.source_id = n2.id 等价；
	//                              双向会让桥过度宽松 → 多关联噪音）
	nodes       map[string]*relNode
	readsByFunc map[string][]*relNode
}

type relNode struct {
	id         string
	kind       string
	name       string
	access     string // field_access 的 access_kind（read/write/filter）
	typeString string // 虚拟节点类型（sql/gorm/xorm/...）
	funcID     string
	fullPath   string
	isExternal bool
}

// loadRelationGraph 一次加载全库图。两条全表查询：
//  1. 全部边（kind 一并取回，分流 dataAdj/allOut）
//  2. 全部节点元数据（json_extract 6 个属性）
//
// 空库返回空图（BFS 自然空结果，不报错）。

// isDataKind 是否为 BFS 数据流边。

// tables 内存版 GetTables（语义一致：外部 gorm/sql/xorm 虚拟节点
// 表名去重排序；name 无点或含多点不产生表名）。

// typeNameOf 内存版：节点 type_string 提取类型名（[]example.com/m.Session →
// Session；*Session → Session；无类型/基本类型返回 ok=false）。

// filterReachable2 桥条件：该 read 节点下游 2 跳内可达 filter 外部节点
// （字段 → 值 → filter：真正进 Where 的字段；防同类型全字段扩散）。
// 定向出边（allOut）——与旧 SQL EXISTS（e1.source_id = n2.id）等价；
// 双向会让桥过度宽松 → 多关联噪音。

// relationsFor 单表关联分析（等价旧 GetTableRelations 逐节点 SQL 版）：
// 本表全部列虚拟节点为起点 BFS，收集其他表虚拟节点（表.列，is_external），
// 输出稳定排序（from_col, hops, to_table, to_col）。

// ---- relation_candidates 缓存（P0③）----

// currentBuildID 最新构建 id；无构建元数据（fixture/手动建库）返回空串——
// 缓存层整体跳过（现场计算，不写缓存行）。

// loadRelationCandidates 读单表缓存。返回 ok=true 表示缓存已计算该表
// （含"无关联"空结果——写入时带 marker 行）；ok=false 表示未缓存需现场算。

// saveRelationCandidates 写单表缓存（覆盖旧行；rels 为空时写 marker 行，
// 标记"该表已计算过、无关联"，避免每次查询重算）。

// rebuildRelationCandidates --all 全量重建缓存：清空该 build_id 全部行，
// 每张表写 marker（含无关联表），再写全部真实关联行。

// filterFKNoise Q159 外键语义过滤（独立函数便于单测）：
// id→id 一律丢弃（两表都不会拿各自自增主键互查）；同目标列多起点时
// 外键形态列（xxx_id）优先——主键 id 起点是对象值共享桥接噪音；保留
// 形态：A.xxx_id → B.id（外键查主键）、A.id → B.xxx_id（主键被外键引用
// 查询）、A.xxx_id → B.xxx_id（业务关联键）。

// typeNameOf 查节点 type_string 并提取类型名（[]example.com/m.Session →
// Session；*Session → Session；无类型/非 ssa_value 返回 ok=false）。

// getAllTableRelationsSQL --memory sql 模式的全库聚合：GetTables 枚举 +
// 逐表 relationsForSQL（逐节点查询，内存 O(1)——大仓库逃生路径）。
