---
name: export-pdf
license: 'MIT'
description: '把 codeintel 查询结果导出为 PDF——源码带行号 + 查询输出（文本/tree/mermaid/json 四格式）+ mermaid 渲染图。用于给用户交付可读的查询示例/报告（Q235-10/11/12 沉淀）。'
---

# 查询结果导出 PDF

把 codeintel 的 value-trace 等查询结果渲染成排版良好的 PDF：
源码（带行号）+ 四格式输出 + mermaid 图。

## 前置

- python3 + pymupdf：`pip install --break-system-packages pymupdf`
- 网络（mermaid.ink 渲染图；无网络时脚本自动跳过图）

## 使用

```bash
# 基础用法：repo + 锚点节点
python3 skills/export-pdf/scripts/value-trace-pdf.py \
  --repo /path/to/repo \
  --anchor "symbol:go:example.com/m:main#u.Brands.write@30" \
  --out /tmp/value-trace-demo.pdf

# 完整参数
python3 skills/export-pdf/scripts/value-trace-pdf.py \
  --repo <仓库路径> \
  --anchor <value-trace 锚点节点 ID> \
  --out <输出.pdf> \
  --bin <codeintel 二进制> \    # 默认 codeintel（PATH）
  --depth 6 \                   # value-trace 深度（默认 6）
  --src-file main.go \          # 源码文件（默认锚点文件；可省略）
  --title "标题"                # 默认自动生成
```

## 输出结构

PDF 分节（自动分页）：
1. 源码（带行号，与查询输出的行号可对照）
2. 默认文本格式（分组：写入值/对象/来源/去向 + 源码片段 + 接收者来源）
3. `--format tree`（ASCII 树形）
4. `--format mermaid`（flowchart 父子链 + 渲染图）
5. `--json`（结构化输出）

## 技术要点（Q235-10/11/12 沉淀）

- **字体**：PDF 标准字体（英文 courier 等宽、中文 china-s）——**不嵌入**
  （文件小，~240KB）；**中英混排**：按连续 ASCII/非 ASCII 段切分绘制
  （china-s 对英文是全角宽度——整行用会字母间隔过大）
- **mermaid 图**：mermaid.ink `?width=2000` 高分辨率渲染 → 插入时
  **宽度优先缩放**（min(可用宽/图宽, 可用高/图高)）+ 居中——图不溢出
- **分页**：y 坐标超限自动 new_page（单页溢出会导致图插到页面外——
  图嵌入了但不可见，Q235-11 教训）
- 文本提取校验：生成后 `page.get_text()` 抽查中文是否正常（subset
  类操作可能损坏字形）

## 依赖脚本

- `scripts/value-trace-pdf.py`：主脚本（CLI 查询 + mermaid 渲染 +
  pymupdf 构建）
- `scripts/render-mermaid.py`：独立 mermaid.ink 渲染（输出图片，
  可单独用）
