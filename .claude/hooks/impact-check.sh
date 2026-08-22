#!/usr/bin/env bash
# Q235-2：impact-before-edit 非阻断提醒（PreToolUse on Edit/Write）。
# 对 Go 文件编辑前输出一行提示（注入代理上下文，不拒绝操作）：
#   - 仓库已索引 → 提醒改共享符号前先跑 impact 分析（AGENTS.md 强制流程）
#   - 仓库未索引 → 提醒先 init/update 补索引
# stdin: hook JSON（{"tool_name":..., "tool_input": {"file_path": ...}}）
set -euo pipefail

repo="${CLAUDE_PROJECT_DIR:-$PWD}"
file_path=""
if [ -t 0 ]; then
  echo "提示：非阻断 impact 检查需要 hook JSON stdin（Claude Code 自动传入）。" >&2
  exit 0
fi
# stdin 取 file_path（无 jq 依赖，python3 解析）
file_path=$(python3 -c "
import json, sys
try:
    d = json.load(sys.stdin)
    print(d.get('tool_input', {}).get('file_path', ''))
except Exception:
    print('')
")

case "$file_path" in
  *.go)
    if [ -f "$repo/.codeintel/codeintel.db" ]; then
      echo "提示：本仓库已索引。修改被引用的共享符号前，先运行 \`codeintel query impact <符号>\` 评估影响面（AGENTS.md 强制流程 / DoD 契约兼容）。"
    else
      echo "提示：仓库未索引（$repo）。改共享符号前建议先 \`codeintel init --repo <path>\` 以支持影响分析。"
    fi
    ;;
esac
exit 0
