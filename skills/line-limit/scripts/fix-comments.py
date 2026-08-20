#!/usr/bin/env python3
"""修复错位/孤立注释（Q232）：基于 find-misplaced.py 的判定。

用法:
  fix-comments.py [--apply]  删除 DUP-DEF 残留（默认 dry-run 列出）
  fix-comments.py --move     搬移 MOVE 注释到同包定义文件定义前（带
                              断言：源段落全为注释/空行；目标定义前 3
                              行内无同名注释防重复；同文件按行号从后往
                              前处理防偏移）

安全边界：
- 只删 DUP-DEF（实现文件已有相同文档，删除有副本兜底）
- 搬移带断言，误伤即中断（assert 失败不写文件）
- SELF-DEF / NO-SAME-PKG-DEF（const 块内等误报）不动
"""
import os
import re
import subprocess
import sys

ROOT = os.path.dirname(os.path.abspath(__file__))

def scan():
    return subprocess.run(
        [sys.executable, os.path.join(ROOT, 'find-misplaced.py')] + sys.argv[1:] if False else
        [sys.executable, os.path.join(ROOT, 'find-misplaced.py')],
        capture_output=True, text=True).stdout

def main():
    args = sys.argv[1:]
    do_apply = '--apply' in args
    do_move = '--move' in args
    out = scan()
    deletes = {}   # path -> [(start, end)] 0-based
    moves = []     # (src, start, end, name, dst, dst_line)
    for line in out.splitlines():
        if '[' not in line or ']' not in line:
            continue
        head, rest = line.split(']', 1)
        path, _, ln = head.rpartition(':')
        ln = ln.split('[')[0].strip()
        start = int(ln) - 1
        m = re.search(r'DUP-DEF\(([^:]+):(\d+)\)', rest)
        if m:
            deletes.setdefault(path, []).append((start, None))
            continue
        m = re.search(r'MOVE\(([^:]+):(\d+)\)', rest)
        if m:
            name = line.split('[')[1].split(']')[0].strip()
            moves.append((path, start, name, m.group(1), int(m.group(2))))
    # 去重：同 (name, dst) 只处理一次；同文件按行号从后往前
    seen = set()
    moves = [m for m in moves if not (m[2], m[3]) in seen and not seen.add((m[2], m[3]))]
    moves.sort(key=lambda x: (-x[1], x[0]))

    if do_apply:
        for path, ranges in deletes.items():
            lines = open(path, encoding='utf-8').read().split('\n')
            for s, e in sorted(ranges, reverse=True):
                e2 = s
                while e2 < len(lines) and lines[e2].lstrip().startswith('//'):
                    e2 += 1
                del lines[s:e2]
            open(path, 'w', encoding='utf-8').write('\n'.join(lines))
            print(f'DEL {path}: {len(ranges)} blocks')
    elif not do_move:
        for path, ranges in deletes.items():
            for s, _ in ranges:
                print(f'[dry-run] DEL {path}:{s+1}（--apply 执行）')
    if do_move:
        for src, sl, name, dst, dl in moves:
            slines = open(src, encoding='utf-8').read().split('\n')
            start = sl - 1
            end = start
            while end < len(slines):
                if slines[end].lstrip().startswith('//'):
                    end += 1
                elif not slines[end].strip():
                    k = end
                    while k < len(slines) and not slines[k].strip():
                        k += 1
                    if k < len(slines) and slines[k].lstrip().startswith('//'):
                        end = k
                    else:
                        break
                else:
                    break
            para = slines[start:end]
            for p in para:
                assert p.strip() == '' or p.lstrip().startswith('//'), \
                    f'{src}:{start+1} 非注释行: {p[:40]}'
            dlines = open(dst, encoding='utf-8').read().split('\n')
            di = next((i for i, l in enumerate(dlines) if l.strip().startswith(
                f'func {name}') or re.match(r'^func(?: \([^)]*\))?\s+' + re.escape(name) + r'[\( ]', l.strip())), None)
            if di is None:
                print(f'{src}:{start+1} [{name}] 目标 {dst} 定义未找到，跳过')
                continue
            near = '\n'.join(dlines[max(0, di-3):di])
            if f'// {name}' in near:
                print(f'{src}:{start+1} [{name}] 目标已有注释，只删源')
                del slines[start:end]
                open(src, 'w', encoding='utf-8').write('\n'.join(slines))
                continue
            dlines[di:di] = para + ['']
            open(dst, 'w', encoding='utf-8').write('\n'.join(dlines))
            del slines[start:end]
            open(src, 'w', encoding='utf-8').write('\n'.join(slines))
            print(f'MOVE {src}:{start+1} [{name}] -> {dst}:{dl}')
    if not do_apply and not do_move:
        print('--- dry-run：--apply 删 DUP-DEF，--move 搬 MOVE ---')

if __name__ == '__main__':
    main()
