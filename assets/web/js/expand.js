// 展开与剪枝（原 app.js 1.4 节）：expandNode / pruneSiblings / 树辅助
import { state } from './state.js';
import { addNode, addEdge } from './graph-ops.js';
import { rowClass, edgeKind, arrangeLayers } from './layout.js';
import { relayoutTree } from './layout-tree.js';
import { renderNodePanel } from './panel.js';

// expandNode 展开节点：fetch /api/expand → 过滤 → addNode/addEdge →
// 同向剪枝 → 布局（整树或三行）→ fitView
export function expandNode(id) {
  if (state.expanding) return;
  state.expanding = true;
  var myToken = ++state.expandToken;
  state.container.classList.add('loading');
  fetch('/api/expand?id=' + encodeURIComponent(id))
    .then(function (res) {
      if (!res.ok) throw new Error('HTTP ' + res.status);
      return res.json();
    })
    .then(function (data) {
      // 期间发生了收起/其他展开：放弃本次结果，避免已删节点复活
      if (myToken !== state.expandToken) return data;
      // 展开前的节点位置（增量布局用）：须在 addNode 之前收集，
      // 否则新节点的网格初始位置会被当成"已有位置"
      var prevY = {};
      state.graph.getData().nodes.forEach(function (n) {
        var d = state.graph.getNodeData(n.id);
        if (d && d.style) prevY[n.id] = d.style.y;
      });
      // 展开时过滤"其他父"：已有父的节点展开后，只保留父这个 caller，
      // 其他 calls 入边节点（潜在父）不展示，只保留子节点方向。
      // 例外：展开 caller（up 类）节点时不过滤——展示它的调用方让链
      // 向上延伸。只拦 calls 入边——has_method/implements 等入边是
      // 节点的关联须展示
      var parent = parentOf(id);
      var neighbors = data.neighbors || [];
      var edges = data.edges || [];
      // 展开 struct 节点时不展示它的方法们（has_method 出边）：
      // 方法是 struct 的细节，探索链时避免其它方法涌入
      if (data.node && data.node.kind === 'struct') {
        neighbors = neighbors.filter(function (n) {
          return !edges.some(function (e) {
            return e.kind === 'has_method' && e.source === id && e.target === n.id;
          });
        });
        edges = edges.filter(function (e) {
          return !(e.kind === 'has_method' && e.source === id);
        });
      }
      if (parent) {
        // 展开 down/mid 类节点才过滤其他调用方；up 类（caller）展示调用方
        var filterCallers = rowClass(parent, id) !== 'up';
        var blocked = new Set();
        neighbors = neighbors.filter(function (n) {
          if (n.id === parent) return true;
          var e = edges.find(function (x) {
            return (x.source === id && x.target === n.id) || (x.source === n.id && x.target === id);
          });
          if (e && e.direction === 'in' && e.kind === 'calls' && filterCallers) { blocked.add(n.id); return false; }
          return true;
        });
        edges = edges.filter(function (e) {
          var other = e.source === id ? e.target : e.source;
          return !blocked.has(other);
        });
      }
      var added = 0;
      var newIds = [];
      var newEdgeKeys = [];
      neighbors.forEach(function (n) { if (addNode(n)) { added++; newIds.push(n.id); } });
      edges.forEach(function (e) {
        var key = e.source + '→' + e.target + '|' + e.kind;
        if (addEdge(e)) { added++; newEdgeKeys.push(key); }
      });
      if (newIds.length || newEdgeKeys.length) {
        state.expandedMap.set(id, { nodes: newIds, edges: newEdgeKeys });
      }
      // 展开后移除兄弟节点（父的其他子节点及其展开子树），保留一条
      // 干净的链路
      pruneSiblings(id);
      // 布局：有父或图中已有其他节点（如收起后悬浮分支再展开）用整树
      // 层级布局；根（无父且仅自身）用三行排布
      if (parent || state.graph.getData().nodes.length > 1) {
        var root = treeRoot();
        if (root) {
          relayoutTree(root, prevY);
          // 布局行超出画布（顶部/底部，如深度修正后行数变多）时自适应
          // 缩放保证全部可见。updateNodeData 无返回（同步触发动画），
          // fitView 须等动画完成后再计算包围盒（否则按旧位置算）
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
        }
      } else {
        arrangeLayers(id);
      }
      state.graph.draw(); // updateNodeData/addNodeData 后不自动渲染，须显式重绘
      state.tip.textContent = added > 0
        ? '展开 ' + newIds.length + ' 个邻居 · 双击可收起'
        : '该节点没有更多依赖';
      return data;
    })
    .then(function (data) { if (data.node) renderNodePanel(data); })
    .catch(function (err) {
      state.tip.textContent = '展开失败: ' + err.message;
    })
    .finally(function () {
      state.expanding = false;
      state.container.classList.remove('loading');
    });
}

// pruneSiblings 展开节点后的剪枝（用 setData 全量重建）：
//   1. 节点有父：同向剪枝——只移除与展开节点同侧（同方向，见 rowClass）
//      且关系类型在 hideKinds 配置的兄弟；已展开的兄弟保留
//   2. 节点无父（根）：移除其子节点的其他父节点（其他展开分支）
function pruneSiblings(id) {
  var parent = parentOf(id);
  var toRemove = new Set();
  var edgesToRemove = new Set();
  if (parent) {
    var rec = state.expandedMap.get(parent);
    if (!rec) return;
    var targetClass = rowClass(parent, id);
    var siblings = rec.nodes.filter(function (cid) {
      if (cid === id || state.expandedMap.has(cid)) return false;
      // 按配置只隐藏勾选的关系类型（默认仅 calls）：has_method/
      // implements 等未勾选的关联（接收者/接口）即使同侧也不隐藏
      var k = edgeKind(parent, cid);
      if (k === null || !state.hideKinds.has(k)) return false;
      if (targetClass === null) return true; // 方向未知：按旧行为移除
      return rowClass(parent, cid) === targetClass;
    });
    siblings.forEach(function (sid) {
      collectSubtree(sid, toRemove, edgesToRemove);
    });
  } else {
    // 无父（根）：移除其子节点的其他父节点（其他展开分支）
    var rec2 = state.expandedMap.get(id);
    if (!rec2) return;
    var otherParents = new Set();
    rec2.nodes.forEach(function (cid) {
      state.expandedMap.forEach(function (r, pid) {
        if (pid !== id && r.nodes.indexOf(cid) >= 0) otherParents.add(pid);
      });
    });
    otherParents.forEach(function (op) {
      collectSubtree(op, toRemove, edgesToRemove);
    });
  }
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
  // 清理被删除节点的展开记录，并从父记录的 children 中移除已删节点
  Array.from(state.expandedMap.keys()).forEach(function (k) {
    if (!keepNodes.some(function (n) { return n.id === k; })) {
      state.expandedMap.delete(k);
      return;
    }
    var rec3 = state.expandedMap.get(k);
    if (rec3) {
      rec3.nodes = rec3.nodes.filter(function (cid) {
        return keepNodes.some(function (n) { return n.id === cid; });
      });
    }
  });
  state.graph.setData({ nodes: keepNodes, edges: keepEdges });
}

// collectSubtree 递归收集节点及其展开子树（节点 + 边），并清理展开记录
export function collectSubtree(id, toRemove, edgesToRemove) {
  var rec = state.expandedMap.get(id);
  if (rec) {
    rec.edges.forEach(function (k) { edgesToRemove.add(k); });
    rec.nodes.forEach(function (cid) { collectSubtree(cid, toRemove, edgesToRemove); });
    state.expandedMap.delete(id);
  }
  toRemove.add(id);
}

// parentOf 返回展开记录中包含 childId 的父节点（该子节点由谁展开）
function parentOf(childId) {
  var found = null;
  state.expandedMap.forEach(function (rec, pid) {
    if (!found && rec.nodes.indexOf(childId) >= 0) found = pid;
  });
  return found;
}

// treeRoot 返回展开树根：优先用户选择的入口；否则取未被任何展开记录
// 包含为子节点的已展开节点
export function treeRoot() {
  if (state.entryRootId && state.graph.getNodeData(state.entryRootId)) {
    return state.entryRootId;
  }
  var asChild = new Set();
  state.expandedMap.forEach(function (rec) {
    rec.nodes.forEach(function (cid) { asChild.add(cid); });
  });
  var found = null;
  state.expandedMap.forEach(function (rec, pid) {
    if (!found && !asChild.has(pid)) found = pid;
  });
  return found;
}

