---
name: er-screenshot
license: 'MIT'
description: '把 codeintel serve 的数据库 ER 图页（/er.html）截图交付：playwright 打开页面、可选勾选全图画线/双击展开表、截图（首屏或 fullPage 长图）、长图缩放切分。当用户要求查看/交付某个仓库的 ER 图、表关联图、数据库关系截图时使用本 skill。'
---

# ER 图截图

把 `codeintel serve` 的 `/er.html`（数据库 ER 图：表卡片 + 关系线）截成图片交付。
Q238 沉淀：go2o 304 表全画线截图流程（playwright + 长图缩放切分）。

## 前置

- serve 已启动：`codeintel serve --repo <仓库> --addr :8096`
- playwright：仓库 `e2e/node_modules`（`cd e2e` 后运行，模块解析按脚本路径找）
- Pillow（切分用）：`pip install --break-system-packages pillow`

## 使用

```bash
# 1. 截图（首屏；--full-page 整页长图；--all-lines 全图画线；--dblclick <表名> 双击展开）
cd <仓库>/e2e && node ../skills/er-screenshot/scripts/er-screenshot.mjs \
  --base http://localhost:8096 --out /tmp/er.png --all-lines --dblclick pay_merchant

# 2. 长图缩放切分（IM 里全页长图不可读——缩到宽 1200 + 切段，重叠 60px）
python3 skills/er-screenshot/scripts/split-scale.py /tmp/er.png /tmp/er
#    → /tmp/er-scaled.png（拼接一张整图）+ /tmp/er-partN.png（切段）

# 3. 发送：整图拼接一张（--scaled）或多段（--partN）
cc-connect send --image /tmp/er-scaled.png
cc-connect send --image /tmp/er-part1.png --image /tmp/er-part2.png ...
```

## 参数

| 参数 | 作用 |
|---|---|
| `--base <url>` | serve 地址（默认 http://localhost:8096） |
| `--out <path>` | 输出 PNG（默认 /tmp/er.png） |
| `--all-lines` | 勾选「全图画线」（Q204：直接画全部关系线，免双击） |
| `--dblclick <表名>` | 双击展开该表（只显示相关表重排 + 高亮） |
| `--full-page` | fullPage 整页长图（大仓库很高，配合 split-scale.py） |
| `--width <px>` | 视口宽度（默认 1800）——关联展开横向排布超出时加宽（如 3200，配合 --full-page 防右边截断） |

## 技术要点（Q238 沉淀）

- **表元素选择器**：`rect[data-tbl="<表名>"]`（SVG 自绘，非 HTML 卡片）；
  双击事件委托在 `#svg-wrap`——dispatchEvent dblclick 到 rect（bubbles）即可
- **全图画线开关**：`#f-alllines` checkbox，设置 checked 后 dispatchEvent('change')
- **渲染等待**：默认 3s（表布局）+ 全画线再加 3.5s（关系线按需加载，Q224 弹框）
- **长图交付**：1800×7000+ 全页图在 IM 不可读——split-scale.py 缩到宽
  1200 + 切段（重叠 60px 防衔接信息丢失）；**拼接一张**用
  `-scaled.png`（缩放整图，横向不截断时优先——IM 里纵向长图
  自动压缩，可读性可接受）
- **横向截断**：关联展开图横向超出视口时 fullPage 只扩高度——
  用 `--width 3200` 加宽重截（右边不全的修复路径）
- 表数统计：`document.querySelectorAll('rect[data-tbl]').length`

## 依赖脚本

- `scripts/er-screenshot.mjs`：playwright 截图（--base/--out/--all-lines/
  --dblclick/--full-page/--width）
- `scripts/split-scale.py`：Pillow 缩放 + 切段（宽 1200 / 段高 1620 / 重叠 60）
