# Runbook：故障模式速查表（Q235，2026-08-22）

借鉴 GitNexus「Signs 故障模式表」（现象 → 处理 → 原因/预防）成文。
全部条目来自 codeintel 开发/运维真实踩坑。只收运维/环境类故障；
代码语义类教训（BFS 清空语义、taint 求交等）留在 field_trace.md
对应 Q 节。

| # | 现象 | 处理 | 原因与预防 |
|---|---|---|---|
| 1 | go build/test 链接失败 `No space left on device` | `rm -rf /tmp/go-build*`；或 `TMPDIR=/home/schaepher/.tmp-build go build` | /tmp 是 tmpfs，配额小。预防：大构建显式 TMPDIR |
| 2 | 大仓库 reindex/init 后 db 损坏 | 后台运行 + 轮询日志等完成，**勿强杀**；损坏则 clean + 重建 | 强杀写进程损坏 WAL。预防：大仓库计算一律后台+轮询 |
| 3 | `make e2e-fixture` 起不来 / 8096 被占 | 停掉占 8096 的进程（如 go2o serve）再跑 | 端口冲突。预防：跑 e2e 前检查 `ss -tlnp \| grep 8096` |
| 4 | pgrep 误杀自己（进程自杀） | 用 `pgrep -x <名>` + kill，不用 `pgrep -f` | `-f` 匹配整条命令行，会匹配自身。预防：精确进程名 |
| 5 | `schema version mismatch` | 加表类变更：新版本自动补建（Q235-3），直接打开；列变更：`codeintel clean --repo X --force` + init | schema 变更（Q235-3 后仅减法需 clean）。预防：见 field_trace.md Q235-3 |
| 6 | `query relations --all` 返回进度而非数据 | 先 `codeintel precompute relations --repo X`（或 serve 兜底后台计算） | Q228 进度协议：全量不再现场算。预防：precompute 后查询 |
| 7 | `go run ./skills/...` 报 `outside main module` | 用仓库真实路径 go run（软链路径仅 skill 发现用） | go module 从软链路径解析失败。预防：见 line-limit SKILL.md |
| 8 | 查询结果陈旧（改了代码没反映） | `codeintel update --repo X`（增量）或 reindex | 索引未更新。预防：改代码后 update |
| 9 | `sqlite busy / locked` | 停冲突进程重试；确认无残留 serve/计算进程 | 单写者连接池（_busy_timeout=5000）。预防：不并发写 |
| 10 | python 替换脚本静默不生效 | 替换前 `assert old in s` | `str.replace` 找不到串不报错。预防：脚本前置断言（Q225/Q226 教训） |
| 11 | bash cwd 被重置（命令在错误目录执行） | 命令用绝对路径；不用 cd 依赖相对路径 | shell cwd 不持久。预防：关键命令绝对路径 |
| 12 | ER 页面刷新后「全图画线」开关恢复 | 预期行为，非故障 | Q227 设计如此：开关不持久化 |
| 13 | 分析逻辑变更后查询结果未反映 | precompute relations 重跑（build_id 失效自动重算）或 update | relations 按 build_id 缓存；分析逻辑版本变更自动失效 |
| 14 | 索引与二进制 schema 版本不匹配（旧库） | 加表类自动补建；列变更 clean 重建 | schema 演进。预防：Q235-3 自动迁移 |
| 15 | probe/脚本用 `src[pos]` 反查源码字符错位 1 字节（把 '(' 看成 ')'） | 用 `fset.Position(pos).Offset` 索引源码，不用 token.Pos 直接索引 | token.Pos = base+offset（base≥1，多文件递增）。预防：源码反查一律经 Position（Q236 教训——错位曾把 Call.Pos=Lparen 误判成 Rparen，「死代码」误报） |
| 16 | serve 页面是旧版交互（前端改动没生效） | `go build -o codeintel ./cmd/codeintel` 重建二进制；`strings codeintel \| grep <新标记>` 验证 | 前端走 go:embed，构建时打包（Q236 P2：go2o 旧二进制嵌 Q228 页面） |
