// 全局状态与常量：所有模块共享（重构拆分，原 app.js 1.1/1.9 节）
export const KIND_COLOR = {
  function: '#1677ff', // 函数：蓝
  method: '#13c2c2',   // 方法：青
  struct: '#52c41a',   // 结构体：绿
  interface: '#722ed1', // 接口：紫
  package: '#fa8c16',  // 包：橙
  file: '#8c8c8c',     // 文件：灰
  commit: '#595959',   // 提交：深灰
  object: '#00b96b',   // 对象：薄荷绿
  parameter: '#d48806', // 签名参数：金
  receiver: '#d4380d',  // 方法接收者：深橙（与参数同系更红，区分展示）
  result: '#f759ab',   // 返回值：粉
  field_access: '#7cb305', // 字段访问：酸橙
  ssa_value: '#bfbfbf' // SSA 值：浅灰
};
export const FLAG_COLOR = {
  main: '#eb2f96',
  http: '#fa541c',
  grpc: '#f5222d',
  framework: '#531dab'
};
export const EDGE_KIND_LINE = {
  calls: [],
  implements: [4, 4],
  imports: [2, 4],
  initializes: [1, 3],
  uses: [6, 2],
  passes_to: [2, 2, 2, 4],
  passes_result: [2, 2, 2, 4],
  of_type: [1, 4, 1, 4],
  has_method: [5, 2],
  has_param: [3, 3],
  has_result: [1, 3, 1, 3],
  argument: [2, 2],
  returns: [4, 4],
  phi_operand: [6, 2],
  alias: [1, 2]
};
export const EDGE_KIND_LABEL = {
  calls: '调用',
  implements: '拥有实现',
  imports: '导入',
  initializes: '初始化',
  uses: '使用',
  passes_to: '持有参数',
  passes_result: '持有返回参数',
  of_type: '类型',
  has_method: '拥有方法',
  has_param: '参数',
  has_result: '返回',
  argument: '参数传递',
  returns: '返回值',
  phi_operand: '分支输入',
  alias: '别名',
  data_flows_to: '数据流'
};
export const EDGE_OUT_COLOR = '#1677ff';
export const EDGE_IN_COLOR = '#f5222d';
export const EDGE_DEFAULT_COLOR = '#000000';
// 信息栏关系分组展示顺序（未知 kind 追加在最后）
export const REL_ORDER = ['calls', 'implements', 'imports', 'initializes', 'uses', 'passes_to', 'passes_result', 'of_type', 'has_method', 'has_param', 'has_result', 'argument', 'returns', 'phi_operand', 'alias', 'data_flows_to'];
// 隐藏规则可选项：展开时移除"同侧且属于这些关系类型"的兄弟
export const HIDE_OPTIONS = ['calls', 'has_method', 'implements', 'initializes', 'imports', 'uses', 'passes_to', 'passes_result', 'of_type', 'has_param', 'has_result', 'argument', 'returns', 'phi_operand', 'alias', 'data_flows_to'];
// 行内排列顺序：子节点按与父节点的边类型分组（相同类型相邻），
// [调用]（calls）放最后；未知/悬浮节点（无父边）排最前
export const ROW_KIND_RANK = {};
['implements', 'imports', 'initializes', 'uses', 'passes_to', 'passes_result',
 'of_type', 'has_method', 'has_param', 'has_result', 'argument', 'returns', 'phi_operand', 'alias', 'data_flows_to', 'calls'].forEach(function (k, i) {
  ROW_KIND_RANK[k] = i + 1;
});
// 信息栏类型/标记中文名
export const KIND_LABEL = {
  function: '函数', method: '方法', struct: '结构体', interface: '接口',
  package: '包', file: '文件', commit: '提交', object: '对象',
  parameter: '参数', receiver: '接收者', result: '返回', field_access: '字段访问', ssa_value: 'SSA 值'
};
export const FLAG_LABEL = { main: 'main 入口', http: 'HTTP 服务', grpc: 'gRPC 服务', framework: '框架回调' };

// 全局可变状态（graph 由 main.js 创建后赋值）
export const state = {
  container: document.getElementById('container'),
  tip: document.getElementById('tip'),
  entryInput: document.getElementById('entry-input'),
  entryList: document.getElementById('entry-list'),
  panel: document.getElementById('sidepanel'),
  panelBody: document.getElementById('panel-body'),
  modal: document.getElementById('modal'),
  modalTitle: document.getElementById('modal-title'),
  modalCode: document.getElementById('modal-code'),
  graph: null,
  seenNodes: new Set(),
  seenEdges: new Set(),
  expanding: false,
  expandToken: 0,
  expandedMap: new Map(),
  allEntries: [],
  entryRootId: null,
  selectedId: null,
  currentPanelId: null,
  panelGroupNodes: {},
  // 信息栏渲染时缓存的当前节点完整数据（[展开] 按钮"只显示一层"用）：
  // panelNeighbors = id → 邻居完整节点；panelEdges = 当前节点的全部边
  panelNeighbors: {},
  panelEdges: [],
  hideKinds: new Set(['calls']),
  searchTimer: null,
  searchSeq: 0
};
