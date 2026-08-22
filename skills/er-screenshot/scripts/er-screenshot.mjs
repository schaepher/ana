// ER 图截图（Q238 沉淀为 skill）：serve 的 /er.html 页面 → 截图。
//
// 用法:
//   node er-screenshot.mjs [--base http://localhost:8096] [--out /tmp/er.png]
//       [--all-lines] [--dblclick <表名>] [--full-page]
//
// 依赖: playwright（仓库 e2e/node_modules——createRequire 锚定，任意位置可跑）
//
// 行为: 打开 ER 页 → 等待渲染 → （可选）勾选全图画线 / 双击展开表 →
//   截图（默认 viewport 首屏；--full-page 整页长图）。
import { createRequire } from 'module';
import path from 'path';
import { fileURLToPath } from 'url';

// 仓库根 = 脚本位置向上三级（skills/er-screenshot/scripts/..）
const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../../..');
const require = createRequire(path.join(repoRoot, 'e2e/'));
const { chromium } = require('playwright');

const args = process.argv.slice(2);
const get = (k, d) => { const i = args.indexOf(k); return i >= 0 ? args[i + 1] : d; };
const has = (k) => args.includes(k);
const BASE = get('--base', 'http://localhost:8096');
const OUT = get('--out', '/tmp/er.png');
const dblTable = get('--dblclick', '');
const fullPage = has('--full-page');
const width = parseInt(get('--width', '1800'), 10);

const page = await (await chromium.launch()).newPage({ viewport: { width, height: 1100 } });
page.on('pageerror', (e) => console.log('[pageerror]', e.message));
await page.goto(BASE + '/er.html', { waitUntil: 'networkidle' });
await page.waitForTimeout(3000);

const info = await page.evaluate(({ allLines, dbl }) => {
  const tables = document.querySelectorAll('rect[data-tbl]').length;
  let dblDone = false;
  if (dbl) {
    const t = document.querySelector(`rect[data-tbl="${dbl}"]`);
    if (t) { t.dispatchEvent(new MouseEvent('dblclick', { bubbles: true })); dblDone = true; }
  }
  if (allLines) {
    const all = document.getElementById('f-alllines');
    if (all && !all.checked) { all.checked = true; all.dispatchEvent(new Event('change')); }
  }
  return { tables, dblDone };
}, { allLines: has('--all-lines'), dbl: dblTable });
console.log(`表数: ${info.tables}，双击${info.dblDone ? '已展开 ' + dblTable : '未展开'}`);

await page.waitForTimeout(has('--all-lines') ? 3500 : 2000);
await page.screenshot({ path: OUT, fullPage });
await (await page.context()).browser().close();
console.log('截图已保存:', OUT);
