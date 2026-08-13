// 入口选择（原 app.js 1.2 节）：加载顶层入口 + 搜索框全库搜索
import { state } from './state.js';
import { addNode } from './graph-ops.js';
import { closePanel } from './panel.js';

// loadEntries 加载全部顶层入口作为搜索数据源（不放入图中）
export function loadEntries() {
  fetch('/api/roots')
    .then(function (res) { return res.json(); })
    .then(function (data) {
      state.allEntries = data.nodes || [];
      state.tip.textContent = '已加载 ' + state.allEntries.length + ' 个顶层入口 · 搜索选择后双击展开依赖';
    })
    .catch(function (err) {
      state.tip.textContent = '加载入口列表失败: ' + err.message;
    });
}

// 输入过滤（防抖 200ms）→ 本地入口 + 全库符号搜索合并渲染
export function bindSearch() {
  state.entryInput.addEventListener('input', function () {
    var q = state.entryInput.value.trim().toLowerCase();
    if (!q) {
      state.entryList.style.display = 'none';
      return;
    }
    clearTimeout(state.searchTimer);
    state.searchTimer = setTimeout(function () { doSearch(q); }, 200);
  });
  state.entryInput.addEventListener('blur', function () {
    setTimeout(function () { state.entryList.style.display = 'none'; }, 150);
  });
}

function doSearch(q) {
  var seq = ++state.searchSeq;
  var matched = state.allEntries.filter(function (e) {
    return (e.name + ' ' + e.id + ' ' + (e.file || '') + ' ' + (e.flags || []).join(' '))
      .toLowerCase().indexOf(q) >= 0;
  });
  // 全库符号搜索补充（入口之外的符号，如任意函数/方法）
  fetch('/api/search?q=' + encodeURIComponent(q))
    .then(function (res) { return res.json(); })
    .then(function (data) {
      if (seq !== state.searchSeq) return; // 丢弃过期结果
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
      if (seq === state.searchSeq) renderEntryList(matched.slice(0, 50));
    });
}

function renderEntryList(items) {
  if (!items.length) {
    state.entryList.style.display = 'none';
    return;
  }
  state.entryList.innerHTML = '';
  items.forEach(function (e) {
    var li = document.createElement('li');
    li.textContent = entryLabel(e);
    li.addEventListener('mousedown', function (evt) {
      evt.preventDefault();
      selectEntry(e);
    });
    state.entryList.appendChild(li);
  });
  state.entryList.style.display = 'block';
}

// 选择入口：清空图，仅展示该节点（双击展开依赖）
export function selectEntry(e) {
  state.entryList.style.display = 'none';
  state.entryInput.value = '';
  resetGraph();
  state.entryRootId = e.id; // 展开树根
  addNode(e);
  // 入口节点置于画布正中：addNode 的网格预置位置在左上角，force 布局
  // 不移动孤立节点，需显式定位
  var w = state.container.clientWidth || 1200;
  var h = state.container.clientHeight || 800;
  state.graph.updateNodeData([{ id: e.id, style: { x: w / 2, y: h / 2 } }]);
  state.graph.layout();
  state.tip.textContent = '已选择 ' + e.name + ' · 双击节点展开依赖';
  closePanel();
}

function entryLabel(e) {
  var label = e.name;
  if (e.file) label += ' · ' + e.file;
  if (e.flags && e.flags.length) label += '  [' + e.flags.join(', ') + ']';
  return label;
}

// resetGraph 清空图数据与全部状态（收起顶层节点后回到入口视图）
function resetGraph() {
  state.expandToken++;
  if (typeof state.graph.stopLayout === 'function') state.graph.stopLayout();
  state.seenNodes.clear();
  state.seenEdges.clear();
  state.expandedMap.clear();
  state.selectedId = null;
  state.graph.setData({ nodes: [], edges: [] });
}
