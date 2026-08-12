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
  loadRoots();

  /* ---------- 数据加载 ---------- */

  function loadRoots() {
    tip.textContent = '加载顶层入口…';
    fetch('/api/roots')
      .then(function (res) { return res.json(); })
      .then(function (data) {
        (data.nodes || []).forEach(addNode);
        graph.draw();
        var n = data.nodes ? data.nodes.length : 0;
        tip.textContent = n
          ? '已加载 ' + n + ' 个顶层入口 · 单击节点展开依赖'
          : '没有找到入口（先运行 codeintel init）';
      })
      .catch(function (err) {
        tip.textContent = '加载失败: ' + err.message;
      });
  }

  function expandNode(id) {
    if (expanding) return;
    expanding = true;
    container.classList.add('loading');
    fetch('/api/expand?id=' + encodeURIComponent(id))
      .then(function (res) {
        if (!res.ok) throw new Error('HTTP ' + res.status);
        return res.json();
      })
      .then(function (data) {
        var added = 0;
        (data.neighbors || []).forEach(function (n) { if (addNode(n)) added++; });
        (data.edges || []).forEach(function (e) { if (addEdge(e)) added++; });
        graph.draw();
        tip.textContent = added > 0
          ? '展开 ' + (added / 2 | 0) + ' 个邻居 · 继续单击节点探索'
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

  /* ---------- 图数据增量 ---------- */

  function addNode(n) {
    if (seenNodes.has(n.id)) return false;
    seenNodes.add(n.id);
    graph.addNodeData([{
      id: n.id,
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

  graph.on('node:click', function (evt) {
    var id = evt.target.id;
    if (!id) return;
    expandNode(id);
  });

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
