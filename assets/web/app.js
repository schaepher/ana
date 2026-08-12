/* codeintel 依赖探索：初始展示顶层入口（main / HTTP / gRPC 服务），
 * 单击节点双向展开一级依赖（CALLS 实线、IMPLEMENTS 虚线、IMPORTS 点线）。
 * 依赖方向由箭头表示：A → B 表示 A 依赖 B（A 调用/实现/导入 B）。
 */
(function () {
  'use strict';

  var container = document.getElementById('container');
  var tip = document.getElementById('tip');
  var info = document.getElementById('info');
  var entryInput = document.getElementById('entry-input');
  var entryList = document.getElementById('entry-list');
  var seenNodes = new Set();
  var seenEdges = new Set();
  var expanding = false;
  // 展开令牌：收起会使飞行中的展开回调失效，防止已删节点"复活"
  var expandToken = 0;
  // 展开记录：parentId → { nodes: 新增节点 id, edges: 新增边 key }（双击收起用）。
  // 注意：邻居可能已在图中，其边也要记录才能完整收起。
  var expandedMap = new Map();
  // 顶层入口列表（搜索下拉框数据源）
  var allEntries = [];

  var KIND_COLOR = {
    function: '#1677ff',
    method: '#1677ff',
    struct: '#52c41a',
    interface: '#722ed1',
    package: '#fa8c16',
    file: '#8c8c8c',
    commit: '#595959',
    object: '#00b96b'
  };
  var FLAG_COLOR = {
    main: '#eb2f96',
    http: '#fa541c',
    grpc: '#f5222d',
    framework: '#531dab'
  };
  // 边：出边（该节点依赖对方）蓝色，入边（对方依赖该节点）红色；
  // 线型区分关系种类，label 显示关系说明
  var EDGE_KIND_LINE = {
    calls: [],
    implements: [4, 4],
    imports: [2, 4],
    initializes: [1, 3],
    uses: [6, 2],
    passes_to: [2, 2, 2, 4],
    of_type: [1, 4, 1, 4]
  };
  var EDGE_KIND_LABEL = {
    calls: '调用',
    implements: '实现',
    imports: '导入',
    initializes: '初始化',
    uses: '使用',
    passes_to: '传给',
    of_type: '类型'
  };
  var EDGE_OUT_COLOR = '#1677ff';
  var EDGE_IN_COLOR = '#f5222d';
  var EDGE_DEFAULT_COLOR = '#000000';
  // 当前选中节点：以其为参照，出边蓝、入边红，其他边黑色
  var selectedId = null;

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
      },
      state: {
        selected: {
          stroke: '#000000',
          lineWidth: 4
        }
      }
    },
    edge: {
      style: function (d) {
        // 颜色跟随选中节点（闭包变量 selectedId）：
        // 选中节点的出边蓝、入边红，其余黑色。样式函数在元素渲染时求值，
        // 选中变化后由 setElementState 触发全图重渲染重算。
        var stroke = EDGE_DEFAULT_COLOR;
        if (selectedId) {
          if (d.source === selectedId) stroke = EDGE_OUT_COLOR;
          else if (d.target === selectedId) stroke = EDGE_IN_COLOR;
        }
        return {
          stroke: stroke,
          lineWidth: 1.5,
          lineDash: EDGE_KIND_LINE[d.data.kind] || [],
          endArrow: true,
          endArrowSize: 8,
          labelText: EDGE_KIND_LABEL[d.data.kind] || d.data.kind,
          labelFontSize: 9,
          labelBackground: true,
          labelBackgroundFill: 'rgba(255,255,255,.8)',
          labelBackgroundRadius: 2
        };
      }
    },
    layout: { type: 'force', linkDistance: 110, preventOverlap: true, nodeStrength: 1000 },
    behaviors: ['drag-canvas', 'zoom-canvas', 'drag-element']
  });

  graph.render();
  // 调试/自动化钩子：暴露 graph 实例供 playwright 等检查布局
  window.__codeintelGraph = graph;
  loadEntries();

  /* ---------- 入口选择（搜索下拉框） ---------- */

  // 加载全部顶层入口作为搜索数据源（不放入图中）
  function loadEntries() {
    fetch('/api/roots')
      .then(function (res) { return res.json(); })
      .then(function (data) {
        allEntries = data.nodes || [];
        tip.textContent = '已加载 ' + allEntries.length + ' 个顶层入口 · 搜索选择后双击展开依赖';
      })
      .catch(function (err) {
        tip.textContent = '加载入口列表失败: ' + err.message;
      });
  }

  // 输入过滤 → 渲染下拉列表
  entryInput.addEventListener('input', function () {
    var q = entryInput.value.trim().toLowerCase();
    if (!q) {
      entryList.style.display = 'none';
      return;
    }
    var matched = allEntries.filter(function (e) {
      return (e.name + ' ' + e.id + ' ' + (e.file || '') + ' ' + (e.flags || []).join(' '))
        .toLowerCase().indexOf(q) >= 0;
    }).slice(0, 50);
    renderEntryList(matched);
  });

  entryInput.addEventListener('blur', function () {
    setTimeout(function () { entryList.style.display = 'none'; }, 150);
  });

  function renderEntryList(items) {
    if (!items.length) {
      entryList.style.display = 'none';
      return;
    }
    entryList.innerHTML = '';
    items.forEach(function (e) {
      var li = document.createElement('li');
      li.textContent = entryLabel(e);
      li.addEventListener('mousedown', function (evt) {
        evt.preventDefault();
        selectEntry(e);
      });
      entryList.appendChild(li);
    });
    entryList.style.display = 'block';
  }

  // 选择入口：清空图，仅展示该节点（双击展开依赖）
  function selectEntry(e) {
    entryList.style.display = 'none';
    entryInput.value = '';
    resetGraph();
    addNode(e);
    graph.layout();
    tip.textContent = '已选择 ' + e.name + ' · 双击节点展开依赖';
    info.style.display = 'none';
  }

  function entryLabel(e) {
    var label = e.name;
    if (e.file) label += ' · ' + e.file;
    if (e.flags && e.flags.length) label += '  [' + e.flags.join(', ') + ']';
    return label;
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
        // 三行排布被展开节点及其关联（caller 上行/节点中间/callee 下行），
        // 其他已展开节点位置不动；不跑 force 避免覆盖布局
        arrangeLayers(id);
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

  // arrangeLayers 三行排布被展开节点的关联：
  //   上行：callers（调用该节点的，calls 入边）
  //   中间行：该节点 + 非 calls 关联（implements/imports 等）
  //   下行：callees（该节点调用的，calls 出边）
  // 不跑 force 布局，避免旧坐标影响（focusOn 后不调用 graph.layout）。
  function arrangeLayers(id) {
    var data = graph.getData();
    if (!data.nodes.some(function (n) { return n.id === id; })) return;
    var w = container.clientWidth || 1200;
    var h = container.clientHeight || 800;
    var cx = w / 2;
    var cy = h / 2;
    var callers = [];
    var callees = [];
    var others = [];
    (data.edges || []).forEach(function (e) {
      var kind = (e.data && e.data.kind) || '';
      var other = null;
      if (e.source === id) other = e.target;
      else if (e.target === id) other = e.source;
      if (!other) return;
      if (kind === 'calls') {
        if (e.source === id) pushUniq(callees, other);
        else pushUniq(callers, other);
      } else {
        pushUniq(others, other);
      }
    });
    var updates = [{ id: id, style: { x: cx, y: cy } }];
    var rowGap = 240;
    var rowWidth = w - 140;
    // 中间行：非 calls 关联水平分布在节点两侧
    placeRow(updates, others, cy, rowWidth, cx, 160);
    // 上行：callers
    placeRow(updates, callers, cy - rowGap, rowWidth, cx, 90);
    // 下行：callees
    placeRow(updates, callees, cy + rowGap, rowWidth, cx, 90);
    graph.updateNodeData(updates);
  }

  // placeRow 将一组节点水平均匀排布在一行（居中）。
  function placeRow(updates, ids, y, rowWidth, cx, minSpacing) {
    if (!ids.length) return;
    var spacing = Math.min(170, Math.max(minSpacing, rowWidth / ids.length));
    var start = cx - ((ids.length - 1) * spacing) / 2;
    ids.forEach(function (nid, i) {
      updates.push({ id: nid, style: { x: start + i * spacing, y: y } });
    });
  }

  function pushUniq(arr, v) {
    if (arr.indexOf(v) < 0) arr.push(v);
  }

  // resetGraph 清空图数据与全部状态（收起顶层节点后回到入口视图）。
  function resetGraph() {
    expandToken++;
    if (typeof graph.stopLayout === 'function') graph.stopLayout();
    seenNodes.clear();
    seenEdges.clear();
    expandedMap.clear();
    selectedId = null;
    graph.setData({ nodes: [], edges: [] });
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
    // 边颜色由样式函数按 selectedId 渲染时求值（见 edge.style）
    graph.addEdgeData([{
      source: e.source,
      target: e.target,
      data: { kind: e.kind, direction: e.direction }
    }]);
    return true;
  }

  /* ---------- 交互 ---------- */

  // 单击节点：选中并以它为参照染色（出边蓝/入边红），显示符号信息
  graph.on('node:click', function (evt) {
    var id = evt.target.id;
    if (!id) return;
    selectNode(id);
    var n = nodeById(id);
    if (n) showInfo(n);
  });

  // 单击空白：取消选中，边恢复黑色
  graph.on('canvas:click', function () {
    clearSelection();
    info.style.display = 'none';
  });

  // 选中节点：先更新 selectedId 再触发状态变化重渲染。
  // G6 v5 中 setElementState 的状态变化会触发全图样式函数重新求值，
  // 边据此按新选中节点染色（updateEdgeData/draw 都不会重算样式函数）。
  function selectNode(id) {
    if (selectedId === id) return;
    if (selectedId) graph.setElementState(selectedId, []);
    selectedId = id;
    graph.setElementState(id, ['selected']);
  }

  // 取消选中：必须先置空 selectedId 再触发状态变化重渲染（顺序颠倒
  // 会导致渲染仍按旧选中节点染色）。
  function clearSelection() {
    if (!selectedId) return;
    var prev = selectedId;
    selectedId = null;
    graph.setElementState(prev, []);
  }

  graph.on('node:dblclick', function (evt) {
    var id = evt.target.id;
    if (!id) return;
    if (expandedMap.has(id)) {
      // 先取名字：顶层节点收起会清空图，之后再查会找不到节点
      var name = nodeById(id);
      collapseNode(id);
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
