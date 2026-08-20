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
	dataAdj    map[string][]string        // 数据流边双向邻接（BFS 主边，与旧 SQL OR 双向等价）
	crossEdges map[string]map[string]bool // 跨函数边（argument/returns 正向，Q199）
	allOut     map[string][]string        // 全部边定向邻接（出边——桥 2 跳检查须定向，
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

// ---- relation_candidates 缓存（P0③）----
