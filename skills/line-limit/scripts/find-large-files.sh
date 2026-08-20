#!/usr/bin/env bash
# 列出超过 300 行的 Go 文件（按行数降序）。
# 用法: find-large-files.sh [dir...]  默认 internal
set -euo pipefail
dirs=("$@")
[ ${#dirs[@]} -eq 0 ] && dirs=(internal)
find "${dirs[@]}" -name '*.go' -not -path '*/node_modules/*' \
  -not -path '*/.codeintel/*' -not -path '*/vendor/*' | \
  xargs wc -l 2>/dev/null | awk '$1 > 300 {print}' | sort -rn
