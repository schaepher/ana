#!/bin/sh
# 安装 post-commit hook：本地写完代码提交后自动触发 codeintel serve
# 增量更新（field_trace.md §20.1）。
# 用法：scripts/install-git-hook.sh [仓库目录] [serve 端口]
#   serve 端口须与 codeintel serve --addr 一致（默认 8090）
set -e

REPO="${1:-.}"
PORT="${2:-8090}"
HOOK_DIR="$(cd "$REPO" && pwd)/.git/hooks"
HOOK="$HOOK_DIR/post-commit"

if [ ! -d "$HOOK_DIR" ]; then
  echo "error: $HOOK_DIR 不存在（$REPO 不是 git 仓库？）" >&2
  exit 1
fi

cat > "$HOOK" << HOOK_EOF
#!/bin/sh
# codeintel 增量更新（自动生成，勿手改；重新运行 install-git-hook.sh 覆盖）
curl -s -X POST "http://localhost:${PORT}/incremental" > /dev/null 2>&1 || true
HOOK_EOF
chmod +x "$HOOK"
echo "已安装 post-commit hook → $HOOK（serve 端口 ${PORT}）"
echo "提示：codeintel serve --repo $REPO --addr :${PORT} 需先启动，索引未构建时返回 404（先 init）"
