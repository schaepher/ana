// 配置与图例（原 app.js 1.9 节）：统一图例下拉、隐藏规则、侧栏拖拽
import {
  state, KIND_COLOR, KIND_LABEL, FLAG_COLOR, FLAG_LABEL,
  EDGE_KIND_LINE, EDGE_KIND_LABEL, HIDE_OPTIONS,
  EDGE_OUT_COLOR, EDGE_IN_COLOR, EDGE_DEFAULT_COLOR
} from './state.js';
import { relayoutTree } from './layout-tree.js';
import { treeRoot } from './expand.js';

// bindConfig 绑定配置 UI（main.js 调用）：
// 统一图例（节点类型/入口标记/连线类型/选中态）、隐藏规则（localStorage）、侧栏拖拽宽度
export function bindConfig() {
  bindKindLegend();
  bindHideLegend();
  bindResize();
}

// 统一图例（右上角「图例 ▾」下拉）：四节
// 1. 节点类型（填充色）2. 入口标记（描边色）3. 连线类型（线型+标注）4. 选中态（出/入/默认边色）
function bindKindLegend() {
  var kindLegend = document.getElementById('kind-legend');
  var html = '';
  // 1. 节点类型
  html += '<div class="lg-sec">节点类型</div>';
  Object.keys(KIND_COLOR).forEach(function (k) {
    html += '<div class="ki"><i class="dot" style="background:' + KIND_COLOR[k] + '"></i>' +
      '<span class="lbl">' + (KIND_LABEL[k] || k) + '</span></div>';
  });
  // 2. 入口标记（描边色方块）
  html += '<div class="lg-sec">入口标记</div>';
  Object.keys(FLAG_COLOR).forEach(function (k) {
    html += '<div class="ki"><i class="sq" style="border:3px solid ' + FLAG_COLOR[k] + '"></i>' +
      '<span class="lbl">' + (FLAG_LABEL[k] || k) + '</span></div>';
  });
  // 3. 连线类型（SVG 线示意：线型 + 箭头 + 标注）
  html += '<div class="lg-sec">连线类型</div>';
  Object.keys(EDGE_KIND_LINE).forEach(function (k) {
    html += '<div class="ki">' + edgeLineSvg(k, '#333') +
      '<span class="lbl">' + (EDGE_KIND_LABEL[k] || k) + '</span></div>';
  });
  // 4. 选中态（选中节点的出/入边颜色与默认色）
  html += '<div class="lg-sec">选中态</div>';
  html += '<div class="ki">' + edgeLineSvg(null, EDGE_OUT_COLOR) + '<span class="lbl">选中节点出边</span></div>';
  html += '<div class="ki">' + edgeLineSvg(null, EDGE_IN_COLOR) + '<span class="lbl">选中节点入边</span></div>';
  html += '<div class="ki">' + edgeLineSvg(null, EDGE_DEFAULT_COLOR) + '<span class="lbl">默认</span></div>';
  kindLegend.innerHTML = html;
  // 图例线示意：按 EDGE_KIND_LINE 线型画一条带箭头短线（选中态传 null kind 用实线）
  function edgeLineSvg(kind, color) {
    var dash = (kind && EDGE_KIND_LINE[kind]) ? EDGE_KIND_LINE[kind].join(',') : '';
    return '<svg width="24" height="10" viewBox="0 0 24 10">' +
      '<line x1="1" y1="5" x2="18" y2="5" stroke="' + color + '" stroke-width="1.5"' +
      (dash ? ' stroke-dasharray="' + dash + '"' : '') + '/>' +
      '<polygon points="21,5 17,2.2 17,7.8" fill="' + color + '"/></svg>';
  }
  document.getElementById('kind-legend-btn').addEventListener('click', function (evt) {
    evt.stopPropagation();
    kindLegend.classList.toggle('hidden');
  });
  document.addEventListener('click', function () {
    kindLegend.classList.add('hidden');
  });
}

// 隐藏规则：勾选展开时隐藏的关系类型（默认仅 calls，localStorage 持久化）
function bindHideLegend() {
  var saved = null;
  try { saved = JSON.parse(localStorage.getItem('codeintel.hideKinds')); } catch (e) {}
  if (Array.isArray(saved) && saved.length) {
    state.hideKinds = new Set(saved.filter(function (k) { return HIDE_OPTIONS.indexOf(k) >= 0; }));
  } else {
    state.hideKinds = new Set(['calls']); // 默认：只隐藏调用关系
  }
  var list = document.getElementById('hide-list');
  HIDE_OPTIONS.forEach(function (k) {
    var label = document.createElement('label');
    var cb = document.createElement('input');
    cb.type = 'checkbox';
    cb.checked = state.hideKinds.has(k);
    cb.addEventListener('change', function () {
      if (cb.checked) state.hideKinds.add(k); else state.hideKinds.delete(k);
      localStorage.setItem('codeintel.hideKinds', JSON.stringify(Array.from(state.hideKinds)));
    });
    label.appendChild(cb);
    label.appendChild(document.createTextNode(EDGE_KIND_LABEL[k] || k));
    list.appendChild(label);
  });
  var hideLegend = document.getElementById('hide-legend');
  document.getElementById('hide-legend-btn').addEventListener('click', function (evt) {
    evt.stopPropagation();
    hideLegend.classList.toggle('hidden');
  });
  document.addEventListener('click', function () {
    hideLegend.classList.add('hidden');
  });
}

// 侧栏拖拽调整宽度（--panel-w 驱动 #main-area 右边界与 #sidepanel 宽度）
function bindResize() {
  var resizeHandle = document.getElementById('panel-resize');
  var rootStyle = document.documentElement.style;
  var PANEL_MIN_W = 240;
  var PANEL_MAX_W = 520;
  var dragState = null;
  resizeHandle.addEventListener('mousedown', function (evt) {
    evt.preventDefault();
    dragState = { startX: evt.clientX, startW: panelWidth() };
    document.body.style.userSelect = 'none';
  });
  window.addEventListener('mousemove', function (evt) {
    if (!dragState) return;
    // 向左拖（clientX 变小）→ 侧栏变宽
    var w = dragState.startW + (dragState.startX - evt.clientX);
    w = Math.max(PANEL_MIN_W, Math.min(PANEL_MAX_W, w));
    rootStyle.setProperty('--panel-w', w + 'px');
  });
  window.addEventListener('mouseup', function () {
    if (!dragState) return;
    dragState = null;
    document.body.style.userSelect = '';
    // 画布宽度变化后重排居中（G6 的 ResizeObserver 会自动缩放画布，
    // 节点坐标不变，重排使内容重新居中于新宽度）
    var root = treeRoot();
    if (root) relayoutTree(root);
  });

  function panelWidth() {
    return parseInt(getComputedStyle(document.documentElement).getPropertyValue('--panel-w')) || 320;
  }
}
