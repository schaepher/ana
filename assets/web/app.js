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
  // 展开树的根（用户选择的入口），收起后整树按层级重新布局
  var entryRootId = null;

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
    of_type: [1, 4, 1, 4],
    has_receiver: [5, 2]
  };
  var EDGE_KIND_LABEL = {
    calls: '调用',
    implements: '实现',
    imports: '导入',
    initializes: '初始化',
    uses: '使用',
    passes_to: '传给',
    of_type: '类型',
    has_receiver: '接收者',
    data_flows_to: '数据流'
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
        labelFontSize: 10,
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
  window.__codeintelExpanded = expandedMap;
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

  // 输入过滤（防抖 200ms）→ 本地入口 + 全库符号搜索合并渲染
  var searchTimer = null;
  var searchSeq = 0;
  entryInput.addEventListener('input', function () {
    var q = entryInput.value.trim().toLowerCase();
    if (!q) {
      entryList.style.display = 'none';
      return;
    }
    clearTimeout(searchTimer);
    searchTimer = setTimeout(function () { doSearch(q); }, 200);
  });

  function doSearch(q) {
    var seq = ++searchSeq;
    var matched = allEntries.filter(function (e) {
      return (e.name + ' ' + e.id + ' ' + (e.file || '') + ' ' + (e.flags || []).join(' '))
        .toLowerCase().indexOf(q) >= 0;
    });
    // 全库符号搜索补充（入口之外的符号，如任意函数/方法）
    fetch('/api/search?q=' + encodeURIComponent(q))
      .then(function (res) { return res.json(); })
      .then(function (data) {
        if (seq !== searchSeq) return; // 丢弃过期结果
        var seen = new Set(matched.map(function (e) { return e.id; }));
        (data.nodes || []).forEach(function (n) {
          if (!seen.has(n.id)) {
            seen.add(n.id);
            matched.push(n);
          }
        });
        renderEntryList(matched.slice(0, 50));
      })
      .catch(function () {
        if (seq === searchSeq) renderEntryList(matched.slice(0, 50));
      });
  }

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
    entryRootId = e.id; // 展开树根
    addNode(e);
    // 入口节点置于画布正中：addNode 的网格预置位置在左上角，force 布局
    // 不移动孤立节点，需显式定位
    var w = container.clientWidth || 1200;
    var h = container.clientHeight || 800;
    graph.updateNodeData([{ id: e.id, style: { x: w / 2, y: h / 2 } }]);
    graph.layout();
    tip.textContent = '已选择 ' + e.name + ' · 双击节点展开依赖';
    closePanel();
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
        // 展开时过滤"其他父"：已有父的节点展开后，caller 方向只保留父，
        // 其他入边节点（潜在父）不展示，只保留子节点方向
        var parent = parentOf(id);
        var neighbors = data.neighbors || [];
        var edges = data.edges || [];
        if (parent) {
          var blocked = new Set();
          neighbors = neighbors.filter(function (n) {
            if (n.id === parent) return true;
            var e = edges.find(function (x) {
              return (x.source === id && x.target === n.id) || (x.source === n.id && x.target === id);
            });
            if (e && e.direction === 'in') { blocked.add(n.id); return false; }
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
          expandedMap.set(id, { nodes: newIds, edges: newEdgeKeys });
        }
        // 展开后移除兄弟节点（父节点的其他子节点及其展开子树），
        // 保留一条干净的链路
        pruneSiblings(id);
        // 布局：有父的节点展开用整树层级布局（根最上、逐层向下），
        // 避免多级链三行布局把每层父节点都排到同一行导致重叠；
        // 根（无父）展开用三行排布（caller 上行/节点中间/callee 下行）
        if (parentOf(id)) {
          var root = treeRoot();
          if (root) relayoutTree(root);
        } else {
          arrangeLayers(id);
        }
        graph.draw(); // updateNodeData/addNodeData 后不自动渲染，须显式重绘
        tip.textContent = added > 0
          ? '展开 ' + newIds.length + ' 个邻居 · 双击可收起'
          : '该节点没有更多依赖';
        return data;
      })
      .then(function (data) { if (data.node) renderNodePanel(data); })
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
      // 按关系方向分行，避免非 calls 关系堆在中间一行：
      //   出边（我调用/初始化/实现/导入）→ 下行；入边（别人调用/初始化我、
      //   接口被实现、包被导入）→ 上行；对象关系（uses/passes_to）→ 中间
      var down = e.source === id;
      switch (kind) {
        case 'calls':
        case 'initializes':
          if (down) pushUniq(callees, other);
          else pushUniq(callers, other);
          break;
        case 'implements':
        case 'imports':
          if (down) pushUniq(callers, other); // 实现的接口/导入的包 → 上行
          else pushUniq(callees, other);      // 接口的实现者/被导入者 → 下行
          break;
        default: // uses / passes_to / of_type 等对象关系
          pushUniq(others, other);
      }
    });
    var updates = [{ id: id, style: { x: cx, y: cy } }];
    var rowGap = 240;
    var rowWidth = w - 140;
    // 中间行：非 calls 关联水平分布在节点两侧；
    // offsetSingle：单个节点（如 receiver）偏移到中心右侧，避免与中心节点重叠
    placeRow(updates, others, cy, rowWidth, cx, 160, true);
    // 上行：callers
    placeRow(updates, callers, cy - rowGap, rowWidth, cx, 90);
    // 下行：callees
    placeRow(updates, callees, cy + rowGap, rowWidth, cx, 90);
    graph.updateNodeData(updates);
  }

  // placeRow 将一组节点水平均匀排布在一行（居中）。
  // offsetSingle：仅 1 个节点且与中心节点同行时偏移到中心右侧（避免重叠）。
  function placeRow(updates, ids, y, rowWidth, cx, minSpacing, offsetSingle) {
    if (!ids.length) return;
    var spacing = Math.min(170, Math.max(minSpacing, rowWidth / ids.length));
    var start = cx - ((ids.length - 1) * spacing) / 2;
    if (offsetSingle && ids.length === 1) start = cx + spacing;
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
    // 收起后对展开树按层级重新布局（根在上、逐层向下，每层水平均匀），
    // 避免长链收起后远处节点漂移导致层次混乱
    var root = treeRoot();
    if (root) {
      relayoutTree(root);
    }
    graph.draw();
  }

  // pruneSiblings 展开节点后的剪枝（用 setData 全量重建）：
  //   1. 节点有父：同向剪枝——只移除与展开节点同侧（同方向，见 rowClass）
  //      的兄弟，另一侧保留（展开 callee 时保留 caller，链路顶行不消失）；
  //      已展开的兄弟节点（有展开记录）保留；方向无法判断时按旧行为移除全部
  //   2. 节点无父（根）：移除其子节点的其他父节点（其他展开分支）
  function pruneSiblings(id) {
    var parent = parentOf(id);
    var toRemove = new Set();
    var edgesToRemove = new Set();
    if (parent) {
      var rec = expandedMap.get(parent);
      if (!rec) return;
      var targetClass = rowClass(parent, id);
      var siblings = rec.nodes.filter(function (cid) {
        if (cid === id || expandedMap.has(cid)) return false;
        if (targetClass === null) return true; // 方向未知：按旧行为移除
        return rowClass(parent, cid) === targetClass;
      });
      siblings.forEach(function (sid) {
        collectSubtree(sid, toRemove, edgesToRemove);
      });
    } else {
      // 无父（根）：移除其子节点的其他父节点（其他分支）
      var rec2 = expandedMap.get(id);
      if (!rec2) return;
      var otherParents = new Set();
      rec2.nodes.forEach(function (cid) {
        expandedMap.forEach(function (r, pid) {
          if (pid !== id && r.nodes.indexOf(cid) >= 0) otherParents.add(pid);
        });
      });
      otherParents.forEach(function (op) {
        collectSubtree(op, toRemove, edgesToRemove);
      });
    }
    if (!toRemove.size) return;

    var data = graph.getData();
    var keepNodes = (data.nodes || []).filter(function (n) { return !toRemove.has(n.id); });
    var keepEdges = (data.edges || []).filter(function (e) {
      if (toRemove.has(e.source) || toRemove.has(e.target)) return false;
      return !edgesToRemove.has(e.source + '→' + e.target + '|' + ((e.data && e.data.kind) || ''));
    });
    seenNodes.clear();
    keepNodes.forEach(function (n) { seenNodes.add(n.id); });
    seenEdges.clear();
    keepEdges.forEach(function (e) {
      seenEdges.add(e.source + '→' + e.target + '|' + ((e.data && e.data.kind) || ''));
    });
    // 清理被删除节点的展开记录，并从父记录的 children 中移除已删节点
    Array.from(expandedMap.keys()).forEach(function (k) {
      if (!keepNodes.some(function (n) { return n.id === k; })) {
        expandedMap.delete(k);
        return;
      }
      var rec = expandedMap.get(k);
      if (rec) {
        rec.nodes = rec.nodes.filter(function (cid) {
          return keepNodes.some(function (n) { return n.id === cid; });
        });
      }
    });
    graph.setData({ nodes: keepNodes, edges: keepEdges });
  }

  // collectSubtree 递归收集节点及其展开子树（节点 + 边），并清理展开记录。
  function collectSubtree(id, toRemove, edgesToRemove) {
    var rec = expandedMap.get(id);
    if (rec) {
      rec.edges.forEach(function (k) { edgesToRemove.add(k); });
      rec.nodes.forEach(function (cid) { collectSubtree(cid, toRemove, edgesToRemove); });
      expandedMap.delete(id);
    }
    toRemove.add(id);
  }

  // parentOf 返回展开记录中包含 childId 的父节点（该子节点由谁展开）。
  function parentOf(childId) {
    var found = null;
    expandedMap.forEach(function (rec, pid) {
      if (!found && rec.nodes.indexOf(childId) >= 0) found = pid;
    });
    return found;
  }

  // rowClass 判断节点 other 相对中心节点 center 的布局方向（与 arrangeLayers
  // 相同的分类）："up"=caller 行、"down"=callee 行、"mid"=中间行（对象关系
  // uses/passes_to/of_type）；图中无边时返回 null。用于同向剪枝：展开节点时
  // 只移除同侧兄弟。
  // 注意：树布局（relayoutTree）不用本函数判断上下——implements/imports 虽
  // 在三行布局中视为上行依赖，但在链视图中是节点自身的子项，排下一行。

  // isCaller 判断 child 是否为 parent 的 caller（calls 入边）。树布局行号
  // 仅以 calls 入边为"上一行"（链路顶行），其余关系一律下一行，保证链路
  // 垂直（展开 callee 后 cmdInit 仍居中在 FullBuild 上方）。
  function isCaller(parent, child) {
    var data = graph.getData();
    return (data.edges || []).some(function (x) {
      return x.source === child && x.target === parent &&
        ((x.data && x.data.kind) || '') === 'calls';
    });
  }
  function rowClass(center, other) {
    var data = graph.getData();
    var e = (data.edges || []).find(function (x) {
      return (x.source === center && x.target === other) ||
             (x.source === other && x.target === center);
    });
    if (!e) return null;
    var kind = (e.data && e.data.kind) || '';
    var down = e.source === center;
    switch (kind) {
      case 'calls':
      case 'initializes':
        return down ? 'down' : 'up';
      case 'implements':
      case 'imports':
        return down ? 'up' : 'down';
      default:
        return 'mid';
    }
  }

  // treeRoot 返回展开树根：优先用户选择的入口；否则取未被任何展开记录
  // 包含为子节点的已展开节点。
  function treeRoot() {
    if (entryRootId && graph.getNodeData(entryRootId)) {
      return entryRootId;
    }
    var asChild = new Set();
    expandedMap.forEach(function (rec) {
      rec.nodes.forEach(function (cid) { asChild.add(cid); });
    });
    var found = null;
    expandedMap.forEach(function (rec, pid) {
      if (!found && !asChild.has(pid)) found = pid;
    });
    return found;
  }

  // relayoutTree 对展开树做方向感知的层级布局：以根为中间行，caller 子
  // 节点（rowClass=up）排在其上一行、callee 子节点（down/mid）排在下一
  // 行，逐层递推；每行水平均匀分布。未在展开树中的节点（如共享节点）
  // 追加到最下行。相比按展开树深度分层，方向感知保证链路顶行（caller，
  // 如 cmdInit）始终位于其父节点上方——展开 callee 后顶行不会落回父节点
  // 下一行。
  function relayoutTree(rootId) {
    var data = graph.getData();
    if (!data.nodes.some(function (n) { return n.id === rootId; })) return;
    var nodeSet = new Set(data.nodes.map(function (n) { return n.id; }));
    // BFS 计算每节点行号：根=0，caller（calls 入边）-1，其余（callee、
    // 实现接口、导入包、对象关系）+1
    var depths = new Map([[rootId, 0]]);
    var queue = [rootId];
    while (queue.length) {
      var pid = queue.shift();
      var rec = expandedMap.get(pid);
      if (!rec) continue;
      var pd = depths.get(pid);
      rec.nodes.forEach(function (cid) {
        if (!nodeSet.has(cid) || depths.has(cid)) return; // 已分配/已删节点跳过
        depths.set(cid, pd + (isCaller(pid, cid) ? -1 : 1));
        queue.push(cid);
      });
    }
    // 未在展开树中的节点（如共享节点）追加到最后一行
    var tail = [];
    data.nodes.forEach(function (n) {
      if (!depths.has(n.id)) tail.push(n.id);
    });
    // 按行号分组
    var rows = new Map();
    depths.forEach(function (d, nid) {
      if (!rows.has(d)) rows.set(d, []);
      rows.get(d).push(nid);
    });
    if (tail.length) {
      var maxD = 0;
      rows.forEach(function (_, d) { if (d > maxD) maxD = d; });
      rows.set(maxD + 1, tail);
    }
    var w = container.clientWidth || 1200;
    var h = container.clientHeight || 800;
    var cx = w / 2;
    var rowGap = 200;
    var startY = 80;
    var minD = 0;
    rows.forEach(function (_, d) { if (d < minD) minD = d; });
    var updates = [];
    rows.forEach(function (ids, d) {
      var y = startY + (d - minD) * rowGap;
      var spacing = Math.min(180, Math.max(90, (w - 120) / ids.length));
      var x0 = cx - ((ids.length - 1) * spacing) / 2;
      ids.forEach(function (nid, i) {
        updates.push({ id: nid, style: { x: x0 + i * spacing, y: y } });
      });
    });
    graph.updateNodeData(updates);
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
        label: nodeLabel(n),
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

  // 单击节点：选中并以它为参照染色（出边蓝/入边红），右侧信息栏展示详情
  graph.on('node:click', function (evt) {
    var id = evt.target.id;
    if (!id) return;
    selectNode(id);
    showNodePanel(id);
  });

  // 单击空白：取消选中，边恢复黑色，关闭信息栏
  graph.on('canvas:click', function () {
    clearSelection();
    closePanel();
  });

  // 选中节点：先更新 selectedId 再触发状态变化重渲染（与 clearSelection
  // 相同的顺序原则）。G6 v5 的 setElementState 是异步绘制（内部 await
  // element.draw），且绘制时才会重算样式函数（读闭包 selectedId）：若先
  // 调用 setElementState(旧节点, []) 再更新 selectedId，前一次异步绘制
  // 会按旧选中节点染色并在之后完成，覆盖新染色——表现为直接点其他节点时
  // 边色不重置。先更新参照变量，保证每次重渲染都按新选中节点求值。
  function selectNode(id) {
    if (selectedId === id) return;
    var prev = selectedId;
    selectedId = id;
    if (prev) graph.setElementState(prev, []);
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

  /* ---------- 右侧节点信息栏 ---------- */

  var panel = document.getElementById('sidepanel');
  var panelBody = document.getElementById('panel-body');
  var modal = document.getElementById('modal');
  var modalTitle = document.getElementById('modal-title');
  var modalCode = document.getElementById('modal-code');
  // 当前信息栏节点（Source Code 按钮用）
  var currentPanelId = null;
  // 委托：Source Code 按钮（函数/方法节点信息栏顶部）
  panelBody.addEventListener('click', function (evt) {
    if (evt.target.id === 'source-btn' && currentPanelId) showSource(currentPanelId);
  });
  document.getElementById('modal-close').addEventListener('click', closeSource);
  modal.addEventListener('click', function (evt) {
    if (evt.target === modal) closeSource(); // 点击遮罩关闭
  });

  // showSource 请求 /api/source 并弹窗展示函数/方法源码
  function showSource(id) {
    fetch('/api/source?id=' + encodeURIComponent(id))
      .then(function (res) {
        if (!res.ok) throw new Error('HTTP ' + res.status);
        return res.json();
      })
      .then(function (data) {
        modalTitle.textContent = data.file + ':' + data.line;
        modalCode.textContent = data.code;
        modal.classList.remove('hidden');
      })
      .catch(function (err) {
        modalTitle.textContent = 'Source Code';
        modalCode.textContent = '加载源码失败: ' + err.message;
        modal.classList.remove('hidden');
      });
  }

  function closeSource() {
    modal.classList.add('hidden');
  }

  var KIND_LABEL = {
    function: '函数', method: '方法', struct: '结构体', interface: '接口',
    package: '包', file: '文件', commit: '提交', object: '对象'
  };
  var FLAG_LABEL = { main: 'main 入口', http: 'HTTP 服务', grpc: 'gRPC 服务', framework: '框架回调' };
  // 关系分组展示顺序（未知 kind 追加在最后）
  var REL_ORDER = ['calls', 'implements', 'imports', 'initializes', 'uses', 'passes_to', 'of_type', 'has_receiver', 'data_flows_to'];

  // showNodePanel 单击节点：复用 /api/expand 取节点的完整关系后渲染信息栏
  function showNodePanel(id) {
    fetch('/api/expand?id=' + encodeURIComponent(id))
      .then(function (res) {
        if (!res.ok) throw new Error('HTTP ' + res.status);
        return res.json();
      })
      .then(function (data) { renderNodePanel(data); })
      .catch(function (err) {
        panelBody.innerHTML = '<p class="doc">加载节点信息失败: ' + escapeHtml(err.message) + '</p>';
      });
  }

  // renderNodePanel 渲染信息栏，分组：基本信息 / 文档注释 / 提交信息 /
  // 关系（按类型分组，出方向在前，每条显示方向、对方节点与位置行号）
  function renderNodePanel(data) {
    var node = data.node;
    if (!node) return;
    currentPanelId = node.id;
    var edges = data.edges || [];
    var byId = {};
    (data.neighbors || []).forEach(function (n) { byId[n.id] = n; });
    var html = [];

    // 函数/方法：Source Code 按钮（弹窗展示源码）
    if (node.kind === 'function' || node.kind === 'method') {
      html.push('<button id="source-btn">Source Code</button>');
    }

    // 基本信息
    var basic = [];
    basic.push(kv('名称', node.name));
    basic.push(kv('类型', KIND_LABEL[node.kind] || node.kind));
    if (node.file) basic.push(kv('文件', node.file + (node.line ? ':' + node.line : '')));
    if (node.signature) basic.push(kv('签名', node.signature));
    if (node.flags && node.flags.length) {
      basic.push(kv('标记', node.flags.map(function (f) { return FLAG_LABEL[f] || f; }).join('、')));
    }
    basic.push(kv('ID', node.id));
    html.push('<h3>基本信息</h3>' + basic.join(''));

    // 字段（struct 节点）
    if (node.fields && node.fields.length) {
      var rows = node.fields.map(function (f) {
        return '<tr><td>' + escapeHtml(f.name) + '</td><td class="ftype">' + escapeHtml(f.type) + '</td></tr>';
      }).join('');
      html.push('<h3>字段（' + node.fields.length + '）</h3>' +
        '<table class="fields"><thead><tr><th>字段名</th><th>类型</th></tr></thead><tbody>' + rows + '</tbody></table>');
    }

    // 文档注释
    if (node.docComment) html.push('<h3>文档注释</h3><p class="doc">' + escapeHtml(node.docComment) + '</p>');

    // 提交信息（commit 节点）
    if (node.message) {
      var c = [kv('说明', node.message)];
      if (node.date) c.push(kv('时间', node.date));
      html.push('<h3>提交信息</h3>' + c.join(''));
    }

    // 关系：按类型分组
    var byKind = {};
    var restOrder = [];
    edges.forEach(function (e) {
      if (!byKind[e.kind]) {
        byKind[e.kind] = [];
        if (REL_ORDER.indexOf(e.kind) < 0) restOrder.push(e.kind);
      }
      var other = e.source === node.id ? e.target : e.source;
      var otherNode = byId[other];
      byKind[e.kind].push({
        dir: e.source === node.id ? '出' : '入',
        name: otherNode ? otherNode.name : other,
        loc: locOf(e, node, otherNode)
      });
    });
    REL_ORDER.concat(restOrder).forEach(function (kind) {
      var items = byKind[kind];
      if (!items || !items.length) return;
      items.sort(function (a, b) { return a.dir === b.dir ? 0 : (a.dir === '出' ? -1 : 1); });
      var lis = items.map(function (g) {
        var loc = g.loc ? ' · ' + escapeHtml(g.loc) : '';
        return '<div class="rel"><span class="dir">' + (g.dir === '出' ? '→' : '←') + '</span>' +
          '<span class="name">' + escapeHtml(g.name) + '</span>' +
          '<span class="loc">' + loc + '</span></div>';
      });
      html.push('<h3>' + (EDGE_KIND_LABEL[kind] || kind) + '（' + items.length + '）</h3>' + lis.join(''));
    });

    panelBody.innerHTML = html.join('');
  }

  // locOf 关系的位置：出边的行号在节点自身文件，入边在对方文件
  function locOf(e, node, otherNode) {
    var f = e.source === node.id ? node.file : (otherNode ? otherNode.file : '');
    if (!e.line) return f;
    return f ? f + ':' + e.line : '';
  }

  // kv 键值行
  function kv(k, v) {
    return '<div class="kv"><span class="k">' + k + '</span><span class="v">' + escapeHtml(String(v)) + '</span></div>';
  }

  // resetPanel 无选中时信息栏显示提示（信息栏为常驻侧边栏，不清空画布）
  function closePanel() {
    panelBody.innerHTML = '<p class="doc">单击节点查看详细信息</p>';
  }

  /* ---------- 工具 ---------- */

  // nodeLabel 两行节点标签：
  //   第一行：文件所在目录 + basename（如 orchestrator/orchestrator.go）
  //   第二行：符号名
  // 无文件信息的节点（如 commit）只显示单行符号名。
  function nodeLabel(n) {
    var name = n.name || '';
    var f = n.file || '';
    var parts = f.split('/');
    var line1 = parts.length >= 2
      ? parts[parts.length - 2] + '/' + parts[parts.length - 1]
      : f;
    if (!line1) return name;
    return line1 + '\n' + name;
  }

  function escapeHtml(s) {
    return String(s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }
})();
