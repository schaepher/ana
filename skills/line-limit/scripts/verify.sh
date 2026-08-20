#!/usr/bin/env bash
# 行数治理验证：gofmt + build + 超行复查 + 孤立注释复查。
# 用法: verify.sh [dir...]  默认 internal（在仓库根运行）
set -euo pipefail
dirs=("$@")
[ ${#dirs[@]} -eq 0 ] && dirs=(internal)
echo "== gofmt =="
unformatted=$(gofmt -l "${dirs[@]}" 2>/dev/null | head -5)
[ -n "$unformatted" ] && echo "$unformatted" && echo "gofmt FAIL" && exit 1
echo "gofmt ok"
echo "== build =="
go build ./... && echo "build ok"
echo "== 超行检查（>300）=="
over=$(find "${dirs[@]}" -name '*.go' | xargs wc -l 2>/dev/null | awk '$1 > 300 {print}')
if [ -n "$over" ]; then echo "$over"; else echo "无超行文件 ok"; fi
echo "== 孤立注释检查 =="
"$(dirname "$0")/find-misplaced.py" "${dirs[@]}" | grep -E 'DUP-DEF|MOVE' || echo "无孤立/错位注释 ok"
