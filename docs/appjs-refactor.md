# app.js 功能清单与重构准备（2026-08-13）

本文档整理 `assets/web/app.js` 的当前功能模块与行为契约，作为重构基线。
重构后须通过 `e2e/regression-suite.mjs`（playwright 回归套件）验证行为不变。

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
  （出=下行、入=上行）；单个 mid 节点偏移中心右侧（防重叠）
- **relayoutTree**（树布局）：
  1. BFS 深度：根=0，child 通过任意边指向 parent（isUp）→ 上一行
  2. tail 定位：不在展开树的节点（悬浮分支/共享节点）——第一轮与
     已分层节点按边定位；剩余多分支锚点传播（每分支锚点 maxD+2）
  3. 边方向修正循环：所有边 source 深度 < target 深度
  4. rows 分组（含 tail 行 y 补丁）
  5. rowY 分配：增量（prevY 优先 + 插值）或全量
     （depthChanged/suspended 时按 minD 分层——prevY 与新深度错位）
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
- [展开] 按钮：expandGroupNodes（串行，等 expanding 释放）
- Source Code 按钮 + 弹窗（/api/source，hljs 高亮，CDN 失败降级）
- 拖拽调整宽度（#panel-resize，CSS 变量 --panel-w，240–520px）

### 1.9 配置与图例
- hideKinds（隐藏规则下拉）：勾选展开时隐藏的关系类型，
  localStorage（codeintel.hideKinds），默认 {calls}
- kind-legend（节点类型颜色图例下拉）

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
8. 信息栏分组渲染、[隐藏]（已展开保留）、[展开]（依次展开）
9. 标签四行；边标注：调用/拥有方法/拥有实现/持有参数/持有返回参数
10. 隐藏规则可配置（localStorage）

## 3. 重构建议（拆分方向）

- 状态层：graph state（seen/expandedMap/selectedId/expandToken）独立
- 布局引擎：arrangeLayers/relayoutTree/rowClass 独立模块（纯函数化，
  便于单测）
- 图操作：addNode/addEdge/collapse/prune 独立（数据操作与渲染解耦）
- 信息栏：render/事件/按钮逻辑独立模块
- 配置：hideKinds/图例/侧栏宽度独立

## 4. 回归测试

- `e2e/regression-suite.mjs`：playwright 套件，覆盖 1.1–1.10 各模块
  与第 2 节契约；依赖 :8096 serve（ana 库）
- 运行：`cd /tmp/layout-test && node /home/schaepher/Codes/ana/e2e/regression-suite.mjs`
  （playwright 在 /tmp/layout-test/node_modules）
- 重构完成后跑全量通过即视为行为未变
