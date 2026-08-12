/* codeintel 依赖探索：初始展示顶层入口（main / HTTP / gRPC 服务），
 * 单击节点双向展开一级依赖（CALLS 实线、IMPLEMENTS 虚线、IMPORTS 点线）。
 * 依赖方向由箭头表示：A → B 表示 A 依赖 B（A 调用/实现/导入 B）。
 */
(function () {
  'use strict';

  var container = document.getElementById('container');
  var tip = document.getElementById('tip');
  var info = document.getElementById('info');
  var seenNodes = new Set();
  var seenEdges = new Set();
  var expanding = false;
  // 展开令牌：收起会使飞行中的展开回调失效，防止已删节点"复活"
  var expandToken = 0;
  // 展开记录：parentId → { nodes: 新增节点 id, edges: 新增边 key }（双击收起用）。
  // 注意：邻居可能已在图中（roots 里的服务入口），其边也要记录才能完整收起。
  var expandedMap = new Map();

  var KIND_COLOR = {
    function: '#1677ff',
    method: '#1677ff',
    struct: '#52c41a',
    interface: '#722ed1',
    package: '#fa8c16',
    file: '#8c8c8c',
    commit: '#595959'
  };
  var FLAG_COLOR = {
    main: '#eb2f96',
    http: '#fa541c',
    grpc: '#f5222d'
  };
  var EDGE_STYLE = {
    calls: { stroke: '#4e5969', lineDash: [] },
    implements: { stroke: '#722ed1', lineDash: [4, 4] },
    imports: { stroke: '#fa8c16', lineDash: [2, 4] }
  };

  var graph = new G6.Graph({
    container: container,
    autoFit: 'view',
    data: { nodes: [], edges: [] },
    node: {
      style: {
        size: 34,
        fill: function (d) { return KIND_COLOR[d.data.kind] || '#86909c'; },
        stroke: function (d) {
          var flags = d.data.flags || [];
          for (var i = 0; i < flags.length; i++) {
            if (FLAG_COLOR[flags[i]]) return FLAG_COLOR[flags[i]];
          }
          return '#000';
        },
        lineWidth: function (d) { return (d.data.flags && d.data.flags.length) ? 3 : 1.5; },
        labelText: function (d) { return d.data.label; },
        labelPlacement: 'bottom',
        labelBackground: true,
        labelBackgroundFill: 'rgba(255,255,255,.85)',
        labelBackgroundRadius: 3,
        labelFontSize: 12,
        cursor: 'pointer'
      }
    },
    edge: {
      style: function (d) {
        var s = EDGE_STYLE[d.data.kind] || EDGE_STYLE.calls;
        return {
          stroke: s.stroke,
          lineWidth: 1.5,
          lineDash: s.lineDash,
          endArrow: true,
          endArrowSize: 8
        };
      }
    },
    layout: { type: 'force', linkDistance: 110, preventOverlap: true, nodeStrength: 1000 },
    behaviors: ['drag-canvas', 'zoom-canvas', 'drag-element']
  });

  graph.render();
  // 调试/自动化钩子：暴露 graph 实例供 playwright 等检查布局
  window.__codeintelGraph = graph;
  loadRoots();

  /* ---------- 数据加载 ---------- */

  function loadRoots() {
    tip.textContent = '加载顶层入口…';
    fetch('/api/roots')
      .then(function (res) { return res.json(); })
      .then(function (data) {
        (data.nodes || []).forEach(addNode);
        // 注意：draw() 只渲染不布局，增量数据必须显式 layout() 否则节点堆在原点
        graph.layout();
        var n = data.nodes ? data.nodes.length : 0;
        tip.textContent = n
          ? '已加载 ' + n + ' 个顶层入口 · 双击节点展开/收起依赖'
          : '没有找到入口（先运行 codeintel init）';
      })
      .catch(function (err) {
        tip.textContent = '加载失败: ' + err.message;
      });
  }

  function expandNode(id) {
    if (expanding) return;
    expanding = true;
    var myToken = ++expandToken;
    container.classList.add('loading');
    fetch('/api/expand?id=' + encodeURIComponent(id))
      .then(function (res) {
        if (!res.ok) throw new Error('HTTP ' + res.status);
        return res.json();
      })
      .then(function (data) {
        // 期间发生了收起/其他展开：放弃本次结果，避免已删节点复活
        if (myToken !== expandToken) return data;
        var added = 0;
        var newIds = [];
        var newEdgeKeys = [];
        (data.neighbors || []).forEach(function (n) { if (addNode(n)) { added++; newIds.push(n.id); } });
        (data.edges || []).forEach(function (e) {
          var key = e.source + '→' + e.target + '|' + e.kind;
          if (addEdge(e)) { added++; newEdgeKeys.push(key); }
        });
        if (newIds.length || newEdgeKeys.length) {
          expandedMap.set(id, { nodes: newIds, edges: newEdgeKeys });
        }
        graph.layout(); // 增量数据后必须显式布局，否则节点堆在原点
        tip.textContent = added > 0
          ? '展开 ' + newIds.length + ' 个邻居 · 双击可收起'
          : '该节点没有更多依赖';
        return data;
      })
      .then(function (data) { if (data.node) showInfo(data.node); })
      .catch(function (err) {
        tip.textContent = '展开失败: ' + err.message;
      })
      .finally(function () {
        expanding = false;
        container.classList.remove('loading');
      });
  }

  // collapseNode 收起节点的展开分支（递归）：删除子节点中只与该节点
  // 相连的（孤儿）节点；仍被其他节点引用的共享节点保留（但其与收起
  // 节点的边一并删除）。实现上用 setData 全量重建——G6 v5 的
  // removeEdgeData/removeNodeData 增量删除在批处理时可能引用已删节点
  // （"Node not found"），全量重建规避该坑。
  function collapseNode(id) {
    var children = expandedMap.get(id);
    if (!children || children.size === 0) return;

    // 停止布局动画并取消飞行中的展开回调
    if (typeof graph.stopLayout === 'function') graph.stopLayout();
    expandToken++;

    // 递归收集要删除的节点（孤儿才删）与要删除的边（本次展开添加的边）
    var toRemove = new Set();
    var edgesToRemove = new Set();
    collectCollapse(id, toRemove, edgesToRemove);

    // 全量重建：保留所有不在删除集合中的节点与边
    var data = graph.getData();
    var keepNodes = (data.nodes || []).filter(function (n) { return !toRemove.has(n.id); });
    var keepEdges = (data.edges || []).filter(function (e) {
      if (toRemove.has(e.source) || toRemove.has(e.target)) return false;
      return !edgesToRemove.has(e.source + '→' + e.target + '|' + ((e.data && e.data.kind) || ''));
    });
    // 同步去重集合
    seenNodes.clear();
    keepNodes.forEach(function (n) { seenNodes.add(n.id); });
    seenEdges.clear();
    keepEdges.forEach(function (e) {
      seenEdges.add(e.source + '→' + e.target + '|' + ((e.data && e.data.kind) || ''));
    });

    graph.setData({ nodes: keepNodes, edges: keepEdges });
    graph.layout();
  }

  // collectCollapse 递归收集收起子树中应删除的节点与边：
  // - edgesToRemove：各层展开时新增的边（key 为 "source→target|kind"）
  // - toRemove：孤儿子节点（无指向保留节点的边）才删除
  function collectCollapse(id, toRemove, edgesToRemove) {
    var record = expandedMap.get(id);
    if (!record) return;
    expandedMap.delete(id);
    record.edges.forEach(function (k) { edgesToRemove.add(k); });
    var data = graph.getData();
    record.nodes.forEach(function (cid) {
      collectCollapse(cid, toRemove, edgesToRemove);
      if (toRemove.has(cid)) return;
      var hasOtherEdge = (data.edges || []).some(function (e) {
        if (e.source === cid || e.target === cid) {
          var other = e.source === cid ? e.target : e.source;
          var k1 = e.source + '→' + e.target + '|' + ((e.data && e.data.kind) || '');
          var k2 = e.target + '→' + e.source + '|' + ((e.data && e.data.kind) || '');
          return other !== id && !toRemove.has(other) &&
            !edgesToRemove.has(k1) && !edgesToRemove.has(k2);
        }
        return false;
      });
      if (!hasOtherEdge) toRemove.add(cid);
    });
  }

  /* ---------- 图数据增量 ---------- */

  function addNode(n) {
    if (seenNodes.has(n.id)) return false;
    seenNodes.add(n.id);
    // 预置网格初始位置：G6 v5 force 布局不处理孤立节点与增量新节点，
    // 无初始位置的节点会堆在原点。force 对连通部分重排，孤立节点保留此位置。
    // 固定列数避免 sqrt 回绕导致位置重叠。
    var idx = seenNodes.size - 1;
    var cols = 4;
    graph.addNodeData([{
      id: n.id,
      style: {
        x: 100 + (idx % cols) * 140,
        y: 100 + Math.floor(idx / cols) * 140
      },
      data: {
        label: shortLabel(n),
        kind: n.kind,
        flags: n.flags || [],
        full: n
      }
    }]);
    return true;
  }

  function addEdge(e) {
    var key = e.source + '→' + e.target + '|' + e.kind;
    if (seenEdges.has(key)) return false;
    seenEdges.add(key);
    graph.addEdgeData([{
      source: e.source,
      target: e.target,
      data: { kind: e.kind }
    }]);
    return true;
  }

  /* ---------- 交互 ---------- */

  // 单击：显示符号信息；双击：展开 / 收起依赖
  graph.on('node:click', function (evt) {
    var id = evt.target.id;
    if (!id) return;
    var n = nodeById(id);
    if (n) showInfo(n);
  });

  graph.on('node:dblclick', function (evt) {
    var id = evt.target.id;
    if (!id) return;
    if (expandedMap.has(id)) {
      collapseNode(id);
      var name = nodeById(id);
      tip.textContent = '已收起' + (name ? ' ' + name.name : '') + ' · 双击可重新展开';
    } else {
      expandNode(id);
    }
  });

  // nodeById 从图数据中取节点信息（含原始 API 数据）。
  function nodeById(id) {
    var d = graph.getNodeData(id);
    return d && d.data ? d.data.full : null;
  }

  function showInfo(n) {
    if (!n) return;
    var parts = [];
    parts.push('<span class="label">' + escapeHtml(n.name) + '</span>');
    if (n.kind) parts.push('<span class="label">[' + n.kind + ']</span>');
    if (n.file) {
      parts.push('<span class="label">' + escapeHtml(n.file) + (n.line ? ':' + n.line : '') + '</span>');
    }
    if (n.signature) parts.push('<span class="sig">' + escapeHtml(n.signature) + '</span>');
    if (parts.length) {
      info.innerHTML = parts.join(' ');
      info.style.display = 'block';
    }
  }

  /* ---------- 工具 ---------- */

  function shortLabel(n) {
    var name = n.name || '';
    // 方法名 (T).m 已短；包节点显示包名
    return name;
  }

  function escapeHtml(s) {
    return String(s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }
})();
