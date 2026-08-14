#!/bin/sh
# 安装 post-commit hook：本地写完代码提交后自动更新 codeintel 索引
# （field_trace.md §20.1/§20.3）。
# 用法：scripts/install-git-hook.sh [仓库目录] [serve 端口] [--direct]
#   --direct：不依赖 serve，post-commit 直接运行 codeintel update
#             （确定性更新，代价：每次提交跑全量分析）
set -e

REPO="${1:-.}"
shift || true
PORT="8090"
MODE="curl"
for arg in "$@"; do
  case "$arg" in
    --direct) MODE="direct" ;;
    *) PORT="$arg" ;;
  esac
done

HOOK_DIR="$(cd "$REPO" && pwd)/.git/hooks"
HOOK="$HOOK_DIR/post-commit"
if [ ! -d "$HOOK_DIR" ]; then
  echo "error: $HOOK_DIR 不存在（$REPO 不是 git 仓库？）" >&2
  exit 1
fi

if [ "$MODE" = "direct" ]; then
  # 直连模式：直接跑 codeintel update（不需要 serve）
  CODEMODULE="$(cd "$REPO" && git rev-parse --show-toplevel 2>/dev/null || echo "$REPO")"
  cat > "$HOOK" << HOOK_EOF
#!/bin/sh
# codeintel 增量更新（直连模式，自动生成）
if ! command -v codeintel > /dev/null 2>&1; then
  echo "⚠ codeintel 未安装，索引未更新（go install 后重试）" >&2
  exit 0
fi
codeintel update --repo "$CODEMODULE" > /dev/null 2>&1 || \\
  echo "⚠ codeintel update 失败，索引可能过期（查看 $CODEMODULE/.codeintel/codeintel.log）" >&2
HOOK_EOF
  echo "已安装 post-commit hook（直连模式）→ $HOOK"
  echo "提示：codeintel 需在 PATH；update 为全量分析+增量写入（大仓提交可能变慢）"
else
  # curl 模式：需 serve 运行
  cat > "$HOOK" << HOOK_EOF
#!/bin/sh
# codeintel 增量更新（curl 模式，自动生成）
if ! curl -s -X POST "http://localhost:${PORT}/incremental" > /dev/null 2>&1; then
  echo "⚠ codeintel 索引未更新（serve 未启动？）——先启动 'codeintel serve --addr :${PORT}' 或手动 'codeintel update'" >&2
fi
HOOK_EOF
  echo "已安装 post-commit hook（curl 模式）→ $HOOK（serve 端口 ${PORT}）"
  echo "提示：codeintel serve --repo $REPO --addr :${PORT} 需先启动；索引未构建时返回 404（先 init）"
fi
chmod +x "$HOOK"
