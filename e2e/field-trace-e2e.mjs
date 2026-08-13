// codeintel 字段追溯 e2e（playwright）：参数/返回节点展开、信息栏类型、
// 字段数据流按钮与文本树、符号搜索排除。
//
// 运行：make e2e E2E_REPO=<已构建的仓库>（默认 ../radar）
//   或手动：/path/to/codeintel serve --repo <repo> --addr :8096
//           cd e2e && node field-trace-e2e.mjs
import { chromium } from 'playwright';

const BASE = process.env.BASE || 'http://localhost:8096/';
const FN_ID = process.env.FN_ID || 'symbol:go:github.com/schaepher/radar/internal/agent:(Manager).Run';
const PARAM_ID = FN_ID + '#param.ctx';
const RESULT_ID = FN_ID + '#result.0';

const results = [];
let passed = 0, failed = 0;

function check(name, ok, detail) {
  results.push({ name, ok, detail });
  if (ok) passed++; else failed++;
  console.log((ok ? 'PASS' : 'FAIL') + ' ' + name + (detail ? ' — ' + detail : ''));
}

const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1500, height: 940 } });
page.on('pageerror', (e) => console.log('[pageerror]', e.message));
await page.goto(BASE, { waitUntil: 'networkidle' });
await page.waitForTimeout(1500);

const api = (path) => page.evaluate(async (p) => {
  const res = await fetch(p);
  return res.json();
}, path);

// 1. /api/expand：参数/返回边与节点（后端）
const exp = await api('/api/expand?id=' + encodeURIComponent(FN_ID));
check('expand 返回 has_param + has_result 边',
  exp.edges.some((e) => e.kind === 'has_param') && exp.edges.some((e) => e.kind === 'has_result'),
  'edges=' + exp.edges.map((e) => e.kind).join(','));
const hasParam = exp.neighbors.some((n) => n.kind === 'parameter' && n.type);
const hasResult = exp.neighbors.some((n) => n.kind === 'result' && n.type);
check('expand 返回 parameter/result 节点（带 type）', hasParam && hasResult,
  'neighbors=' + exp.neighbors.map((n) => n.kind + ':' + (n.type || '?')).join(','));

// 2. /api/flows：函数内字段数据流
const flows = await api('/api/flows?id=' + encodeURIComponent(FN_ID));
check('flows 非空且含 field_access',
  (flows.flows || []).length > 0 && flows.flows.some((f) => f.kind === 'field_access'),
  'flows=' + (flows.flows || []).length);

// 3. /api/search：符号搜索排除 field_access / ssa_value
const search = await api('/api/search?q=cfg');
check('符号搜索排除字段追溯内部节点',
  (search.nodes || []).every((n) => n.kind !== 'field_access' && n.kind !== 'ssa_value'),
  'nodes=' + (search.nodes || []).map((n) => n.kind).join(','));

// 4. UI：注入函数节点 + 邻居 → 画布出现参数/返回节点
await page.evaluate(async (fnId) => {
  const g = window.__codeintelGraph;
  const res = await fetch('/api/expand?id=' + encodeURIComponent(fnId));
  const d = await res.json();
  const nodes = [{ id: d.node.id, data: { label: d.node.name, kind: d.node.kind } }];
  d.neighbors.forEach((n) => nodes.push({ id: n.id, data: { label: n.name, kind: n.kind } }));
  g.addNodeData(nodes);
  const edges = d.edges.map((e, i) => ({
    id: fnId + '-e' + i, source: e.source, target: e.target, data: { kind: e.kind }
  }));
  g.addEdgeData(edges);
  g.draw();
}, FN_ID);
await page.waitForTimeout(1000);
const kindsOnGraph = await page.evaluate(() => window.__codeintelGraph.getData().nodes.map((n) => n.data.kind));
check('画布出现 parameter 与 result 节点',
  kindsOnGraph.includes('parameter') && kindsOnGraph.includes('result'),
  'kinds=' + kindsOnGraph.join(','));

// 5. 节点配色（渲染期样式）
const styleOf = (id) => page.evaluate((i) => {
  const g = window.__codeintelGraph;
  try {
    const s = g.getElementRenderStyle(i);
    return s ? s.fill : null;
  } catch (err) {
    return null;
  }
}, id);
const paramColor = await styleOf(PARAM_ID);
const resultColor = await styleOf(RESULT_ID);
check('参数节点金色 #d48806', paramColor === '#d48806', 'fill=' + paramColor);
check('返回节点粉色 #f759ab', resultColor === '#f759ab', 'fill=' + resultColor);

// 6. 信息栏类型：单击参数节点
await page.evaluate((id) => window.__codeintelGraph.emit('node:click', { target: { id } }), PARAM_ID);
await page.waitForTimeout(800);
const panelText = await page.evaluate(() => document.getElementById('panel-body').textContent);
check('信息栏显示参数类型', panelText.includes('类型') && panelText.includes('context.Context'),
  'panel=' + panelText.slice(0, 80).replace(/\n/g, ' '));

// 7. 字段数据流按钮 + 文本树：单击方法节点 → 点按钮 → 产生链/使用链
await page.evaluate((id) => window.__codeintelGraph.emit('node:click', { target: { id } }), FN_ID);
await page.waitForTimeout(800);
const hasBtn = await page.evaluate(() => !!document.getElementById('flows-btn'));
check('信息栏有"字段数据流"按钮', hasBtn);
if (hasBtn) {
  await page.click('#flows-btn');
  await page.waitForTimeout(800);
  const flowsText = await page.evaluate(() => {
    const el = document.getElementById('flows-panel');
    return el ? el.textContent : '';
  });
  check('文本树渲染产生链/使用链',
    flowsText.includes('产生链') && flowsText.includes('使用链') && flowsText.includes('[读]'),
    'tree=' + flowsText.slice(0, 100).replace(/\n/g, ' '));
}

// 8. 入参/返回按定义顺序（信息栏分组条目顺序，此时面板为 (Manager).Run）
const orderText = await page.evaluate(() => document.getElementById('panel-body').textContent);
const i1 = (s) => orderText.indexOf(s);
const paramSeg = orderText.slice(orderText.indexOf('参数（'), orderText.indexOf('返回（'));
check('参数按定义顺序（m→ctx→sessionID→userMessage）',
  i1('→m') >= 0 && i1('→ctx') > i1('→m') && i1('→sessionID') > i1('→ctx') && i1('→userMessage') > i1('→sessionID'),
  'params=' + paramSeg.replace(/\n/g, ' '));
const s1 = i1('→string');
const s2 = orderText.indexOf('→string', s1 + 1);
check('返回按定义顺序（string→string→error）',
  s1 >= 0 && s2 > s1 && i1('→error') > s2,
  'results=' + orderText.slice(orderText.indexOf('返回（'), orderText.indexOf('返回（') + 80).replace(/\n/g, ' '));

console.log('\n===== 字段追溯 e2e: ' + passed + ' passed, ' + failed + ' failed =====');
await browser.close();
process.exit(failed ? 1 : 0);
