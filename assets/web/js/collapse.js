// 收起（原 app.js 1.5 节）：只收一层——删本次边 + 孤儿，共享保留不递归
import { state } from './state.js';
import { hasOtherEdge } from './layout.js';
import { relayoutTree } from './layout-tree.js';
import { treeRoot } from './expand.js';

export function collapseNode(id) {
  var rec = state.expandedMap.get(id);
  if (!rec || !rec.nodes.length) return;

  // 停止布局动画并取消飞行中的展开回调
  if (typeof state.graph.stopLayout === 'function') state.graph.stopLayout();
  state.expandToken++;

  // 只收一层：删除本次展开新增的边；子节点去掉这些边后若无其他
  // 引用（孤儿）才删除——共享节点（被其他边引用）保留，不递归
  // 收子节点的展开分支（双击根不再收起整棵树）
  var toRemove = new Set();
  var edgesToRemove = new Set();
  state.expandedMap.delete(id);
  rec.edges.forEach(function (k) { edgesToRemove.add(k); });
  var data = state.graph.getData();
  rec.nodes.forEach(function (cid) {
    if (!hasOtherEdge(cid, id, toRemove, edgesToRemove)) toRemove.add(cid);
  });

  // 全量重建：保留所有不在删除集合中的节点与边
  var keepNodes = (data.nodes || []).filter(function (n) { return !toRemove.has(n.id); });
  var keepEdges = (data.edges || []).filter(function (e) {
    if (toRemove.has(e.source) || toRemove.has(e.target)) return false;
    return !edgesToRemove.has(e.source + '→' + e.target + '|' + ((e.data && e.data.kind) || ''));
  });
  // 同步去重集合
  state.seenNodes.clear();
  keepNodes.forEach(function (n) { state.seenNodes.add(n.id); });
  state.seenEdges.clear();
  keepEdges.forEach(function (e) {
    state.seenEdges.add(e.source + '→' + e.target + '|' + ((e.data && e.data.kind) || ''));
  });
  // 清理 expandedMap 中被删节点的记录（保留节点的记录不动）
  Array.from(state.expandedMap.keys()).forEach(function (k) {
    if (!keepNodes.some(function (n) { return n.id === k; })) {
      state.expandedMap.delete(k);
      return;
    }
    var r = state.expandedMap.get(k);
    if (r) {
      r.nodes = r.nodes.filter(function (cid) {
        return keepNodes.some(function (n) { return n.id === cid; });
      });
    }
  });

  state.graph.setData({ nodes: keepNodes, edges: keepEdges });
  // 收起后重排（已有节点保持位置）
  var root = treeRoot();
  if (root) {
    var prevY = {};
    state.graph.getData().nodes.forEach(function (n) {
      var d = state.graph.getNodeData(n.id);
      if (d && d.style) prevY[n.id] = d.style.y;
    });
    relayoutTree(root, prevY);
  }
  // 显式重绘：setData 后不自动渲染，须 draw（否则隐藏要等下次
  // 状态变化（如点空白）才可见）
  state.graph.draw();
}
