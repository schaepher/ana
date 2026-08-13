// 图数据增量操作（原 app.js 1.3 节）：addNode / addEdge
import { state } from './state.js';
import { nodeLabel } from './utils.js';

// addNode 加入节点：seenNodes 去重；预置网格初始位置（G6 v5 force 布局
// 不处理孤立节点与增量新节点，无初始位置的节点会堆在原点）。
// 固定列数避免 sqrt 回绕导致位置重叠。
export function addNode(n) {
  if (state.seenNodes.has(n.id)) return false;
  state.seenNodes.add(n.id);
  var idx = state.seenNodes.size - 1;
  var cols = 4;
  state.graph.addNodeData([{
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

// addEdge 加入边：seenEdges 去重（key "source→target|kind"）
export function addEdge(e) {
  var key = e.source + '→' + e.target + '|' + e.kind;
  if (state.seenEdges.has(key)) return false;
  state.seenEdges.add(key);
  state.graph.addEdgeData([{
    source: e.source,
    target: e.target,
    data: { kind: e.kind, direction: e.direction }
  }]);
  return true;
}
