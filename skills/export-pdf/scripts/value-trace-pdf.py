#!/usr/bin/env python3
"""value-trace 导出 PDF：源码带行号 + 文本/tree/mermaid/json 四格式 +
mermaid 渲染图（Q235-10/11/12 沉淀为 skill）。

用法:
  python3 value-trace-pdf.py --repo <path> --anchor <node-id> \\
      [--out out.pdf] [--bin codeintel] [--depth 6] [--src-file <file>] [--title <t>]

依赖: pymupdf (pip install --break-system-packages pymupdf)；网络
（mermaid.ink 渲染图——无网络自动跳过图继续）。
"""
import argparse
import base64
import re
import subprocess
import sys
import urllib.request

try:
    import pymupdf
except ImportError:
    sys.exit("需要 pymupdf: pip install --break-system-packages pymupdf")

PAGE_W, PAGE_H = 595, 842  # A4 pt
MARGIN = 40
MAX_W = PAGE_W - 2 * MARGIN
MAX_H = PAGE_H - 2 * MARGIN
LATIN = "courier"  # 英文/代码：等宽紧凑（PDF 标准字体，不嵌入）
CJK = "china-s"    # 中文：PDF 标准字体（不嵌入）


def run_cli(bin_path, anchor, repo, depth, extra):
    cmd = [bin_path, "query", "value-trace", anchor, "--repo", repo,
           "--max-depth", str(depth)] + extra
    r = subprocess.run(cmd, capture_output=True, timeout=120)
    return r.stdout.decode("utf-8", errors="replace")


def render_mermaid(graph):
    """mermaid.ink 渲染（width=2000 高分辨率）。失败返回 None（跳过图）。"""
    try:
        enc = base64.urlsafe_b64encode(graph.encode()).decode()
        req = urllib.request.Request(
            f"https://mermaid.ink/img/{enc}?width=2000",
            headers={"User-Agent": "Mozilla/5.0"})
        return urllib.request.urlopen(req, timeout=30).read()
    except Exception:
        return None


def draw_mixed(page, x, y, text, size):
    """中英混排：连续 ASCII 段用 courier（等宽紧凑）、中文段用 china-s。
    china-s 对英文是全角宽度——整行用会字母间隔过大（Q235-12 教训）。"""
    for seg in re.split(r"([\x00-\x7f]+)", text):
        if not seg:
            continue
        if seg[0] < "\x80":
            page.insert_text((x, y), seg, fontname=LATIN, fontsize=size)
            x += pymupdf.get_text_length(seg, fontname=LATIN, fontsize=size)
        else:
            page.insert_text((x, y), seg, fontname=CJK, fontsize=size)
            x += pymupdf.get_text_length(seg, fontname=CJK, fontsize=size)


class Writer:
    def __init__(self, doc):
        self.doc = doc
        self.page = doc.new_page(width=PAGE_W, height=PAGE_H)
        self.y = MARGIN

    def new_page_if_needed(self, need=30):
        if self.y + need > PAGE_H - 40:
            self.page = self.doc.new_page(width=PAGE_W, height=PAGE_H)
            self.y = MARGIN

    def text(self, s, size):
        self.new_page_if_needed(size * 2)
        draw_mixed(self.page, MARGIN, self.y, s, size)
        self.y += size * 1.6

    def code(self, block, size=8):
        self.new_page_if_needed(30)
        for line in block.rstrip("\n").split("\n"):
            if self.y > PAGE_H - 40:
                self.page = self.doc.new_page(width=PAGE_W, height=PAGE_H)
                self.y = MARGIN
            draw_mixed(self.page, MARGIN + 4, self.y, line, size)
            self.y += size * 1.35
        self.y += 8

    def image(self, path):
        # 宽度优先缩放 + 居中——图不溢出页面（Q235-11 教训）
        pix = pymupdf.Pixmap(path)
        w_pt = pix.width * 72 / max(pix.xres, 1)
        h_pt = pix.height * 72 / max(pix.yres, 1)
        scale = min(MAX_W / w_pt, MAX_H / h_pt)
        w_new, h_new = w_pt * scale, h_pt * scale
        self.new_page_if_needed(h_new + 20)
        x = (PAGE_W - w_new) / 2
        self.page.insert_image(
            pymupdf.Rect(x, self.y, x + w_new, self.y + h_new), pixmap=pix)
        self.y += h_new + 12


def numbered_source(path, max_lines=200):
    lines = open(path, encoding="utf-8", errors="replace").read().split("\n")
    return "\n".join(f"{i+1:3d} | {l}" for i, l in enumerate(lines[:max_lines]))


def main():
    ap = argparse.ArgumentParser(description="value-trace 导出 PDF")
    ap.add_argument("--repo", required=True, help="目标仓库路径")
    ap.add_argument("--anchor", required=True, help="value-trace 锚点节点 ID")
    ap.add_argument("--out", default="value-trace.pdf", help="输出 PDF 路径")
    ap.add_argument("--bin", default="codeintel", help="codeintel 二进制")
    ap.add_argument("--depth", type=int, default=6, help="value-trace 深度")
    ap.add_argument("--src-file", default="", help="源码文件（默认锚点文件）")
    ap.add_argument("--title", default="", help="PDF 标题（默认自动生成）")
    args = ap.parse_args()

    # 查询四格式
    text_out = run_cli(args.bin, args.anchor, args.repo, args.depth, [])
    tree_out = run_cli(args.bin, args.anchor, args.repo, args.depth, ["--format", "tree"])
    mermaid_out = run_cli(args.bin, args.anchor, args.repo, args.depth, ["--format", "mermaid"])
    json_out = run_cli(args.bin, args.anchor, args.repo, args.depth, ["--json"])

    # 源码（带行号）
    src_path = args.src_file
    if not src_path:
        src_path = f"{args.repo.rstrip('/')}/main.go"
    src_numbered = ""
    try:
        src_numbered = numbered_source(src_path)
    except Exception:
        src_numbered = "（源码文件不可读: %s）" % src_path

    # mermaid 渲染（失败跳过）
    img_path = None
    if mermaid_out.strip().startswith("flowchart"):
        data = render_mermaid(mermaid_out)
        if data:
            img_path = "/tmp/vt_mermaid_%d.jpg" % (abs(hash(args.anchor)) % 1000000)
            open(img_path, "wb").write(data)

    title = args.title or f"value-trace 四格式示例（{args.anchor}）"
    doc = pymupdf.open()
    w = Writer(doc)
    w.text(title, 16)
    w.text(f"锚点: {args.anchor}", 10)
    w.y += 4
    w.text("一、源码（带行号）", 13)
    w.code(src_numbered)
    w.text("二、默认文本格式", 13)
    w.code(text_out)
    w.text("三、--format tree", 13)
    w.code(tree_out)
    w.text("四、--format mermaid", 13)
    w.code(mermaid_out)
    if img_path:
        w.image(img_path)
    w.text("五、--json", 13)
    w.code(json_out)

    doc.save(args.out, garbage=4, deflate=True)
    print(f"PDF 已生成: {args.out} ({len(open(args.out,'rb').read())} bytes)")


if __name__ == "__main__":
    main()
