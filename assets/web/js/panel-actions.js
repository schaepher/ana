// 信息栏动作（原 app.js 1.8 节）：[隐藏]/[展开] 分组节点、Source Code 弹窗
import { state } from './state.js';
import { collectSubtree, treeRoot } from './expand.js';
import { addNode, addEdge } from './graph-ops.js';
import { relayoutTree } from './layout-tree.js';
import { showNodePanel } from './panel.js';

// bindPanelActions 绑定信息栏事件委托（main.js 调用）：
// Source Code 按钮 + 分组 [隐藏]/[展开] 按钮
export function bindPanelActions() {
  state.panelBody.addEventListener('click', function (evt) {
    if (evt.target.id === 'source-btn' && state.currentPanelId) showSource(state.currentPanelId);
    // 分组 [隐藏] 按钮：隐藏该分组涉及的节点（曾展开过的保留）
    if (evt.target.classList && evt.target.classList.contains('hide-group-btn')) {
      var gi = evt.target.getAttribute('data-gi');
      hideGroupNodes(state.panelGroupNodes[gi] || []);
    }
    // 分组 [展开] 按钮：只把该分组节点显示到图上（一层，不展开关系）
    if (evt.target.classList && evt.target.classList.contains('expand-group-btn')) {
      var gi = evt.target.getAttribute('data-gi');
      expandGroupNodes(state.panelGroupNodes[gi] || []);
    }
  });
  document.getElementById('modal-close').addEventListener('click', closeSource);
  state.modal.addEventListener('click', function (evt) {
    if (evt.target === state.modal) closeSource(); // 点击遮罩关闭
  });
}

// expandGroupNodes 只把分组节点显示到图上（一层）：分组里的节点加入
// 画布、补上与当前节点的边，不展开它们各自的关系（想继续可双击单节点）。
// 数据用 renderNodePanel 缓存的 panelNeighbors/panelEdges，不发请求。
function expandGroupNodes(ids) {
  if (!ids || !ids.length) return;
  var cur = state.currentPanelId;
  // 增量布局：prevY 须在 addNode 之前收集（同 expandNode 的坑）
  var prevY = {};
  state.graph.getData().nodes.forEach(function (n) {
    var d = state.graph.getNodeData(n.id);
    if (d && d.style) prevY[n.id] = d.style.y;
  });
  var newIds = [];
  var newEdgeKeys = [];
  ids.forEach(function (id) {
    if (state.seenNodes.has(id)) return;
    var n = state.panelNeighbors[id];
    if (n && addNode(n)) newIds.push(id);
  });
  // 补上与当前节点的边（分组节点之间的边不在 /api/expand 数据内）
  (state.panelEdges || []).forEach(function (e) {
    if (ids.indexOf(e.source) < 0 && ids.indexOf(e.target) < 0) return;
    var key = e.source + '→' + e.target + '|' + e.kind;
    if (addEdge(e)) newEdgeKeys.push(key);
  });
  if (!newIds.length && !newEdgeKeys.length) return;
  // 展开记录挂当前节点名下（已有记录则合并）：双击当前节点可收起这层
  var rec = state.expandedMap.get(cur);
  if (rec) {
    newIds.forEach(function (x) { if (rec.nodes.indexOf(x) < 0) rec.nodes.push(x); });
    newEdgeKeys.forEach(function (x) { if (rec.edges.indexOf(x) < 0) rec.edges.push(x); });
  } else {
    state.expandedMap.set(cur, { nodes: newIds, edges: newEdgeKeys });
  }
  // 增量重排（已有节点位置不变），越界时 fitView 兜底
  var root = treeRoot();
  if (root) relayoutTree(root, prevY);
  state.graph.draw();
  var minY = Infinity, maxY = -Infinity;
  state.graph.getData().nodes.forEach(function (n) {
    var d = state.graph.getNodeData(n.id);
    if (d && d.style) {
      if (d.style.y < minY) minY = d.style.y;
      if (d.style.y > maxY) maxY = d.style.y;
    }
  });
  var ch = state.container.clientHeight || 800;
  if ((minY < 0 || maxY > ch) && typeof state.graph.fitView === 'function') {
    setTimeout(function () { state.graph.fitView(); }, 500);
  }
  state.tip.textContent = '显示 ' + newIds.length + ' 个节点 · 双击可收起';
}

// hideGroupNodes 隐藏一组节点（及其子树边）：曾展开过（有展开记录）
// 的节点保留；隐藏后增量重排并刷新信息栏。
function hideGroupNodes(ids) {
  if (!ids || !ids.length) return;
  var toRemove = new Set();
  var edgesToRemove = new Set();
  ids.forEach(function (nid) {
    if (state.expandedMap.has(nid)) return; // 曾展开过的节点保留
    collectSubtree(nid, toRemove, edgesToRemove);
  });
  if (!toRemove.size) return;
  var data = state.graph.getData();
  var keepNodes = (data.nodes || []).filter(function (n) { return !toRemove.has(n.id); });
  var keepEdges = (data.edges || []).filter(function (e) {
    if (toRemove.has(e.source) || toRemove.has(e.target)) return false;
    return !edgesToRemove.has(e.source + '→' + e.target + '|' + ((e.data && e.data.kind) || ''));
  });
  state.seenNodes.clear();
  keepNodes.forEach(function (n) { state.seenNodes.add(n.id); });
  state.seenEdges.clear();
  keepEdges.forEach(function (e) {
    state.seenEdges.add(e.source + '→' + e.target + '|' + ((e.data && e.data.kind) || ''));
  });
  // 清理 expandedMap 中被删节点及其记录
  Array.from(state.expandedMap.keys()).forEach(function (k) {
    if (!keepNodes.some(function (n) { return n.id === k; })) {
      state.expandedMap.delete(k);
      return;
    }
    var rec = state.expandedMap.get(k);
    if (rec) {
      rec.nodes = rec.nodes.filter(function (cid) {
        return keepNodes.some(function (n) { return n.id === cid; });
      });
    }
  });
  state.graph.setData({ nodes: keepNodes, edges: keepEdges });
  // 增量重排（已有节点保持位置）
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
  // 刷新信息栏（分组已变化）
  if (state.currentPanelId) showNodePanel(state.currentPanelId);
}

// showSource 请求 /api/source 并弹窗展示函数/方法源码
function showSource(id) {
  fetch('/api/source?id=' + encodeURIComponent(id))
    .then(function (res) {
      if (!res.ok) throw new Error('HTTP ' + res.status);
      return res.json();
    })
    .then(function (data) {
      state.modalTitle.textContent = data.file + ':' + data.line;
      if (window.hljs) {
        // 语法高亮（Go）：hljs.highlight 输出已转义的安全 HTML
        state.modalCode.innerHTML = hljs.highlight(data.code, { language: 'go' }).value;
      } else {
        state.modalCode.textContent = data.code; // CDN 未加载时降级纯文本
      }
      state.modal.classList.remove('hidden');
    })
    .catch(function (err) {
      state.modalTitle.textContent = 'Source Code';
      state.modalCode.textContent = '加载源码失败: ' + err.message;
      state.modal.classList.remove('hidden');
    });
}

function closeSource() {
  state.modal.classList.add('hidden');
}
