// 信息栏渲染（原 app.js 1.8 节）：分组展示节点信息（基本/字段/文档/
// 提交/关系），关系按 kind 与文件分组、方向拆分
import { state, KIND_LABEL, FLAG_LABEL, REL_ORDER, EDGE_KIND_LABEL } from './state.js';
import { displayName, escapeHtml } from './utils.js';

// showNodePanel 单击节点：复用 /api/expand 取节点的完整关系后渲染信息栏
export function showNodePanel(id) {
  fetch('/api/expand?id=' + encodeURIComponent(id))
    .then(function (res) {
      if (!res.ok) throw new Error('HTTP ' + res.status);
      return res.json();
    })
    .then(function (data) { renderNodePanel(data); })
    .catch(function (err) {
      state.panelBody.innerHTML = '<p class="doc">加载节点信息失败: ' + escapeHtml(err.message) + '</p>';
    });
}

// renderNodePanel 渲染信息栏，分组：基本信息 / 字段 / 文档注释 / 提交 /
// 关系（按类型与文件分组、方向拆分）；panelGroupNodes[gi] 记录分组
// 涉及的对方节点 id（[隐藏]/[展开] 按钮用）
export function renderNodePanel(data) {
  var node = data.node;
  if (!node) return;
  state.currentPanelId = node.id;
  var edges = data.edges || [];
  var byId = {};
  (data.neighbors || []).forEach(function (n) { byId[n.id] = n; });
  var html = [];

  // 函数/方法：Source Code 按钮（弹窗展示源码）+ 字段数据流按钮
  if (node.kind === 'function' || node.kind === 'method') {
    html.push('<button id="source-btn">Source Code</button>');
    html.push('<button id="flows-btn" title="查看该函数内的字段数据流（字段读写点与 def-use 链）">字段数据流</button>');
  }

  // 基本信息
  var basic = [];
  basic.push(kv('名称', displayName(node)));
  basic.push(kv('类型', KIND_LABEL[node.kind] || node.kind));
  if (node.file) basic.push(kv('文件', node.file + (node.line ? ':' + node.line : '')));
  if (node.signature) basic.push(kv('签名', node.signature));
  if (node.type) basic.push(kv('类型', node.type));
  if (node.fullPath) basic.push(kv('字段路径', node.fullPath));
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

  // 关系：按类型分组；panelGroupNodes[gi] = 该分组涉及的对方节点 id。
  // 同时缓存完整邻居/边数据供 [展开] 按钮"只显示一层"使用
  var byKind = {};
  var restOrder = [];
  state.panelGroupNodes = {};
  state.panelNeighbors = byId;
  state.panelEdges = edges;
  var gi = 0;
  edges.forEach(function (e) {
    if (!byKind[e.kind]) {
      byKind[e.kind] = [];
      if (REL_ORDER.indexOf(e.kind) < 0) restOrder.push(e.kind);
    }
    var other = e.source === node.id ? e.target : e.source;
    var otherNode = byId[other];
    byKind[e.kind].push({
      id: other,
      dir: e.source === node.id ? '出' : '入',
      name: otherNode ? otherNode.name : other,
      file: otherNode ? otherNode.file : '',
      line: e.line
    });
  });
  REL_ORDER.concat(restOrder).forEach(function (kind) {
    var items = byKind[kind];
    if (!items || !items.length) return;
    if (kind === 'calls') {
      // 调用拆分为 caller/callee 两组：出=该节点调用（callee），
      // 入=调用该节点（caller）；caller（被调用）在上，与图布局一致
      var out = items.filter(function (g) { return g.dir === '出'; });
      var inn = items.filter(function (g) { return g.dir === '入'; });
      if (inn.length) { state.panelGroupNodes[gi] = inn.map(function (g) { return g.id; }); html.push(relGroupHtml('被调用（' + inn.length + '）', inn, gi++)); }
      if (out.length) { state.panelGroupNodes[gi] = out.map(function (g) { return g.id; }); html.push(relGroupHtml('调用（' + out.length + '）', out, gi++)); }
      return;
    }
    if (kind === 'has_method') {
      // 方法线（接收者 → 方法）按视角分组：struct 节点视角=出边
      // （它的方法们），方法节点视角=入边（指向它的接收者）
      var out = items.filter(function (g) { return g.dir === '出'; });
      var inn = items.filter(function (g) { return g.dir === '入'; });
      if (out.length) { state.panelGroupNodes[gi] = out.map(function (g) { return g.id; }); html.push(relGroupHtml('方法（' + out.length + '）', out, gi++)); }
      if (inn.length) { state.panelGroupNodes[gi] = inn.map(function (g) { return g.id; }); html.push(relGroupHtml('接收者（' + inn.length + '）', inn, gi++)); }
      return;
    }
    if (kind === 'implements') {
      // 实现线（接口 → 实现者）按视角分组：接口节点视角=出边（实现者们），
      // 实现者节点视角=入边（它实现的接口）
      var out = items.filter(function (g) { return g.dir === '出'; });
      var inn = items.filter(function (g) { return g.dir === '入'; });
      if (out.length) { state.panelGroupNodes[gi] = out.map(function (g) { return g.id; }); html.push(relGroupHtml('实现者（' + out.length + '）', out, gi++)); }
      if (inn.length) { state.panelGroupNodes[gi] = inn.map(function (g) { return g.id; }); html.push(relGroupHtml('实现（' + inn.length + '）', inn, gi++)); }
      return;
    }
    items.sort(function (a, b) { return a.dir === b.dir ? 0 : (a.dir === '出' ? -1 : 1); });
    state.panelGroupNodes[gi] = items.map(function (g) { return g.id; });
    html.push(relGroupHtml((EDGE_KIND_LABEL[kind] || kind) + '（' + items.length + '）', items, gi++));
  });

  state.panelBody.innerHTML = html.join('');
}

// loadNodeFlows 拉取函数内字段数据流（/api/flows）并渲染文本树：
// 产生链（← 反向：值从哪来）/ 使用链（→ 正向：值流向哪）。
export function loadNodeFlows(nodeId) {
  var old = document.getElementById('flows-panel');
  if (old) old.remove();
  fetch('/api/flows?id=' + encodeURIComponent(nodeId))
    .then(function (r) { return r.ok ? r.json() : Promise.reject(new Error('HTTP ' + r.status)); })
    .then(function (data) {
      var flows = data.flows || [];
      var html = ['<div id="flows-panel"><h3>字段数据流</h3>'];
      if (!flows.length) {
        html.push('<p class="doc">无字段访问（该函数无结构体字段读写，或未构建 SSA 字段追溯）</p>');
      } else {
        html.push(renderFlowsGroup(flows, 0, '产生链（← 反向）'));
        html.push(renderFlowsGroup(flows, 1, '使用链（→ 正向）'));
      }
      html.push('</div>');
      var el = document.createElement('div');
      el.innerHTML = html.join('');
      state.panelBody.appendChild(el);
    })
    .catch(function (err) {
      console.error('load flows:', err);
      var el = document.createElement('div');
      el.id = 'flows-panel';
      el.innerHTML = '<h3>字段数据流</h3><p class="doc">加载失败：' + escapeHtml(String(err)) + '</p>';
      state.panelBody.appendChild(el);
    });
}

// renderFlowsGroup 渲染单向数据流树（缩进 + 边类型 + 节点名 + 行号）。
function renderFlowsGroup(flows, dir, title) {
  var rows = flows.filter(function (f) { return f.dir === dir; });
  if (!rows.length) return '';
  var html = ['<h4>' + title + '</h4>', '<pre class="flows">'];
  rows.forEach(function (f) {
    var arrow = dir === 0 ? '←' : '→';
    var label = f.edgeKind ? arrow + ' ' + f.edgeKind : '';
    var access = f.kind === 'field_access' ? (f.access === 'read' ? ' [读]' : ' [写]') : '';
    var line = f.line ? ' (' + f.line + ')' : '';
    html.push(new Array(f.depth * 2 + 1).join(' ') + label + ' ' + f.name + access + line);
  });
  html.push('</pre>');
  return html.join('');
}

// relGroupHtml 关系分组：标题（含 [隐藏]/[展开] 按钮）+ 按对方节点文件
// 路径分组的条目列表（组头为文件路径，条目显示方向 →/←、对方节点、行号）
function relGroupHtml(title, items, gi) {
  var byFile = {};
  items.forEach(function (g) {
    var f = g.file || '（未知）';
    if (!byFile[f]) byFile[f] = [];
    byFile[f].push(g);
  });
  var out = ['<h3>' + title +
    ' <button class="hide-group-btn" data-gi="' + gi + '" title="隐藏该分组节点（已展开的保留）">隐藏</button>' +
    ' <button class="expand-group-btn" data-gi="' + gi + '" title="把该分组节点显示到图上（只一层，不展开关系）">展开</button></h3>'];
  Object.keys(byFile).forEach(function (f) {
    out.push('<div class="file-group">' + escapeHtml(f) + '</div>');
    byFile[f].forEach(function (g) {
      var loc = g.line ? ' · :' + g.line : '';
      out.push('<div class="rel"><span class="dir">' + (g.dir === '出' ? '→' : '←') + '</span>' +
        '<span class="name">' + escapeHtml(g.name) + '</span>' +
        '<span class="loc">' + loc + '</span></div>');
    });
  });
  return out.join('');
}

// kv 键值行
function kv(k, v) {
  return '<div class="kv"><span class="k">' + k + '</span><span class="v">' + escapeHtml(String(v)) + '</span></div>';
}

// closePanel 无选中时信息栏显示提示（信息栏为常驻侧边栏，不清空画布）
export function closePanel() {
  state.panelBody.innerHTML = '<p class="doc">单击节点查看详细信息</p>';
}
