// codeintel 前端回归套件：覆盖 app.js 各功能模块与行为契约
// （docs/appjs-refactor.md 第 2 节）。重构 app.js 后跑全量通过即行为未变。
//
// 运行：需 :8096 serve（codeintel 库）+ playwright
//   cd /tmp/layout-test && node /home/schaepher/Codes/codeintel/e2e/regression-suite.mjs
import { chromium } from 'playwright';

const BASE = 'http://localhost:8096/';
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
await page.waitForTimeout(2500);

const emit = (evt, id) => page.evaluate(({ e, i }) => window.__codeintelGraph.emit(e, { target: { id: i } }), { e: evt, i: id });
const names = () => page.evaluate(() => window.__codeintelGraph.getData().nodes.map((n) => n.data.label.split('\n').pop()));
const count = () => page.evaluate(() => window.__codeintelGraph.getData().nodes.length);
const Y = (ids) => page.evaluate((list) => {
  const g = window.__codeintelGraph;
  return list.map((id) => { const d = g.getNodeData(id); return d && d.style ? d.style.y : null; });
}, ids);
// 审计：所有边 source.y < target.y（箭头向下）
const auditBad = () => page.evaluate(() => {
  const g = window.__codeintelGraph;
  const d = g.getData();
  const bad = [];
  d.edges.forEach((e) => {
    let sy = null, ty = null;
    try { const ns = g.getNodeData(e.source); sy = ns && ns.style ? ns.style.y : null; } catch (err) {}
    try { const nt = g.getNodeData(e.target); ty = nt && nt.style ? nt.style.y : null; } catch (err) {}
    if (sy === null || ty === null) return;
    if (sy >= ty) bad.push(e.source.split(':').pop() + '→' + e.target.split(':').pop());
  });
  return bad;
});
const edgeLabels = () => page.evaluate(() => {
  const g = window.__codeintelGraph;
  const d = g.getData();
  return d.edges.map((e) => {
    const rs = g.getElementRenderStyle(e.id);
    return e.source.split(':').pop() + '[' + (rs ? rs.labelText : '?') + ']→' + e.target.split(':').pop();
  });
});

const rootId = 'symbol:go:github.com/schaepher/codeintel/internal/orchestrator:(Orchestrator).FullBuild';
const cmdInitId = 'symbol:go:github.com/schaepher/codeintel/internal/cli:cmdInit';
const mainId = 'symbol:go:github.com/schaepher/codeintel/internal/cli:Main';
const cmdMainId = 'symbol:go:github.com/schaepher/codeintel/cmd/codeintel:main';
const saveId = 'symbol:go:github.com/schaepher/codeintel/internal/infrastructure/sqlite:(Repo).Save';
const orchId = 'symbol:go:github.com/schaepher/codeintel/internal/orchestrator:Orchestrator';

async function selectEntry(label) {
  await page.fill('#entry-input', label);
  await page.waitForSelector('#entry-list li');
  await page.click('#entry-list li');
  await page.waitForTimeout(1200);
}

/* ---------- 用例 ---------- */

// 1. 入口选择后节点居中
await selectEntry('FullBuild');
const pos = await page.evaluate(() => {
  const g = window.__codeintelGraph;
  const p = g.getElementPosition(g.getData().nodes[0].id);
  return { x: p[0], y: p[1], cw: document.getElementById('container').clientWidth, ch: document.getElementById('container').clientHeight };
});
check('入口节点居中', Math.abs(pos.x - pos.cw / 2) < 5 && Math.abs(pos.y - pos.ch / 2) < 5, `(${Math.round(pos.x)},${Math.round(pos.y)}) vs (${Math.round(pos.cw / 2)},${Math.round(pos.ch / 2)})`);

// 2. 根展开三行布局：caller 上、节点中、callee 下
await emit('node:dblclick', rootId);
await page.waitForTimeout(3000);
const ys2 = await Y([cmdInitId, rootId, saveId]);
check('根展开三行布局', ys2[0] !== null && ys2[1] !== null && ys2[2] !== null && ys2[0] < ys2[1] && ys2[1] < ys2[2], `caller=${ys2[0]} root=${ys2[1]} callee=${ys2[2]}`);

// 3. 展开 callee（Save）同向剪枝：caller 顶行保留、其他 callee 剪
await emit('node:dblclick', saveId);
await page.waitForTimeout(3000);
const n3 = await names();
check('同向剪枝保留 caller', n3.includes('cmdInit'), 'cmdInit 在');
check('同向剪枝删除其他 callee', !n3.includes('(Repo).Counts') && !n3.includes('FromContext'), 'Counts/FromContext 不在');

// 4. 展开 caller（cmdInit）显示调用方 Main（链向上）
await emit('node:dblclick', cmdInitId);
await page.waitForTimeout(3000);
const n4 = await names();
check('展开 caller 显示调用方', n4.includes('Main'), 'Main 在');

// 5. 展开 Main 后相对顺序不变：cmdInit 仍在 FullBuild 上方（复杂序列
// 可能触发边方向修正/悬浮分支的全量分层，绝对位置允许变化）
await emit('node:dblclick', mainId);
await page.waitForTimeout(3000);
const yAfter = await Y([cmdInitId, rootId]);
check('展开 Main 后 caller 仍在根上方', yAfter[0] !== null && yAfter[1] !== null && yAfter[0] < yAfter[1], `cmdInit=${yAfter[0]} root=${yAfter[1]}`);

// 6. 展开 struct 不显示方法（Orchestrator 的独有方法 GetRepo 不出现）
await emit('node:dblclick', orchId);
await page.waitForTimeout(3000);
const n6 = await names();
check('struct 展开不显示方法', !n6.includes('(Orchestrator).GetRepo'), 'GetRepo 不在');

// 7. 收起只收一层：双击根 → cmdInit 分支保留、孤儿 callee 删除
await emit('node:dblclick', cmdMainId);
await page.waitForTimeout(3000);
await emit('node:dblclick', rootId);
await page.waitForTimeout(3000);
const n7 = await names();
check('收起根保留共享分支', n7.includes('cmdInit') && n7.includes('Main'), 'cmdInit/Main 在');
check('收起根删除孤儿 callee', !n7.includes('(Repo).Save') && !n7.includes('FromContext'), 'Save/FromContext 不在');
check('收起根后箭头审计', (await auditBad()).length === 0, JSON.stringify(await auditBad()));

// 8. 选中染色切换
await selectEntry('FullBuild');
await emit('node:dblclick', rootId);
await page.waitForTimeout(3000);
await emit('node:click', rootId);
await page.waitForTimeout(1000);
const c1 = await page.evaluate(() => {
  const g = window.__codeintelGraph;
  let blue = 0, red = 0;
  g.getData().edges.forEach((e) => {
    const s = g.getElementRenderStyle(e.id).stroke;
    if (s === '#1677ff') blue++; else if (s === '#f5222d') red++;
  });
  return { blue, red };
});
await emit('node:click', saveId);
await page.waitForTimeout(1000);
const c2 = await page.evaluate(() => {
  const g = window.__codeintelGraph;
  let blue = 0, red = 0;
  g.getData().edges.forEach((e) => {
    const s = g.getElementRenderStyle(e.id).stroke;
    if (s === '#1677ff') blue++; else if (s === '#f5222d') red++;
  });
  return { blue, red };
});
check('选中染色（root 出边蓝为主）', c1.blue > 0 && c1.blue > c1.red, JSON.stringify(c1));
check('切换选中颜色跟随（入边红）', c2.blue === 0 && c2.red > 0, JSON.stringify(c2));

// 9. 信息栏分组与按钮
await emit('node:click', rootId);
await page.waitForTimeout(1000);
const panel = await page.evaluate(() => ({
  groups: Array.from(document.querySelectorAll('#panel-body h3')).map((h) => h.textContent),
  hideBtns: document.querySelectorAll('.hide-group-btn').length,
  expandBtns: document.querySelectorAll('.expand-group-btn').length
}));
check('信息栏分组渲染', panel.groups.some((g) => g.includes('调用（')), JSON.stringify(panel.groups));
check('隐藏/展开按钮', panel.hideBtns > 0 && panel.expandBtns > 0, `hide=${panel.hideBtns} expand=${panel.expandBtns}`);

// 10. 隐藏按钮：隐藏分组节点、已展开保留
await emit('node:dblclick', cmdInitId);
await page.waitForTimeout(3000);
await emit('node:click', rootId);
await page.waitForTimeout(1000);
await page.evaluate(() => {
  const h3 = Array.from(document.querySelectorAll('#panel-body h3')).find((h) => h.textContent.trim().startsWith('调用（'));
  h3.querySelector('.hide-group-btn').dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }));
});
await page.waitForTimeout(2000);
const n10 = await names();
check('隐藏按钮隐藏分组节点', !n10.includes('(Repo).Counts') && !n10.includes('FromContext'), 'Counts/FromContext 不在');
check('隐藏保留已展开节点', n10.includes('cmdInit'), 'cmdInit 在');

// 11. 展开按钮：只把分组节点显示到图上（一层，不展开它们的关系）
// 用例 10 刚隐藏了 root 的"调用"分组全部节点（现在都不在图上）。
// 点击 [展开] 后新增节点数应恰等于分组节点数——再多一个就是展开了两层
const expInfo = await page.evaluate(() => {
  const h3 = Array.from(document.querySelectorAll('#panel-body h3')).find((h) => h.textContent.trim().startsWith('调用（'));
  const m = h3 ? h3.textContent.match(/调用（(\d+)）/) : null;
  const before = window.__codeintelGraph.getData().nodes.length;
  if (h3) h3.querySelector('.expand-group-btn').dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }));
  return { n: m ? parseInt(m[1]) : 0, before };
});
await page.waitForTimeout(2500);
const n11 = await count();
check('展开按钮只显示一层', expInfo.n > 0 && n11 - expInfo.before === expInfo.n, `分组 ${expInfo.n} 新增 ${n11 - expInfo.before}`);

// 12. 标签四行 + 边标注
const labels = await page.evaluate(() => window.__codeintelGraph.getData().nodes.map((n) => n.data.label));
const fullBuildLabel = labels.find((l) => l.includes('FullBuild'));
const edges = await edgeLabels();
check('标签四行', fullBuildLabel === 'orchestrator\norchestrator.go\n(Orchestrator)\nFullBuild', JSON.stringify(fullBuildLabel));
check('边标注', edges.some((e) => e.includes('[调用]')), '调用边在');

// 13. 隐藏规则配置（localStorage）：勾选"方法"后持久化
await page.click('#hide-legend-btn');
await page.waitForTimeout(300);
await page.evaluate(() => {
  const labels = Array.from(document.querySelectorAll('#hide-list label'));
  const l = labels.find((x) => x.textContent.includes('方法'));
  if (l && !l.querySelector('input').checked) l.querySelector('input').click();
});
await page.waitForTimeout(300);
const hideConfig = await page.evaluate(() => localStorage.getItem('codeintel.hideKinds'));
check('隐藏规则配置持久化', hideConfig !== null && hideConfig.includes('has_method'), hideConfig);
// 恢复默认（避免影响后续用例）
await page.evaluate(() => {
  const labels = Array.from(document.querySelectorAll('#hide-list label'));
  const l = labels.find((x) => x.textContent.includes('方法'));
  if (l && l.querySelector('input').checked) l.querySelector('input').click();
});

// 14. 箭头审计（完整序列）
await selectEntry('FullBuild');
await emit('node:dblclick', rootId); await page.waitForTimeout(3000);
await emit('node:dblclick', cmdInitId); await page.waitForTimeout(3000);
await emit('node:dblclick', mainId); await page.waitForTimeout(3000);
await emit('node:dblclick', cmdMainId); await page.waitForTimeout(3000);
await emit('node:dblclick', cmdMainId); await page.waitForTimeout(3000);
await emit('node:dblclick', mainId); await page.waitForTimeout(3000);
await emit('node:dblclick', cmdInitId); await page.waitForTimeout(3000);
await emit('node:dblclick', saveId); await page.waitForTimeout(3000);
await emit('node:dblclick', rootId); await page.waitForTimeout(3000);
const bad14 = await auditBad();
check('14 步序列箭头全向下', bad14.length === 0, JSON.stringify(bad14.slice(0, 5)));

// 15. Source Code 弹窗
await selectEntry('FullBuild');
await emit('node:dblclick', rootId);
await page.waitForTimeout(3000);
await emit('node:click', rootId);
await page.waitForTimeout(1000);
await page.click('#source-btn');
await page.waitForTimeout(1000);
const src = await page.evaluate(() => ({
  visible: !document.getElementById('modal').classList.contains('hidden'),
  hasCode: document.getElementById('modal-code').textContent.length > 100,
  hasHljs: document.querySelectorAll('#modal-code .hljs-keyword').length > 0
}));
check('Source Code 弹窗', src.visible && src.hasCode && src.hasHljs, JSON.stringify({ len: src.hasCode, hljs: src.hasHljs }));

/* ---------- 汇总 ---------- */
console.log('\n===== 回归结果: ' + passed + ' 通过 / ' + failed + ' 失败 / 共 ' + results.length + ' 用例 =====');
await browser.close();
process.exit(failed ? 1 : 0);
