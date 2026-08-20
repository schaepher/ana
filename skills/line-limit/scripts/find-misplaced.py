#!/usr/bin/env python3
"""定位错位/孤立注释（Q232）：顶层注释段落（连续 // 行，块间空行分隔
的每段独立）后（跳过空行）是声明但声明名 ≠ 段落首行名 → 错位。

输出判定：
- DUP-DEF(文件:行)  同包定义在其他文件且定义处已有相同注释 → 可删残留
- MOVE(文件:行)     同包定义在其他文件且定义处无注释 → 需搬移
- SELF-DEF          定义在本文件 → 正常文档（保留）
- NO-SAME-PKG-DEF   同包无定义（const 块内等）→ 误报（保留）

用法: find-misplaced.py [dir...]  默认 internal
修复: fix-comments.py（--apply 删 DUP-DEF / --move 搬 MOVE）"""
import os
import re

DECL_RE = re.compile(r'^(func|type|var|const|import|package)\b')
NAME_RE = re.compile(r'^//\s*([A-Za-z_][A-Za-z0-9_]*)')
DECLNAME_RE = re.compile(r'^(?:func(?: \([^)]*\))?|type|var)\s+([A-Za-z_][A-Za-z0-9_]*)')

def line_depths_of(text):
    lines = text.split('\n')
    depths = [0]
    depth = 0
    i = 0
    n = len(text)
    in_str = None
    cur = 0
    while i < n:
        c = text[i]
        if in_str:
            if in_str == '`':
                if c == '`':
                    in_str = None
            elif c == '\\':
                i += 2
                continue
            elif c == in_str:
                in_str = None
        else:
            if c in ('"', "'", '`'):
                in_str = c
            elif c == '/' and i + 1 < n and text[i+1] == '/':
                while i < n and text[i] != '\n':
                    i += 1
                continue
            elif c == '/' and i + 1 < n and text[i+1] == '*':
                i += 2
                while i + 1 < n and not (text[i] == '*' and text[i+1] == '/'):
                    i += 1
                i += 2
                continue
            elif c in '{([':
                depth += 1
            elif c in '})]':
                depth = max(0, depth - 1)
        if c == '\n':
            cur += 1
            depths.append(depth)
        i += 1
    return depths

def parse(path):
    text = open(path, encoding='utf-8').read()
    lines = text.split('\n')
    depths = line_depths_of(text)
    n = len(lines)
    pkg_line = None
    for i, l in enumerate(lines):
        if l.startswith('package '):
            pkg_line = i
            break
    out = []
    i = 0
    while i < n:
        if lines[i].lstrip().startswith('//') and depths[i] == 0:
            j = i
            while j < n and lines[j].lstrip().startswith('//'):
                j += 1
            # 段落（不含块间空行合并）
            para = lines[i:j]
            # 后跟声明？
            k = j
            while k < n and not lines[k].strip():
                k += 1
            decl_name = None
            if k < n:
                m = DECLNAME_RE.match(lines[k].lstrip())
                if m:
                    decl_name = m.group(1)
            out.append((i, j, para, decl_name, k))
            i = j
        else:
            i += 1
    return out, pkg_line

# 收集同包定义
defs = {}  # pkg -> name -> (path, line)
for root, dirs, files in os.walk('internal'):
    for fn in sorted(files):
        if not fn.endswith('.go'):
            continue
        p = os.path.join(root, fn)
        pkg = os.path.dirname(p)
        with open(p, encoding='utf-8') as f:
            for ln, line in enumerate(f, 1):
                m = DECLNAME_RE.match(line.strip())
                if m:
                    defs.setdefault(pkg, {})[m.group(1)] = (p, ln)

total = 0
for root, dirs, files in os.walk('internal'):
    for fn in sorted(files):
        if not fn.endswith('.go'):
            continue
        p = os.path.join(root, fn)
        paras, pkg_line = parse(p)
        pkg = os.path.dirname(p)
        for start, end, para, decl_name, k in paras:
            if pkg_line is not None and end <= pkg_line:
                continue  # package 文档
            m = NAME_RE.match(para[0].strip())
            if not m:
                continue
            name = m.group(1)
            if decl_name is not None and decl_name == name:
                continue  # 正常文档
            # 错位/孤立：段落名 vs 实际
            verdict = f'decl={decl_name}'
            d = defs.get(pkg, {}).get(name)
            if d is None:
                verdict += ' NO-SAME-PKG-DEF'
            elif d[0] != p:
                # 定义在别的文件——查定义处是否有注释
                with open(d[0], encoding='utf-8') as f2:
                    dl = f2.readlines()
                near = ''.join(dl[max(0, d[1]-41):d[1]])
                if f'// {name}' in near:
                    verdict += f' DUP-DEF({d[0]}:{d[1]})'
                else:
                    verdict += f' MOVE({d[0]}:{d[1]})'
            else:
                verdict += f' SELF-DEF({d[1]})'
            total += 1
            print(f'{p}:{start+1} [{name}] {verdict}')
print(f'--- total {total} ---')
