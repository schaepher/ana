// 布局与方向判定（原 app.js 1.6 节）：三行布局、方向分类、边查询
import { state } from './state.js';
import { pushUniq } from './utils.js';

// arrangeLayers 三行排布被展开节点的关联（根/无父展开）：
//   上行：callers（调用该节点的）
//   中间行：该节点 + 非 calls 关联（implements/imports 等）
//   下行：callees（该节点调用的）
export function arrangeLayers(id) {
  var data = state.graph.getData();
  if (!data.nodes.some(function (n) { return n.id === id; })) return;
  var w = state.container.clientWidth || 1200;
  var h = state.container.clientHeight || 800;
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
    var down = e.source === id;
    switch (kind) {
      case 'calls':
      case 'initializes':
      case 'has_method':
      case 'implements':
        // 箭头始终向下：出边（该方法/实现者等 target）→ 下行，
        // 入边（接收者/接口等 source）→ 上行
        if (down) pushUniq(callees, other);
        else pushUniq(callers, other);
        break;
      case 'imports':
        if (down) pushUniq(callers, other); // 导入的包 → 上行
        else pushUniq(callees, other);      // 被导入者 → 下行
        break;
      default: // uses / passes_to / of_type 等对象关系
        pushUniq(others, other);
    }
  });
  var updates = [{ id: id, style: { x: cx, y: cy } }];
  var rowGap = 240;
  var rowWidth = w - 140;
  // 中间行：非 calls 关联水平分布在节点两侧；
  // offsetSingle：单个节点（如接收者）偏移到中心右侧，避免与中心节点重叠
  placeRow(updates, others, cy, rowWidth, cx, 160, true);
  // 上行：callers
  placeRow(updates, callers, cy - rowGap, rowWidth, cx, 90);
  // 下行：callees
  placeRow(updates, callees, cy + rowGap, rowWidth, cx, 90);
  state.graph.updateNodeData(updates);
}

// placeRow 将一组节点水平均匀排布在一行（居中）。
// offsetSingle：仅 1 个节点且与中心节点同行时偏移到中心右侧（避免重叠）。
export function placeRow(updates, ids, y, rowWidth, cx, minSpacing, offsetSingle) {
  if (!ids.length) return;
  var spacing = Math.min(170, Math.max(minSpacing, rowWidth / ids.length));
  var start = cx - ((ids.length - 1) * spacing) / 2;
  if (offsetSingle && ids.length === 1) start = cx + spacing;
  ids.forEach(function (nid, i) {
    updates.push({ id: nid, style: { x: start + i * spacing, y: y } });
  });
}

// edgeKind 返回 parent 与 child 之间第一条边的 kind（无则 null）。
export function edgeKind(parent, child) {
  var data = state.graph.getData();
  var e = (data.edges || []).find(function (x) {
    return (x.source === parent && x.target === child) ||
           (x.source === child && x.target === parent);
  });
  return e ? ((e.data && e.data.kind) || '') : null;
}

// isUp 判断 child 是否通过任意关系指向 parent（child 是边的 source，
// 如 caller / 接收者 / 接口）。树布局行号原则：箭头始终向下——source
// 在 target 上方，适用于所有关系类型（calls/has_method/implements 等）。
export function isUp(parent, child) {
  var data = state.graph.getData();
  return (data.edges || []).some(function (x) {
    return x.source === child && x.target === parent;
  });
}

// rowClass 判断节点 other 相对中心节点 center 的布局方向（与 arrangeLayers
// 相同的分类）："up"=caller 行、"down"=callee 行、"mid"=中间行（对象关系
// uses/passes_to/of_type）；图中无边时返回 null。用于同向剪枝：展开节点时
// 只移除同侧兄弟。
// 注意：树布局（relayoutTree）不用本函数判断上下——implements/imports 虽
// 在三行布局中视为上行依赖，但在链视图中是节点自身的子项，排下一行。
export function rowClass(center, other) {
  var data = state.graph.getData();
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
    case 'has_method':
      // 接口 → 实现者 / 接收者 → 方法：出边（实现者/方法）下行，
      // 入边（接口/接收者）上行——箭头始终向下
      return down ? 'down' : 'up';
    case 'imports':
      return down ? 'up' : 'down';
    default:
      return 'mid';
  }
}

// hasOtherEdge 判断 cid 在移除指定边（edgesToRemove）后是否仍有
// 其他边连接到保留节点（共享节点不删）。
export function hasOtherEdge(cid, id, toRemove, edgesToRemove) {
  var data = state.graph.getData();
  return (data.edges || []).some(function (e) {
    if (e.source === cid || e.target === cid) {
      var other = e.source === cid ? e.target : e.source;
      var k1 = e.source + '→' + e.target + '|' + ((e.data && e.data.kind) || '');
      var k2 = e.target + '→' + e.source + '|' + ((e.data && e.data.kind) || '');
      return other !== id && !toRemove.has(other) &&
        !edgesToRemove.has(k1) && !edgesToRemove.has(k2);
    }
    return false;
  });
}
