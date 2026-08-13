// 入口模块：创建 G6 graph、绑定交互/配置、加载入口（重构后主入口）
import {
  state, KIND_COLOR, FLAG_COLOR, EDGE_KIND_LINE, EDGE_KIND_LABEL,
  EDGE_OUT_COLOR, EDGE_IN_COLOR, EDGE_DEFAULT_COLOR
} from './state.js';
import { loadEntries, bindSearch } from './search.js';
import { bindInteractions } from './interact.js';
import { bindPanelActions } from './panel-actions.js';
import { bindConfig } from './config.js';

// 创建 G6 图：节点/边样式函数、force 布局、画布行为
// 注意：边样式函数读闭包变量 state.selectedId，绘制时求值；
// 选中变化后由 setElementState 触发全图重渲染重算
state.graph = new G6.Graph({
  container: state.container,
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
      // 颜色跟随选中节点：选中节点的出边蓝、入边红，其余黑色
      var stroke = EDGE_DEFAULT_COLOR;
      if (state.selectedId) {
        if (d.source === state.selectedId) stroke = EDGE_OUT_COLOR;
        else if (d.target === state.selectedId) stroke = EDGE_IN_COLOR;
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

state.graph.render();
// 调试/自动化钩子：暴露 graph 实例与展开记录供 playwright 等检查
window.__codeintelGraph = state.graph;
window.__codeintelExpanded = state.expandedMap;

// 绑定各模块
bindInteractions();
bindPanelActions();
bindConfig();
bindSearch();
loadEntries();
