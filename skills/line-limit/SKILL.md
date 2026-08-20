---
name: line-limit
license: 'MIT'
description: 'Go 文件行数治理：把超过 300 行的文件拆分到 ≤300 行（skill 自带 asttool analyze + split 按声明分组搬移），并清理拆分残留的孤立/错位注释（方法实现拆走后注释留在原文件、与实现文件重复）。当用户要求检查超行文件、拆分大文件、移除孤立注释/孤立修饰、整理注释时使用本 skill。'
---

# Go 文件行数治理（≤300 行 + 孤立注释清理）

流程沉淀自 Q231（拆分）与 Q232（孤立注释清理）。分四步：检测 → 拆分
大文件 → 清理注释 → 验证。

## 1. 检测

```bash
scripts/find-large-files.sh            # 列出 >300 行 Go 文件（默认 internal）
scripts/find-misplaced.py              # 列出孤立/错位注释（默认 internal）
```

`find-misplaced.py` 输出判定：

| 判定 | 含义 | 处理 |
|---|---|---|
| `DUP-DEF(文件:行)` | 同包定义在其他文件，定义处已有相同注释 | 删残留（`fix-comments.py --apply`） |
| `MOVE(文件:行)` | 同包定义在其他文件，定义处无注释 | 搬移（`fix-comments.py --move`） |
| `SELF-DEF` | 定义在本文件 | 正常文档，保留 |
| `NO-SAME-PKG-DEF` | 同包无定义（const 块内字段注释等） | 误报，保留 |

## 2. 拆分大文件（>300 行）

asttool 随 skill 提供（scripts/asttool）。软链路径 `~/.claude/skills/
line-limit` 仅用于 skill 发现（Claude 读取 SKILL.md）；go run 须用真实
路径（go module 从软链路径解析失败）。在 codeintel 仓库内用相对路径
`go run ./skills/line-limit/scripts/asttool`；其他仓库用
`go run <skill 仓库真实路径>/skills/line-limit/scripts/asttool`。
建议先 `cd <目标仓库>` 再运行（analyze/split 只读/写当前目录下的文件）：

```bash
# 1) 查看声明清单，规划按主题分组
go run ./skills/line-limit/scripts/asttool analyze <pkg>/<file>.go

# 2) 按主题分组拆分（out 文件新建；保留原文件的声明）
go run ./skills/line-limit/scripts/asttool split <src.go> \
  <out1.go>:Name1,Name2 <out2.go>:Name3,...

# 3) import 清理（split 保留原文件 import 全集——unused import）
goimports -w <改动文件>

# 4) 跨主题方法手动移动（split 只新建文件；如进度方法并入已有文件）
```

## 3. 清理孤立注释

```bash
scripts/find-misplaced.py                # dry-run 预览
scripts/fix-comments.py --apply          # 删 DUP-DEF 残留（安全：实现文件有副本）
scripts/fix-comments.py --move           # 搬 MOVE 注释到同包定义文件定义前
```

`--move` 带断言：源段落必须全为注释/空行（误伤即中断不写文件）；目标
定义前 3 行内有同名注释则只删源不插入（防重复）；同文件按行号从后往前
处理（删除偏移）。

搬移后复查：`find-misplaced.py` 可能报「搬移挤开的相邻注释」（插入点
前已有其他方法注释）——把被挤开的注释移到各自定义前（内容匹配移动）。

## 4. 验证

```bash
scripts/verify.sh                         # gofmt + build + 超行复查 + 孤立复查
make test && make e2e-fixture             # 全量测试（交付前）
```

## 已知坑

- **跨包同名函数**（relPath/isInModule/findNode 等各包独立实现）注释相同
  是复制不是残留——find-misplaced 已限定同包定义，勿跨包处理
- **行数收敛的副作用**：删注释只减行数；方法抽取的签名/日志开销可能使
  行数不减反增（emitElementOp 初抽 312 > 310）——移到行数余量大的文件
- **文件尾孤立注释**（方法拆走注释残留、与实现文件重复）——Q231 案例
  repo.go 尾部 130 行；`find-misplaced.py` 全覆盖（不限文件尾）
- 运行前 `git checkout -- internal/` 可回滚脚本误操作；脚本执行前先
  dry-run 预览

## 配套

- 本 skill 自带工具与脚本：scripts/asttool（analyze/split/migrate/
  orphan）+ scripts/（find-large-files.sh / find-misplaced.py /
  fix-comments.py / verify.sh）
- 全局软链：~/.claude/skills/line-limit → 仓库 skills/line-limit/
  （skill 随仓库版本化；改动后无需重新软链）
