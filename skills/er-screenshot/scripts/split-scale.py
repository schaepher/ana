#!/usr/bin/env python3
"""ER 图长截图缩放 + 切分（Q238：fullPage 长图在 IM 里不可读——缩到
宽度 1200 再切成多段，每段 ~1600px 高）。

用法:
  python3 split-scale.py <in.png> <out_prefix> [--width 1200] [--seg 1620]

输出: <out_prefix>-scaled.png（整体缩放版）+ <out_prefix>-partN.png（切段）。
依赖: Pillow (pip install --break-system-packages pillow)。
"""
import argparse
import os

from PIL import Image


def main():
    ap = argparse.ArgumentParser(description="ER 图长截图缩放切分")
    ap.add_argument("in_file", help="输入截图（fullPage 长图）")
    ap.add_argument("out_prefix", help="输出前缀（如 /tmp/er）")
    ap.add_argument("--width", type=int, default=1200, help="缩放宽度（默认 1200）")
    ap.add_argument("--seg", type=int, default=1620, help="切段高度（默认 1620）")
    args = ap.parse_args()

    im = Image.open(args.in_file)
    w, h = im.size
    nw = args.width
    nh = int(h * nw / w)
    im2 = im.resize((nw, nh), Image.LANCZOS)
    scaled = f"{args.out_prefix}-scaled.png"
    im2.save(scaled)
    print(f"缩放: {w}x{h} -> {nw}x{nh} -> {scaled}")

    # 切段（重叠 60px 防衔接处信息丢失；末段后退出防死循环）
    overlap = 60
    parts = []
    top = 0
    i = 1
    while top < nh:
        bottom = min(top + args.seg, nh)
        part = f"{args.out_prefix}-part{i}.png"
        im2.crop((0, top, nw, bottom)).save(part)
        parts.append(part)
        print(f"切段: {part} (y {top}-{bottom})")
        if bottom >= nh:
            break
        top = bottom - overlap
        i += 1


if __name__ == "__main__":
    main()
