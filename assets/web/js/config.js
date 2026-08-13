// 配置与图例（原 app.js 1.9 节）：隐藏规则下拉、节点类型图例、侧栏拖拽
import { state, KIND_COLOR, KIND_LABEL, EDGE_KIND_LABEL, HIDE_OPTIONS } from './state.js';
import { relayoutTree } from './layout-tree.js';
import { treeRoot } from './expand.js';

// bindConfig 绑定配置 UI（main.js 调用）：
// 节点类型颜色图例、隐藏规则（localStorage 持久化）、侧栏拖拽宽度
export function bindConfig() {
  bindKindLegend();
  bindHideLegend();
  bindResize();
}

// 节点类型颜色图例（右上角下拉）
function bindKindLegend() {
  var kindLegend = document.getElementById('kind-legend');
  var html = '';
  Object.keys(KIND_COLOR).forEach(function (k) {
    html += '<div class="ki"><i class="dot" style="background:' + KIND_COLOR[k] + '"></i>' +
      (KIND_LABEL[k] || k) + '</div>';
  });
  kindLegend.innerHTML = html;
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
