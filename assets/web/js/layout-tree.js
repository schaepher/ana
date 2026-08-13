// 树布局 relayoutTree（原 app.js 1.6 节）：
// BFS 深度 → tail 定位（含悬浮分支锚点传播）→ 边方向修正 → rowY 分配
import { state } from './state.js';
import { isUp } from './layout.js';

// relayoutTree 对展开树做方向感知的层级布局：以根为中间行，child 通过
// 任意关系指向 parent（isUp）排上一行，否则下一行；每行水平均匀分布。
// 增量布局：prevY（id → y）提供展开前的行位置——已有节点保持原 y，新
// 节点所在行在相邻已知行之间插值；有 tail 节点（悬浮分支/共享）或深度
// 被修正时 prevY 已错位，按深度干净分层。
export function relayoutTree(rootId, prevY) {
  var data = state.graph.getData();
  if (!data.nodes.some(function (n) { return n.id === rootId; })) return;
  var nodeSet = new Set(data.nodes.map(function (n) { return n.id; }));
  // BFS 计算每节点行号：根=0，child 通过任意关系指向 parent 则上一行
  var depths = new Map([[rootId, 0]]);
  var queue = [rootId];
  while (queue.length) {
    var pid = queue.shift();
    var rec = state.expandedMap.get(pid);
    if (!rec) continue;
    var pd = depths.get(pid);
    rec.nodes.forEach(function (cid) {
      if (!nodeSet.has(cid) || depths.has(cid)) return; // 已分配/已删节点跳过
      depths.set(cid, pd + (isUp(pid, cid) ? -1 : 1));
      queue.push(cid);
    });
  }
  // 未在展开树中的节点（悬浮分支/共享节点）：第一轮与已分层节点按边
  // 定位；剩余多分支锚点传播相对深度（每分支锚点当前最大深度 +2，避免
  // 与主树碰撞）。有 tail 节点即 suspended——旧 prevY 与新深度错位
  // （会倒挂），rowY 分配放弃 prevY 按深度干净分层。
  var tailSet = new Set();
  data.nodes.forEach(function (n) {
    if (!depths.has(n.id)) tailSet.add(n.id);
  });
  var suspended = tailSet.size > 0;
  var progressed = true;
  while (tailSet.size && progressed) {
    progressed = false;
    tailSet.forEach(function (tid) {
      var e = (data.edges || []).find(function (x) {
        return (x.source === tid || x.target === tid) &&
          depths.has(x.source === tid ? x.target : x.source);
      });
      if (!e) return;
      var other = e.source === tid ? e.target : e.source;
      var od = depths.get(other);
      // 箭头始终向下：我是边的 source（caller/接收者/接口）→ 上一行，
      // 我是 target → 下一行（适用于所有关系类型）
      depths.set(tid, e.source === tid ? od - 1 : od + 1);
      tailSet.delete(tid);
      progressed = true;
    });
  }
  while (tailSet.size) {
    var maxD0 = 0;
    depths.forEach(function (d) { if (d > maxD0) maxD0 = d; });
    var anchor = null;
    tailSet.forEach(function (tid) {
      if (anchor === null) {
        depths.set(tid, maxD0 + 2);
        tailSet.delete(tid);
        anchor = tid;
      }
    });
    var progressed2 = true;
    while (tailSet.size && progressed2) {
      progressed2 = false;
      tailSet.forEach(function (tid) {
        var e = (data.edges || []).find(function (x) {
          return (x.source === tid || x.target === tid) &&
            depths.has(x.source === tid ? x.target : x.source);
        });
        if (!e) return;
        var other = e.source === tid ? e.target : e.source;
        var od = depths.get(other);
        depths.set(tid, e.source === tid ? od - 1 : od + 1);
        tailSet.delete(tid);
        progressed2 = true;
      });
    }
  }
  // 边方向修正：箭头始终向下——对图中所有边，source 深度必须小于
  // target 深度（处理共享节点方向冲突）。循环修正直至收敛。
  var pass = 0;
  var depthChanged = false;
  while (pass++ < 50) {
    var changed = false;
    (data.edges || []).forEach(function (e) {
      if (!depths.has(e.source) || !depths.has(e.target)) return;
      var ds = depths.get(e.source), dt = depths.get(e.target);
      if (ds >= dt) {
        depths.set(e.source, dt - 1);
        changed = true;
        depthChanged = true;
      }
    });
    if (!changed) break;
  }
  // 按行号分组
  var rows = new Map();
  depths.forEach(function (d, nid) {
    if (!rows.has(d)) rows.set(d, []);
    rows.get(d).push(nid);
  });
  var tail = Array.from(tailSet);
  if (tail.length) {
    var maxD = 0;
    depths.forEach(function (d) { if (d > maxD) maxD = d; });
    rows.set(maxD + 1, tail);
  }
  var w = state.container.clientWidth || 1200;
  var h = state.container.clientHeight || 800;
  var cx = w / 2;
  var rowGap = 140;
  var startY = 80;
  // 行 y：已有节点（prevY）所在行优先取原 y；其余深度行插值
  var rowY = new Map();
  var minD = Infinity;
  depths.forEach(function (d) { if (d < minD) minD = d; });
  if (depthChanged || suspended) {
    // 深度被修正/悬浮分支：prevY 与新深度错位（如 BuildMeta 从 1 行提到
    // 0 行但 prevY 还是旧行；main 旧位置在底部、新深度在顶部）——放弃
    // "已有节点不动"，整树按新深度干净分层
    depths.forEach(function (d) {
      if (!rowY.has(d)) rowY.set(d, startY + (d - minD) * rowGap);
    });
  } else {
    depths.forEach(function (d, nid) {
      if (prevY && prevY[nid] !== undefined && !rowY.has(d)) rowY.set(d, prevY[nid]);
    });
    var known = [];
    rowY.forEach(function (_, d) { known.push(d); });
    known.sort(function (a, b) { return a - b; });
    depths.forEach(function (d) {
      if (rowY.has(d)) return;
      var lo = null, hi = null;
      for (var i = 0; i < known.length; i++) {
        if (known[i] < d) lo = known[i];
        else if (known[i] > d) { hi = known[i]; break; }
      }
      if (lo !== null && hi !== null) {
        rowY.set(d, rowY.get(lo) + (d - lo) / (hi - lo) * (rowY.get(hi) - rowY.get(lo)));
      } else if (hi !== null) {
        rowY.set(d, rowY.get(hi) - (hi - d) * rowGap); // 顶部扩展（可能超出画布）
      } else if (lo !== null) {
        rowY.set(d, rowY.get(lo) + (d - lo) * rowGap); // 底部扩展
      } else {
        rowY.set(d, startY + (d - minD) * rowGap); // 全量布局：按深度分层
      }
    });
  }
  // tail 行（未定位节点）不在 depths，rowY 未分配——补在最大行下方
  if (tail.length && !rowY.has(maxD + 1)) {
    var maxYv = -Infinity;
    rowY.forEach(function (yv) { if (yv > maxYv) maxYv = yv; });
    rowY.set(maxD + 1, maxYv === -Infinity ? startY : maxYv + rowGap);
  }
  var updates = [];
  rows.forEach(function (ids, d) {
    var y = Math.round(rowY.get(d));
    var spacing = Math.min(180, Math.max(90, (w - 120) / ids.length));
    var x0 = cx - ((ids.length - 1) * spacing) / 2;
    ids.forEach(function (nid, i) {
      updates.push({ id: nid, style: { x: x0 + i * spacing, y: y } });
    });
  });
  state.graph.updateNodeData(updates);
}
