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
  // 数据节点（字段访问/SSA 值/参数/返回）：追踪此数据在整条链路上的处理
  if (node.kind === 'field_access' || node.kind === 'ssa_value' ||
      node.kind === 'parameter' || node.kind === 'result') {
    html.push('<button id="vt-btn" title="追踪该数据在整条链路（跨函数）上如何被处理">追踪此数据</button>');
  }

  // 基本信息
  var basic = [];
  basic.push(kv('名称', displayName(node)));
  basic.push(kv('类型', KIND_LABEL[node.kind] || node.kind));
  if (node.file) basic.push(kv('文件', node.file + (node.line ? ':' + node.line : '')));
  if (node.signature) basic.push(kv('签名', node.signature));
  if (node.type) basic.push(kv('类型', node.type));
  if (node.fullPath) basic.push(kv('字段路径', node.fullPath));
  if (node.funcName) basic.push(kv('所属函数', node.funcName));
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
    // Q187/Q189：实参来源（passes_result）条目携带来源函数名 + 签名 +
    // 实参名，渲染为"来源签名 → 实参"（来源在左、箭头指向实参）
    var argName = '';
    var srcSig = '';
    if (e.kind === 'passes_result' && e.metadata && e.metadata.arg_name) {
      argName = e.metadata.arg_name;
      if (otherNode && otherNode.signature) {
        srcSig = otherNode.signature.replace(/^func\s+/, '');
      }
    }
    byKind[e.kind].push({
      id: other,
      dir: e.source === node.id ? '出' : '入',
      name: otherNode ? otherNode.name : other,
      argName: argName,
      srcSig: srcSig,
      type: otherNode ? otherNode.type : '',
      file: otherNode ? otherNode.file : '',
      line: e.line
    });
  });
  REL_ORDER.concat(restOrder).forEach(function (kind) {
    var items = byKind[kind];
    if (!items || !items.length) return;
    if (kind === 'calls') return; // 调用分组最后渲染（被调用/调用置底）

    if (kind === 'has_method') {
      // 方法线（接收者 → 方法）按视角分组：struct 节点视角=出边
      // （它的方法们），方法节点视角=入边（指向它的接收者类型）
      var out = items.filter(function (g) { return g.dir === '出'; });
      var inn = items.filter(function (g) { return g.dir === '入'; });
      if (out.length) { state.panelGroupNodes[gi] = out.map(function (g) { return g.id; }); html.push(relGroupHtml('方法（' + out.length + '）', out, gi++)); }
      // Q184：接收者类型（has_method 入边）不单独成组——与 has_param 的
      // 接收者变量合并为一个分组；类型由 receiver 节点 type 显示
      // （Q186：→m · *Manager，与参数/返回一致的 ftype 展示）
      return;
    }
    if (kind === 'has_param') {
      // 参数分组：receiver（kind=receiver）与普通参数区分成两组，
      // 接收者在前（index=-1，与图布局一致）；Q188 参数表格化
      var recvs = items.filter(function (g) { return byId[g.id] && byId[g.id].kind === 'receiver'; });
      var params = items.filter(function (g) { return !(byId[g.id] && byId[g.id].kind === 'receiver'); });
      if (recvs.length) {
        state.panelGroupNodes[gi] = recvs.map(function (g) { return g.id; });
        html.push(relGroupHtml('接收者（' + recvs.length + '）', recvs, gi++));
      }
      if (params.length) html.push('<h3>参数（' + params.length + '）</h3>' + paramTableHtml(params));
      return;
    }
    if (kind === 'has_result') {
      // Q188：返回分组表格化（名称 | 类型）
      html.push('<h3>返回（' + items.length + '）</h3>' + paramTableHtml(items));
      return;
    }
    if (kind === 'dispatch_to') {
      // 动态派发候选（Q95）：接口视角=候选实现（出边）、实现视角=被派发（入边）
      var dout = items.filter(function (g) { return g.dir === '出'; });
      var din = items.filter(function (g) { return g.dir === '入'; });
      if (dout.length) { state.panelGroupNodes[gi] = dout.map(function (g) { return g.id; }); html.push(relGroupHtml('候选实现（' + dout.length + '）', dout, gi++)); }
      if (din.length) { state.panelGroupNodes[gi] = din.map(function (g) { return g.id; }); html.push(relGroupHtml('被派发（' + din.length + '）', din, gi++)); }
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
  // 调用分组置底：在所有关系分组之后渲染。出=该节点调用（callee），
  // 入=调用该节点（caller）；组内 caller（被调用）在上，与图布局一致
  var callItems = byKind['calls'];
  if (callItems && callItems.length) {
    var out = callItems.filter(function (g) { return g.dir === '出'; });
    var inn = callItems.filter(function (g) { return g.dir === '入'; });
    if (inn.length) { state.panelGroupNodes[gi] = inn.map(function (g) { return g.id; }); html.push(relGroupHtml('被调用（' + inn.length + '）', inn, gi++)); }
    if (out.length) { state.panelGroupNodes[gi] = out.map(function (g) { return g.id; }); html.push(relGroupHtml('调用（' + out.length + '）', out, gi++)); }
  }

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
// Q180 可读性：同一字段同一行的读/写合并为 [读/写]（t.A = t.A + 1 的
// 读改写不再拆两行）；值节点（ssa_value）行号来自节点 line_start
// （← data_flows_to t (10)——t 在哪定义一目了然）。
function renderFlowsGroup(flows, dir, title) {
  var rows = flows.filter(function (f) { return f.dir === dir; });
  if (!rows.length) return '';
  // 字段访问节点按 name+line 合并 access（去重保序）
  var fieldAcc = {};
  var fieldSeen = {};
  rows.forEach(function (f) {
    if (f.kind !== 'field_access') return;
    var key = f.name + '|' + f.line;
    if (!fieldAcc[key]) fieldAcc[key] = [];
    if (fieldAcc[key].indexOf(f.access) < 0) fieldAcc[key].push(f.access);
  });
  var html = ['<h4>' + title + '</h4>', '<pre class="flows">'];
  rows.forEach(function (f) {
    var arrow = dir === 0 ? '←' : '→';
    var label = f.edgeKind ? arrow + ' ' + f.edgeKind : '';
    var line = f.line ? ' (' + f.line + ')' : '';
    if (f.kind === 'field_access') {
      var key = f.name + '|' + f.line;
      if (fieldSeen[key]) return; // 已在首行合并输出
      fieldSeen[key] = true;
      var acc = fieldAcc[key].map(function (a) { return a === 'read' ? '读' : '写'; }).join('/');
      html.push('  ' + f.name + ' [' + acc + ']' + line);
    } else {
      html.push(new Array(f.depth * 2 + 1).join(' ') + label + ' ' + f.name + line);
    }
  });
  html.push('</pre>');
  return html.join('');
}

// shortType 短类型名：*github.com/...Manager → *Manager、context.Context →
// Context（保留 * 前缀；Q186 参数/返回条目显示）。
function shortType(t) {
  if (!t) return '';
  var star = '';
  var s = t;
  while (s[0] === '*') { star += '*'; s = s.slice(1); }
  var parts = s.split('.');
  return star + parts[parts.length - 1];
}

// paramTableHtml 参数/返回分组表格（Q188）：名称 | 类型 两列表格，
// 复用 .fields 样式；无类型时类型列留空。
function paramTableHtml(items) {
  if (!items.length) return '';
  var rows = items.map(function (g) {
    var t = g.type ? shortType(g.type) : '';
    return '<tr><td>' + escapeHtml(g.name) + '</td>' +
      '<td class="ftype">' + escapeHtml(t) + '</td></tr>';
  }).join('');
  return '<table class="fields"><thead><tr><th>名称</th><th>类型</th></tr></thead><tbody>' +
    rows + '</tbody></table>';
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
      // Q187：实参来源条目"来源 → 实参"（来源在左、箭头指向实参）；
      // Q189：来源函数带完整签名（lastUserMessage(msgs ...) string → userMessage）；
      // Q186：参数/返回条目显示"名称 · 短类型"（无类型的分组不受影响）
      var typeHtml = g.type ? '<span class="ftype"> · ' + escapeHtml(shortType(g.type)) + '</span>' : '';
      var entry;
      if (g.argName) {
        // Q190：签名本身含函数名（lastUserMessage(msgs ...) string）——
        // 箭头两侧留白、实参名加粗突出"指向"（→ userMessage）
        var src = g.srcSig || g.name;
        entry = '<span class="name">' + escapeHtml(src) + '</span>' +
          '<span class="dir"> → </span><span class="name arg" style="font-weight:600">' +
          escapeHtml(g.argName) + '</span>';
      } else {
        entry = '<span class="dir">' + (g.dir === '出' ? '→' : '←') + '</span>' +
          '<span class="name">' + escapeHtml(g.name) + '</span>' + typeHtml;
      }
      out.push('<div class="rel">' + entry + '<span class="loc">' + loc + '</span></div>');
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

// loadValueTrace 拉取数据值全链（/api/value-trace）并按函数上下文分组渲染：
// 每个函数一个分组，组内行 = 方向 + 边类型 + 节点 + [读/写] + 行号。
export function loadValueTrace(nodeId) {
  var old = document.getElementById('vt-panel');
  if (old) old.remove();
  fetch('/api/value-trace?id=' + encodeURIComponent(nodeId))
    .then(function (r) { return r.ok ? r.json() : Promise.reject(new Error('HTTP ' + r.status)); })
    .then(function (data) {
      var flows = data.flows || [];
      var html = ['<div id="vt-panel"><h3>数据流全链（函数上下文）</h3>'];
      if (!flows.length) {
        html.push('<p class="doc">无数据流（节点不存在或无数据流边）</p>');
      } else {
        var cur = null;
        flows.forEach(function (f) {
          if (f.funcName !== cur) {
            cur = f.funcName;
            html.push('<h4>&#12304;' + escapeHtml(f.funcName || '（未知函数）') + '&#12305;</h4>');
          }
          var arrow = f.dir === 0 ? '←' : '→';
          var label = f.edgeKind ? arrow + ' ' + f.edgeKind : '';
          var acc = f.kind === 'field_access' ? (f.access === 'read' ? ' [读]' : ' [写]') : '';
          var line = f.line ? ':' + f.line : '';
          var cond = (f.conditions && f.conditions.length) ? ' [条件: ' + f.conditions.join('; ') + ']' : '';
          html.push('<div class="vt-row" style="padding-left:' + (f.depth * 12 + 4) + 'px">' +
            escapeHtml(label + ' ' + f.name + acc + line + cond) + '</div>');
        });
      }
      html.push('</div>');
      var el = document.createElement('div');
      el.innerHTML = html.join('');
      state.panelBody.appendChild(el);
    })
    .catch(function (err) {
      console.error('load value-trace:', err);
      var el = document.createElement('div');
      el.id = 'vt-panel';
      el.innerHTML = '<h3>数据流全链</h3><p class="doc">加载失败：' + escapeHtml(String(err)) + '</p>';
      state.panelBody.appendChild(el);
    });
}
