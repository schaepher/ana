// 工具函数（原 app.js 1.10 节）：标签、转义、去重等
import { state } from './state.js';

// displayName 符号显示名：方法已是 (T).m；纯函数加包名前缀 (pkg).f
// （与方法的接收者格式一致，接收者位置放包名，从 canonical ID 提取）。
export function displayName(n) {
  if (!n) return '';
  if (n.kind === 'function') {
    var m = /^symbol:go:([^:]+):/.exec(n.id || '');
    if (m) {
      var pkg = m[1].split('/').pop();
      if (pkg) return '(' + pkg + ').' + (n.name || '');
    }
  }
  return n.name || '';
}

// nodeLabel 四行节点标签：
//   第一行：文件所在目录（如 orchestrator）
//   第二行：文件名（如 orchestrator.go）
//   第三行：方法接收者 (T) / 函数包名 (pkg)
//   第四行：方法名 / 函数名
// 无文件信息的节点（如 commit）只显示单行符号名。
export function nodeLabel(n) {
  var name = n.name || '';
  var f = n.file || '';
  var parts = f.split('/');
  var line1 = parts.length >= 2 ? parts[parts.length - 2] : '';
  var line2 = parts.length >= 1 ? parts[parts.length - 1] : f;
  // 拆前缀：方法 (T).m → (T) / m；函数裸名 → 从 id 取包名 (pkg) / 函数名
  var line3 = '';
  var line4 = name;
  var m = /^\(([^)]+)\)\.(.+)$/.exec(name);
  if (m) {
    line3 = '(' + m[1] + ')';
    line4 = m[2];
  } else if (n.kind === 'function') {
    var pm = /^symbol:go:([^:]+):/.exec(n.id || '');
    if (pm) {
      var pkg = pm[1].split('/').pop();
      if (pkg) line3 = '(' + pkg + ')';
    }
  }
  var lines = [];
  if (line1) lines.push(line1);
  if (line2) lines.push(line2);
  if (line3) lines.push(line3);
  lines.push(line4);
  return lines.join('\n');
}

export function escapeHtml(s) {
  return String(s).replace(/[&<>"']/g, function (c) {
    return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
  });
}

export function pushUniq(arr, v) {
  if (arr.indexOf(v) < 0) arr.push(v);
}

// nodeById 从图数据中取节点信息（含原始 API 数据）。
export function nodeById(id) {
  var d = state.graph.getNodeData(id);
  return d && d.data ? d.data.full : null;
}
