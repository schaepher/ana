// 交互与选中（原 app.js 1.7 节）：单击选中染色、双击展开/收起、空白清除
import { state } from './state.js';
import { expandNode } from './expand.js';
import { collapseNode } from './collapse.js';
import { showNodePanel, closePanel } from './panel.js';

// selectNode 选中节点：先更新 selectedId 再触发状态变化重渲染（与
// clearSelection 相同的顺序原则）。G6 v5 的 setElementState 是异步绘制
// （内部 await element.draw），且绘制时才会重算样式函数（读闭包
// selectedId）：若先调用 setElementState(旧节点, []) 再更新 selectedId，
// 前一次异步绘制会按旧选中节点染色并在之后完成，覆盖新染色——表现为
// 直接点其他节点时边色不重置。先更新参照变量，保证每次重渲染都按新
// 选中节点求值。
export function selectNode(id) {
  if (state.selectedId === id) return;
  var prev = state.selectedId;
  state.selectedId = id;
  if (prev) state.graph.setElementState(prev, []);
  state.graph.setElementState(id, ['selected']);
}

// 取消选中：必须先置空 selectedId 再触发状态变化重渲染（顺序颠倒
// 会导致渲染仍按旧选中节点染色）。
export function clearSelection() {
  if (!state.selectedId) return;
  var prev = state.selectedId;
  state.selectedId = null;
  state.graph.setElementState(prev, []);
}

// bindInteractions 绑定图事件（由 main.js 调用）
export function bindInteractions() {
  // 单击节点：选中并以它为参照染色（出边蓝/入边红），右侧信息栏展示详情
  state.graph.on('node:click', function (evt) {
    var id = evt.target.id;
    if (!id) return;
    selectNode(id);
    showNodePanel(id);
  });

  // 单击空白：取消选中，边恢复黑色，关闭信息栏
  state.graph.on('canvas:click', function () {
    clearSelection();
    closePanel();
  });

  // 双击：已展开则收起（只收一层），否则展开
  state.graph.on('node:dblclick', function (evt) {
    var id = evt.target.id;
    if (!id) return;
    if (state.expandedMap.has(id)) {
      // 先取名字：顶层节点收起会清空图，之后再查会找不到节点
      var d = state.graph.getNodeData(id);
      var name = d && d.data && d.data.full ? d.data.full.name : null;
      collapseNode(id);
      state.tip.textContent = '已收起' + (name ? ' ' + name : '') + ' · 双击可重新展开';
    } else {
      expandNode(id);
    }
  });
}
