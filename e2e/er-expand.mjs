import { chromium } from 'playwright';
const b = await chromium.launch();
const p = await b.newPage({ viewport: { width: 1440, height: 900 } });
await p.goto('http://localhost:8096/er.html');
await p.waitForSelector('#svg-wrap svg', { timeout: 20000 });
await p.waitForTimeout(400);
console.log('init:', await p.$$eval('rect.tbl', e => e.length), 'paths:', await p.$$eval('#svg-wrap svg path', e => e.filter(x => (x.getAttribute('d')||'').startsWith('M')).length));
// 展开 mm_block_list（5 条关联）
await (await p.$('rect.tbl[data-tbl="mm_block_list"]')).dblclick();
await p.waitForTimeout(500);
const info1 = await p.textContent('#info');
console.log('expand mm_block_list:', info1);
await p.screenshot({ path: '/tmp/er-expand1.png' });
// 再展开可见关联表 mm_extra_field（多选）
await (await p.$('rect.tbl[data-tbl="mm_extra_field"]')).dblclick();
await p.waitForTimeout(500);
const t2 = await p.$$eval('rect.tbl', e => e.length);
const p2 = await p.$$eval('#svg-wrap svg path', e => e.filter(x => (x.getAttribute('d')||'').startsWith('M')).length);
const info2 = await p.textContent('#info');
console.log('expand both: tables:', t2, 'paths:', p2, '|', info2);
await p.screenshot({ path: '/tmp/er-expand2.png' });
// 嵌套画法展开 mm_block_list（多线分叉检查）
await p.click('#mode-nested');
await p.waitForSelector('#svg-wrap rect.nest-tbl', { timeout: 10000 });
await p.waitForTimeout(300);
const nt0 = await p.$$eval('rect.nest-tbl', e => e.length);
await (await p.$('rect.nest-tbl[data-tbl="mm_block_list"]')).dblclick();
await p.waitForTimeout(500);
const n1 = await p.$$eval('#svg-wrap svg path', e => e.filter(x => (x.getAttribute('d')||'').startsWith('M')).length);
const nt = await p.$$eval('rect.nest-tbl', e => e.length);
console.log('nested expand mm_block_list: tables:', nt, 'paths:', n1);
// 出线段端点（x1,y1）去重检查重合
const outs = await p.$$eval('#svg-wrap svg path', els => els.map(e => e.getAttribute('d')).filter(d => d && d.startsWith('M')).map(d => d.match(/M ([\d.]+) ([\d.]+) L ([\d.]+) ([\d.]+)/).slice(1).join(',')));
const dup = outs.filter((v, i) => outs.indexOf(v) !== i);
console.log('out-seg dup (same first segment):', dup.length ? dup : 'none');
await p.screenshot({ path: '/tmp/er-expand-nested.png' });
await b.close();
