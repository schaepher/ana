// codeintel 字段追溯 e2e（playwright）：参数/返回节点展开、信息栏类型、
// 字段数据流按钮与文本树、符号搜索排除。
//
// codeintel 字段追溯 e2e（playwright）：参数/返回节点展开、信息栏类型、
// 字段数据流按钮与文本树、符号搜索排除。
//
// 运行（符号 ID 须从目标仓库索引获取，经环境变量注入——仓库名不落库）：
//   make e2e E2E_REPO=<已构建的仓库> \
//     FN_ID='symbol:go:<module>:<pkg>:(T).Method' \
//     E2E_STORE_CREATE='...' E2E_IFACE_ID='...' E2E_TRAIN_ID='...'
//   或手动：/path/to/codeintel serve --repo <repo> --addr :8096
//           cd e2e && FN_ID=... node field-trace-e2e.mjs
import { chromium } from 'playwright';

const BASE = process.env.BASE || 'http://localhost:8096/';
const FN_ID = process.env.FN_ID || '';
const PARAM_ID = FN_ID + '#param.ctx';
const RESULT_ID = FN_ID + '#result.0';

// 符号 ID 由环境变量注入（目标仓库相关，不硬编码仓库名）
const STORE_CREATE = process.env.E2E_STORE_CREATE || '';
const IFACE_ID = process.env.E2E_IFACE_ID || '';
const TRAIN_ID = process.env.E2E_TRAIN_ID || '';
if (!FN_ID) {
  console.log('未设置 FN_ID（目标仓库符号 ID）。示例：');
  console.log('  make e2e E2E_REPO=<repo> FN_ID="symbol:go:<module>:<pkg>:(T).Method"');
  console.log('    E2E_STORE_CREATE=... E2E_IFACE_ID=... E2E_TRAIN_ID=...');
  process.exit(0);
}

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

// 4. UI：双击函数节点展开（真实用户路径，seenNodes/expandedMap 正确维护
// ——手动注入会绕过 seenNodes，后续展开 addNode 重复添加抛
// "Node already exists"）→ 画布出现参数/返回节点
await page.evaluate((id) => window.__codeintelGraph.emit('node:dblclick', { target: { id } }), FN_ID);
await page.waitForTimeout(1200);
const kindsOnGraph = await page.evaluate(() => window.__codeintelGraph.getData().nodes.map((n) => n.data.kind));
check('画布出现 parameter 与 result 节点',
  kindsOnGraph.includes('parameter') && kindsOnGraph.includes('result'),
  'kinds=' + kindsOnGraph.join(','));

// 5. 节点配色（Q214：锁 G6@5.1.1 后恢复颜色断言）
// getElementRenderStyle（渲染期）在 5.1.1 对 relayoutTree 重建后的元素
// 抛错（shapeMap 未建）——改为双层验证：
//   a. 数据层：节点存在且 kind 正确（第 4 步已断言 kind）
//   b. 映射逻辑：前端 KIND_COLOR 常量（state.js）含 parameter→金 /
//      result→粉——颜色映射回归不依赖渲染 API 行为
const paramOnGraph = await page.evaluate((id) => {
  return window.__codeintelGraph.getData().nodes.some((n) => n.id === id);
}, PARAM_ID);
const resultOnGraph = await page.evaluate((id) => {
  return window.__codeintelGraph.getData().nodes.some((n) => n.id === id);
}, RESULT_ID);
check('参数/返回节点渲染存在', paramOnGraph && resultOnGraph,
  'param=' + paramOnGraph + ' result=' + resultOnGraph);
const kindColors = await page.evaluate(async () => {
  const res = await fetch('/js/state.js');
  const src = await res.text();
  return {
    paramGold: src.includes("parameter: '#d48806'"),
    resultPink: src.includes("result: '#f759ab'"),
  };
});
check('节点配色映射（parameter 金 / result 粉）',
  kindColors.paramGold && kindColors.resultPink,
  JSON.stringify(kindColors));

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
const retSeg = orderText.slice(orderText.indexOf('返回（'), orderText.indexOf('被调用（'));
// Q188：参数/返回表格化（名称 | 类型 两列，textContent 单元格拼接无
// 分隔）——顺序断言用分段局部 indexOf
check('参数表格顺序（接收者 m → ctx → sessionID → userMessage）',
  i1('→m · *Manager') >= 0 &&
  paramSeg.indexOf('ctx') < paramSeg.indexOf('sessionID') && paramSeg.indexOf('sessionID') < paramSeg.indexOf('userMessage'),
  'params=' + paramSeg.replace(/\n/g, ' '));
// Q186/Q188：返回表格（名称 = 签名参数名，类型列含 string/error）
check('返回表格顺序（reply→newSessionID→err，带类型）',
  retSeg.indexOf('reply') >= 0 && retSeg.indexOf('reply') < retSeg.indexOf('newSessionID') &&
  retSeg.indexOf('newSessionID') < retSeg.indexOf('err') &&
  retSeg.indexOf('string') >= 0 && retSeg.indexOf('error') >= 0,
  'results=' + retSeg.replace(/\n/g, ' '));

// 9. 双击参数节点：展开数据流上下游（桥边 → ssa_value → field_access）
const RECV_ID = FN_ID + '#param.recv.m';
await page.evaluate((id) => window.__codeintelGraph.emit('node:dblclick', { target: { id } }), RECV_ID);
await page.waitForTimeout(1200);
const afterKinds = await page.evaluate(() => window.__codeintelGraph.getData().nodes.map((n) => n.data.kind));
check('双击参数节点展开数据流上下游（field_access，Q178 参数统一）',
  afterKinds.includes('field_access'),
  'kinds=' + afterKinds.join(','));
const hasCfg = await page.evaluate(() => window.__codeintelGraph.getData().nodes.some(
  (n) => n.data.label && n.data.label.indexOf('m.cfg') >= 0));
check('展开含 m.cfg 字段访问节点', hasCfg);

// 10. 字段访问节点显示所属函数与读写（后端字段 + 画布标签 + 信息栏）
const expParam = await api('/api/expand?id=' + encodeURIComponent(RECV_ID));
const faNode0 = expParam.neighbors.find((n) => n.kind === 'field_access');
check('expand 返回字段访问节点带 funcName',
  faNode0 && faNode0.funcName && faNode0.funcName.indexOf('(Manager).Run') >= 0,
  'funcName=' + (faNode0 ? faNode0.funcName : 'none'));
const faInfo = await page.evaluate(() => {
  const g = window.__codeintelGraph;
  const n = g.getData().nodes.find((x) => x.data.kind === 'field_access');
  return n ? { label: n.data.label, id: n.id } : null;
});
check('画布字段访问节点标签含所属函数与[写]',
  faInfo && faInfo.label.indexOf('(Manager).Run') >= 0 && faInfo.label.indexOf('[写]') >= 0,
  'label=' + (faInfo ? faInfo.label.replace(/\n/g, '|') : 'none'));
if (faInfo) {
  await page.evaluate((id) => window.__codeintelGraph.emit('node:click', { target: { id } }), faInfo.id);
  await page.waitForTimeout(800);
  const faPanel = await page.evaluate(() => document.getElementById('panel-body').textContent);
  check('信息栏显示所属函数',
    faPanel.indexOf('所属函数') >= 0 && faPanel.indexOf('(Manager).Run') >= 0,
    'panel=' + faPanel.slice(0, 60).replace(/\n/g, ' '));
}

// 11. 链上参数可展开到所属函数（has_param 边；Q178 参数统一为签名
//     参数节点。用 ctx——第 9 步已展开过 receiver m，再双击会触发收起）
const M_VALUE_ID = PARAM_ID;
const expVal = await api('/api/expand?id=' + encodeURIComponent(M_VALUE_ID));
check('ssa_value 参数展开返回所属函数桥边',
  expVal.edges.some((e) => e.kind === 'has_param' && e.target === M_VALUE_ID) &&
  expVal.neighbors.some((n) => (n.kind === 'function' || n.kind === 'method') && n.name === '(Manager).Run'),
  'edges=' + expVal.edges.map((e) => e.kind).join(','));
await page.evaluate((id) => window.__codeintelGraph.emit('node:dblclick', { target: { id } }), M_VALUE_ID);
await page.waitForTimeout(1000);
const edgeKinds = await page.evaluate(() => window.__codeintelGraph.getData().edges.map((e) => e.data.kind));
check('双击参数值节点后画布出现 has_param 边（回到所属函数）',
  edgeKinds.includes('has_param'),
  'edges=' + edgeKinds.join(','));

// 12. 数据值全链：信息栏"追踪此数据"按钮 → 函数上下文分组文本树。
// 两个锚点各验证一段能力（Q95 过滤方向语义 + 参数不挂条件的已知
// 边界）：
//   a. ctx 参数锚点：正向 argument 跨函数 → (Handler).PageChatSend 分组
//   b. 画布读节点锚点（m.cfg.read）：反向链 [读] + [条件:]（if 内读）
const vtTextOf = async () => {
  await page.waitForTimeout(800);
  const hasVtBtn = await page.evaluate(() => !!document.getElementById('vt-btn'));
  if (!hasVtBtn) return '';
  await page.click('#vt-btn');
  await page.waitForTimeout(1000);
  return page.evaluate(() => {
    const el = document.getElementById('vt-panel');
    return el ? el.textContent : '';
  });
};
// a. ctx 锚点：跨函数分组（参数传递形态）
await page.evaluate((id) => window.__codeintelGraph.emit('node:click', { target: { id } }), M_VALUE_ID);
const vtCtx = await vtTextOf();
check('全链视图按函数上下文分组（跨函数 argument 链）',
  vtCtx.indexOf('(Manager).Run') >= 0 && vtCtx.indexOf('(Handler).PageChatSend') >= 0,
  'vt=' + vtCtx.slice(0, 120).replace(/\n/g, ' '));
// b. 画布读节点锚点：读写标记 + 路径条件（if 内字段读形态）
const vtReadAnchor = (await page.evaluate(() => {
  const g = window.__codeintelGraph;
  const n = g.getData().nodes.find((x) => x.data.kind === 'field_access' &&
    x.data.label && x.data.label.indexOf('[读]') >= 0);
  return n ? n.id : null;
})) || faInfo.id;
await page.evaluate((id) => window.__codeintelGraph.emit('node:click', { target: { id } }), vtReadAnchor);
const vtRead = await vtTextOf();
check('全链视图显示读写与条件标注（[读] + [条件:]）',
  vtRead.indexOf('[读]') >= 0 && vtRead.indexOf('[条件:') >= 0,
  'vt=' + vtRead.slice(0, 200).replace(/\n/g, ' '));

// 13b. 信息栏 receiver 分组：方法节点面板含"接收者（N）"分组
await page.evaluate((id) => window.__codeintelGraph.emit('node:click', { target: { id } }), FN_ID);
await page.waitForTimeout(800);
const recvPanel = await page.evaluate(() => document.getElementById('panel-body').textContent);
check('信息栏 receiver 独立分组（接收者（N））',
  recvPanel.indexOf('接收者（') >= 0,
  'panel=' + recvPanel.slice(0, 80).replace(/\n/g, ' '));

// 13c. ORM 持久化映射：store.Create 的 flows 含 表.列 虚拟节点（gorm）
//      （E2E_STORE_CREATE 未注入时跳过）
if (STORE_CREATE) {
const createFlows = await api('/api/flows?id=' + encodeURIComponent(STORE_CREATE));
const colNode = (createFlows.flows || []).find((f) => f.kind === 'field_access' &&
  f.name.indexOf('.') >= 0 && f.access === 'write' && f.name.indexOf('ext.') < 0 &&
  f.name.indexOf('t0') < 0 && f.name.indexOf('t1') < 0);
check('持久化映射：Create 的 flows 含 表.列 虚拟节点',
  colNode !== undefined && /^[a-z_]+\.[a-z_]+$/.test(colNode.name),
  'col=' + (colNode ? colNode.name : 'none'));
}

// 13d. 动态派发候选：接口节点 expand 返回 dispatch_to 边
//      （E2E_IFACE_ID 未注入时跳过）
if (IFACE_ID) {
const ifaceExp = await api('/api/expand?id=' + encodeURIComponent(IFACE_ID));
check('动态派发：接口 expand 返回 dispatch_to 候选边',
  (ifaceExp.edges || []).some((e) => e.kind === 'dispatch_to'),
  'edges=' + (ifaceExp.edges || []).map((e) => e.kind).join(','));

// 13e. 前端候选实现展示：单击接口节点 → 信息栏"候选实现（N）"分组
await page.evaluate((id) => window.__codeintelGraph.emit('node:click', { target: { id } }), IFACE_ID);
await page.waitForTimeout(800);
const ifacePanel = await page.evaluate(() => document.getElementById('panel-body').textContent);
check('信息栏接口节点显示"候选实现"分组（Q95 前端展示）',
  ifacePanel.indexOf('候选实现（') >= 0,
  'panel=' + ifacePanel.slice(0, 100).replace(/\n/g, ' '));
}

// 13. map/slice 元素访问（Q83）：/api/flows 返回元素路径（data["Active"] 等）
//      （E2E_TRAIN_ID 未注入时跳过）
if (TRAIN_ID) {
const trainFlows = await api('/api/flows?id=' + encodeURIComponent(TRAIN_ID));
const elem = (trainFlows.flows || []).find((f) => f.kind === 'field_access' && f.name.indexOf('["') >= 0);
check('元素访问节点出现在 flows（map 常量 key 路径）',
  elem !== undefined && elem.access === 'write' && elem.name.indexOf('data["') >= 0,
  'elem=' + (elem ? elem.name : 'none'));
}

// 14. ER 图：同字段反向对合并为一条双向线（Q200 机制 + Q219 fk 类型修正）——
//     严格反向同字段对（A.x→B.y 与 B.y→A.x）合并为一条（bi 标注 ↔）；
//     混合对（一方向 fk、另一方向 query/write）合并后类型取最高
//     （fk > query > write > read，与后端 relTypeRank 一致）——
//     relRank 漏 fk（Q218 引入）会把混合对降级，默认只画 fk 视图下样式错误
await page.goto(BASE + 'er.html', { waitUntil: 'networkidle' });
await page.waitForTimeout(800);
const mergeBi = await page.evaluate(() => {
  const mk = (t, c, tt, tc, type) =>
    ({ from_table: t, from_col: c, to_table: tt, to_col: tc, type, hops: 1 });
  return {
    fkQuery: mergeBidirectional([
      mk('a', 'id', 'b', 'item_id', 'fk'),
      mk('b', 'item_id', 'a', 'id', 'query'),
    ]),
    writeFk: mergeBidirectional([
      mk('a', 'id', 'b', 'item_id', 'write'),
      mk('b', 'item_id', 'a', 'id', 'fk'),
    ]),
    fkFk: mergeBidirectional([
      mk('a', 'id', 'b', 'item_id', 'fk'),
      mk('b', 'item_id', 'a', 'id', 'fk'),
    ]),
    queryQuery: mergeBidirectional([
      mk('a', 'id', 'b', 'item_id', 'query'),
      mk('b', 'item_id', 'a', 'id', 'query'),
    ]),
    notReverse: mergeBidirectional([
      mk('a', 'id', 'b', 'item_id', 'fk'),
      mk('b', 'item_id', 'a', 'name', 'fk'),
    ]),
  };
});
check('ER 同字段反向对合并为一条双向线（fk+query → fk 不降级）',
  mergeBi.fkQuery.length === 1 && mergeBi.fkQuery[0].bi === true && mergeBi.fkQuery[0].type === 'fk',
  JSON.stringify(mergeBi.fkQuery));
check('ER 反向混合对类型取最高（write+fk → fk，不降级为 write）',
  mergeBi.writeFk.length === 1 && mergeBi.writeFk[0].type === 'fk',
  JSON.stringify(mergeBi.writeFk));
check('ER fk+fk 反向对合并为一条双向 fk',
  mergeBi.fkFk.length === 1 && mergeBi.fkFk[0].bi === true && mergeBi.fkFk[0].type === 'fk',
  JSON.stringify(mergeBi.fkFk));
check('ER query+query 反向对仍合并（Q200 回归）',
  mergeBi.queryQuery.length === 1 && mergeBi.queryQuery[0].bi === true && mergeBi.queryQuery[0].type === 'query',
  JSON.stringify(mergeBi.queryQuery));
check('ER 非严格反向对不合并（两条独立线）',
  mergeBi.notReverse.length === 2 && mergeBi.notReverse.every((r) => !r.bi),
  JSON.stringify(mergeBi.notReverse));
// info 栏类型统计含 fk（Q219：fk 为默认线，缺统计误导）
const erInfo = await page.evaluate(() => document.getElementById('info').textContent);
check('ER info 栏统计含 fk 计数',
  /条关联（fk \d+/.test(erInfo),
  'info=' + erInfo.slice(0, 80));
// 15. 双击表 → 按需加载期间展示「加载中...」弹框，加载完成取消（Q224）：
//     双击触发 loadTableRels（fetch ?table=X）→ 弹框显示 → then 后取消并渲染
await page.evaluate(() => {
  const tbl = document.querySelector('[data-tbl]');
  if (tbl) tbl.dispatchEvent(new MouseEvent('dblclick', { bubbles: true }));
});
await page.waitForTimeout(1500);
const erLoading = await page.evaluate(() => ({
  hidden: document.getElementById('loading').classList.contains('hidden'),
  selected: document.querySelectorAll('.tbl.selected, .nest-tbl.selected').length,
}));
check('ER 双击表加载中弹框加载后取消且表展开生效',
  erLoading.hidden && erLoading.selected > 0,
  JSON.stringify(erLoading));

console.log('\n===== 字段追溯 e2e: ' + passed + ' passed, ' + failed + ' failed =====');
await browser.close();
process.exit(failed ? 1 : 0);
