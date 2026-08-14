# app.js 功能清单与重构记录（2026-08-13）

本文档整理前端各功能模块与行为契约。**重构已完成**（2026-08-13）：
原单文件 `assets/web/app.js`（1432 行）拆分为 12 个 ES module
（`assets/web/js/`，最大 226 行），`index.html` 以
`<script type="module" src="js/main.js">` 加载。重构后
`e2e/regression-suite.mjs` 22/22 通过，行为与原版一致。

## 1. 功能模块清单

### 1.1 初始化与全局状态
- G6 v5 Graph 创建：节点/边样式函数（kind 配色、flag 描边、边线型/标注）、
  force 布局、behaviors（drag-canvas / zoom-canvas / drag-element）
- 调试钩子：`window.__codeintelGraph`、`window.__codeintelExpanded`
- 状态：`seenNodes`/`seenEdges`（去重）、`expanding`（展开锁）、
  `expandToken`（展开令牌，防过期回调复活已删节点）、
  `expandedMap`（parent → {nodes, edges}，收起/剪枝依据）、
  `entryRootId`（展开树根）、`selectedId`（选中染色参照）

### 1.2 入口选择（搜索下拉）
- `loadEntries`：/api/roots 加载顶层入口
- 搜索框输入（防抖 200ms）：本地入口过滤 + /api/search 全库符号搜索合并
  （序号 searchSeq 丢弃过期结果）
- `selectEntry`：resetGraph 清空 → addNode → 入口置于画布正中
  （container.clientWidth/Height 中心）→ closePanel

### 1.3 图数据增量
- `addNode`：seenNodes 去重；网格初始位置（4 列，避免 force 堆原点）
- `addEdge`：seenEdges 去重；key = "source→target|kind"

### 1.4 展开（expandNode）
- fetch /api/expand；`expanding` 锁 + `expandToken` 防过期
- **prevY 收集**：addNode 前（增量布局用，新节点网格位置不能当已有位置）
- **struct 方法过滤**：展开 struct 时过滤 has_method 出边（方法不展示）
- **父过滤**：有父且非 up 类 → 拦 calls 入边（其他调用方）；
  up 类（caller）展示调用方（链向上延伸）
- **同向剪枝**（pruneSiblings）：只剪"同侧且关系类型在 hideKinds 配置"
  的兄弟（默认仅 calls）；已展开兄弟保留；collectSubtree 递归清理
- **布局**：有父或图中有其他节点 → relayoutTree(root, prevY)；
  否则 arrangeLayers（三行）；fitView（顶部/底部溢出，动画后 500ms）
- expandedMap 记录本次新增 nodes/edges

### 1.5 收起（collapseNode）——只收一层
- 只处理该节点自己的展开记录：删本次新增边 + 真孤儿节点
  （hasOtherEdge：去掉这些边后无其他引用的才删）
- **不递归**收子分支；共享节点（其他引用）保留
- expandedMap 清理（删节点记录、父记录过滤）；relayoutTree + draw

### 1.6 布局引擎
- **arrangeLayers**（根/无父展开，三行）：callers 上行 / 节点中间 /
  callees 下行；calls/initializes/has_method/implements 方向化
  （出=下行、入=上行）；单个 mid 节点偏移中心右侧（防重叠）；
  垂直行距 140px（与树布局一致，2026-08-13 由 240 收紧）
- **relayoutTree**（树布局）：
  1. BFS 深度：根=0，child 通过任意边指向 parent（isUp）→ 上一行
  2. tail 定位：不在展开树的节点（悬浮分支/共享节点）——第一轮与
     已分层节点按边定位；剩余多分支锚点传播（每分支锚点 maxD+2）
  3. 边方向修正循环：所有边 source 深度 < target 深度
  4. rows 分组（含 tail 行 y 补丁）
  5. rowY 分配：增量（prevY 优先 + 插值）或全量
     （depthChanged/suspended 时按 minD 分层——prevY 与新深度错位）；
     垂直行距 140px（2026-08-13 由 200 收紧——四行标签约 100px 高，
     140 仍留 ~40px 空隙，箭头指向的父子行不过分疏远）
  6. 行内排列（2026-08-13）：按与父节点的边类型分组（ROW_KIND_RANK
     顺序，相同类型相邻），[调用]（calls）放最后；BFS 时记录
     parentKind，稳定排序保持组内原顺序；悬浮/共享节点（无父边）排
     最前。arrangeLayers 三行布局的中间行 others 同样按类型分组
- `rowClass`（方向分类）、`isUp`、`edgeKind`、`hasOtherEdge`

### 1.7 交互与选中
- node:click：selectNode（**先更新 selectedId 再 setElementState**——
  否则异步绘制按旧参照染色）+ showNodePanel
- canvas:click：clearSelection + closePanel
- node:dblclick：expandedMap 有记录 → 收起；否则展开
- 选中染色：出边蓝 #1677ff / 入边红 #f5222d / 其他黑
  （setElementState 状态变化触发全图样式函数重算）

### 1.8 信息栏（右侧常驻侧边栏）
- renderNodePanel 分组：基本信息（displayName）/ 字段表（struct）/
  文档注释 / 提交信息 / 关系（按 kind → 按文件分组）
- 方向拆分：calls（被调用/调用）、has_method（方法/接收者）、
  implements（实现者/实现）
- **panelGroupNodes**：分组索引 → 对方节点 id（[隐藏]/[展开] 按钮数据）
- [隐藏] 按钮：hideGroupNodes（collectSubtree 清理 + setData + draw +
  刷新）；曾展开节点保留
- [展开] 按钮（2026-08-13 改为只显示一层）：expandGroupNodes 不再
  逐个 fetch/expandNode（那会展开两层），而是直接用渲染时缓存的
  panelNeighbors/panelEdges：分组节点 addNode + 补上与当前节点的边，
  不展开它们各自的关系；展开记录挂当前节点名下（已有记录合并），
  双击当前节点可收起这层；[隐藏]/[展开] 按钮样式统一
  （.hide-group-btn/.expand-group-btn 共用基础样式，hover 隐藏红/
  展开蓝）
- Source Code 按钮 + 弹窗（/api/source，hljs 高亮，CDN 失败降级）
- 拖拽调整宽度（#panel-resize，CSS 变量 --panel-w，240–520px）

### 1.9 配置与图例
- hideKinds（隐藏规则下拉）：勾选展开时隐藏的关系类型，
  localStorage（codeintel.hideKinds），默认 {calls}
- **统一图例**（「图例 ▾」下拉，2026-08-13 由节点类型下拉扩为四节）：
  ① 节点类型（KIND_COLOR×KIND_LABEL 填充色圆点）② 入口标记
  （FLAG_COLOR×FLAG_LABEL 描边色方块）③ 连线类型（EDGE_KIND_LINE×
  EDGE_KIND_LABEL，内联 SVG 画线：stroke-dasharray 取线型数组 +
  三角箭头）④ 选中态（出边蓝/入边红/默认黑实线）。头部不再有
  静态图例行，只有「图例 ▾」「隐藏规则 ▾」两按钮

### 1.10 工具函数
- `displayName`：方法 (T).m；函数 (pkg).f（从 canonical ID 取包名）
- `nodeLabel`：四行（目录/文件名/接收者或包名/方法或函数名）
- `escapeHtml`、`kv`、`relGroupHtml`（分组标题+隐藏/展开按钮）

## 2. 行为契约（回归要点）

1. 入口选择后节点居中；双击展开三行布局（caller 上、节点中、callee 下）
2. 展开 callee 同向剪枝保留 caller 顶行；展开 caller 显示调用方（链向上）
3. 已有节点展开时位置不变（增量）；向上展开新行插顶部（fitView 兜底）
4. 展开 struct 不显示其方法们；展开方法显示接收者
5. 收起只收一层：孤儿删、共享保留、不递归——双击根不收起整棵树
6. 所有边 source.y < target.y（箭头始终向下）
7. 选中切换颜色跟随最后点击节点（无需点空白）
8. 信息栏分组渲染、[隐藏]（已展开保留）、[展开]（只显示分组节点
   一层：新增数 = 分组数，不展开它们的关系）；两按钮样式统一
9. 标签四行；边标注：调用/拥有方法/拥有实现/持有参数/持有返回参数
10. 隐藏规则可配置（localStorage）

## 3. 重构结果（文件划分，均 <300 行）

- `js/state.js`（80）：常量 + 全局状态对象（graph/seen/expandedMap/
  selectedId/hideKinds 等）
- `js/utils.js`（66）：displayName/nodeLabel/escapeHtml/kv/pushUniq/nodeById
- `js/graph-ops.js`（40）：addNode/addEdge（去重 + 网格初始位置）
- `js/layout.js`（134）：arrangeLayers 三行 / placeRow / rowClass /
  isUp / edgeKind / hasOtherEdge
- `js/layout-tree.js`（170）：relayoutTree（BFS 深度 → tail 锚点传播 →
  边方向修正 → rowY 增量/全量）
- `js/search.js`（111）：入口加载 / 搜索框 / selectEntry / resetGraph
- `js/expand.js`（226）：expandNode / pruneSiblings / collectSubtree /
  parentOf / treeRoot
- `js/collapse.js`（约 80）：collapseNode（只收一层）
- `js/interact.js`（61）：selectNode/clearSelection/bindInteractions
- `js/panel.js`（157）：showNodePanel / renderNodePanel / relGroupHtml
- `js/panel-actions.js`（127）：hideGroupNodes / expandGroupNodes /
  Source Code 弹窗
- `js/config.js`（97）：kind-legend / hide-legend（localStorage）/
  侧栏拖拽
- `js/main.js`（81）：创建 graph、绑定各模块、调试钩子

依赖：state.js 为根；utils/graph-ops/layout 无内部依赖；
expand → panel（renderNodePanel）、config → expand/layout-tree；
无循环依赖。

## 4. 回归测试

- `e2e/regression-suite.mjs`：playwright 套件，覆盖 1.1–1.10 各模块
  与第 2 节契约；依赖 :8096 serve（codeintel 库）
- 运行：`cd /tmp/layout-test && node /home/schaepher/Codes/codeintel/e2e/regression-suite.mjs`
  （playwright 在 /tmp/layout-test/node_modules）
- 重构完成后跑全量通过即视为行为未变
