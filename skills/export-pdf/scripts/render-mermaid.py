#!/usr/bin/env python3
"""mermaid 渲染（mermaid.ink API）：mermaid 源码 → 图片。

用法:
  python3 render-mermaid.py <mermaid_file> <out.png|jpg> [--width 2000]

依赖: 网络（mermaid.ink）。失败 exit 1 并打印错误。
"""
import argparse
import base64
import sys
import urllib.request


def render(graph, width=2000):
    enc = base64.urlsafe_b64encode(graph.encode()).decode()
    req = urllib.request.Request(
        f"https://mermaid.ink/img/{enc}?width={width}",
        headers={"User-Agent": "Mozilla/5.0"})
    return urllib.request.urlopen(req, timeout=30).read()


def main():
    ap = argparse.ArgumentParser(description="mermaid 渲染")
    ap.add_argument("mermaid_file", help="mermaid 源码文件")
    ap.add_argument("out", help="输出图片路径（png/jpg）")
    ap.add_argument("--width", type=int, default=2000, help="渲染宽度（默认 2000）")
    args = ap.parse_args()

    graph = open(args.mermaid_file, encoding="utf-8").read()
    try:
        data = render(graph, args.width)
    except Exception as e:
        sys.exit(f"mermaid.ink 渲染失败（网络?）: {e}")
    open(args.out, "wb").write(data)
    print(f"已渲染: {args.out} ({len(data)} bytes)")


if __name__ == "__main__":
    main()
