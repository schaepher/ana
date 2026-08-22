# Q238 设计：全局注册表 + worktree/workspace 支持（2026-08-22）

四轮设计访谈收敛（16 问，全部确认）。实施后归档并入 field_trace.md。

## 1. 需求背景

用户要求：
- home 目录下单独建 `.codeintel/` 目录，放 sqlite db 管理全局（**全局注册表**）
- 所有 init 执行后自动注册自身信息（**含路径**）
- 目标：**全局任意位置执行命令都可指定特定 repo**（不必 cd 进仓库）
- 支持 **git worktree** 场景（同一仓库多个 worktree，代码状态不同）
- 支持 **workspace worktree** 场景：把所涉及的项目按需求创建 git
  worktree 到 workspace 目录里

## 2. 决策树（四轮，Q1–Q16）

### 第一轮：骨架

| # | 决策 | 结论 |
|---|---|---|
| Q1 | 注册表用途 | **list 台账 + worktree/workspace 归属管理**；不做 serve/query 自动发现（保持显式 --repo）；不做全局配置载体 |
| Q2 | 全局库文件 | `~/.codeintel/codeintel.db`（单文件纯台账） |
| Q3 | 注册目标 | 以「全局任何地方可指定特定 repo」为设计目标（路径是核心注册信息） |
| Q4 | worktree 索引策略 | **每个 worktree 独立 `.codeintel/` 索引** + 注册表记 `worktree_of` 归属（隔离无污染） |
| Q5 | workspace 形态 | 基于注册表驱动创建 worktree；主仓库限本机已存在路径（不自动 clone）；分支默认当前分支，`--branch` 可覆盖 |

### 第二轮：命令与条目语义

| # | 决策 | 结论 |
|---|---|---|
| Q6 | 命令引用已注册 repo | **`--repo` 无缝扩展**：先按文件系统解析，失败后查注册表（绝对路径后缀 / 目录名 / module 名唯一匹配），多命中报候选；不引入第二参数面 |
| Q7 | worktree 条目形态 | **扁平 repos 表**：worktree 独立条目 + `worktree_of` 指向主仓库；主仓库注销时级联注销 worktree 条目 |
| Q8 | list 内容 | 短名/路径/module/状态（已构建·过期·未构建）/worktree 归属/workspace 归属；过滤 `--worktree-of` / `--workspace` / `--module` / `--stale` |
| Q9 | workspace 执行 | 目录不存在自动创建；已存在幂等（该仓库已有 worktree 跳过）；**默认只创建 worktree + 注册（不构建索引）**，`--build` 单独控制；单个失败不中断，汇总报告 |

### 第三轮：命令面与边界

| # | 决策 | 结论 |
|---|---|---|
| Q10 | workspace 命令形态 | `codeintel workspace init --dir <目录> [--repo <子集>...] [--build]`；缺省子集=注册表全部 |
| Q11 | worktree 失效清理 | `codeintel workspace prune`：扫描注册条目，目录不存在的删除；list 先标记 `[missing]` 不静默 |
| Q12 | 全局库损坏/重建 | 库缺失自动重建（幂等 schema）；损坏（SQLITE_CORRUPT）报错提示手动删除；注册表从不作为命令必需前置 |
| Q13 | 缺省 cwd 边界提示 | 缺省（cwd 非仓库且无 .codeintel）报错附引导：「已注册 N 个仓库，codeintel list 查看；或 --repo <短名>」；显式 --repo 不附 |

### 第四轮：解析细节与状态

| # | 决策 | 结论 |
|---|---|---|
| Q14 | --repo 解析顺序 | ① 文件系统存在（现行语义）→ ② 注册表绝对路径后缀（唯一）→ ③ 目录名（唯一）→ ④ module 名（唯一）；多命中必报候选，不静默取第一个 |
| Q15 | 未构建 vs 过期 | 三态独立：「已构建」（head 一致）/「过期」（已构建但 HEAD 变）/「未构建」（注册未构建，workspace 默认）；`--stale` 只筛过期，`--unbuilt` 筛未构建 |
| Q16 | 全局库 schema 与并发 | 加法自动补（Q235-3 模式）；**列变更采用自动重建表 + 数据迁移**（repos 表小，避免手动删丢台账）；单写者 busy_timeout=5000 + 短重试（同现有 sqlite 模式） |

## 3. 设计细节

### 3.1 全局注册表（~/.codeintel/codeintel.db）

```sql
CREATE TABLE repos (
  id            INTEGER PRIMARY KEY,
  path          TEXT NOT NULL UNIQUE,   -- 绝对路径（主仓库或 worktree 工作目录）
  module        TEXT,                   -- module 名（多 go.mod 取根）
  go_mod_count  INTEGER,
  head_commit   TEXT,                   -- 注册/刷新时的 HEAD
  build_id      TEXT,                   -- 最近一次构建的 build_id（未构建为空）
  last_built_at TEXT,                   -- 最近构建时间（未构建为空）
  is_worktree   INTEGER NOT NULL DEFAULT 0,
  worktree_of   TEXT,                   -- 主仓库绝对路径（is_worktree=1 时有值）
  workspace     TEXT,                   -- 归属 workspace 目录（可选）
  registered_at TEXT NOT NULL
);
```

- 迁移：user_version 独立管理；加法补列（Q235-3 模式）；列变更
  自动重建表+迁移数据（repos 表小，成本低，避免手动删库丢台账）
- 并发：单写者 + busy_timeout=5000 + 短重试（与仓库内 db 同模式）
- 主仓库注销（clean）时：级联删除 `worktree_of` 指向它的条目

### 3.2 注册/注销时机（Q3）

| 事件 | 行为 |
|---|---|
| init 成功 | 注册（含 path/module/HEAD/build_id/registered_at；worktree 检测：`.git` 为 gitdir 指针文件或 `git worktree list` 命中 → is_worktree=1 + worktree_of=主仓库） |
| reindex 成功 | 同 init（覆盖注册） |
| update 成功 | 只刷新 head_commit/last_built_at/build_id |
| clean | 注销（级联 worktree 条目） |
| init/reindex/update 失败 | 不注册/不刷新 |
| serve/query | 不触发注册 |

### 3.3 --repo 解析（Q6/Q14）

```
--repo X 解析顺序：
  1. 文件系统：X 存在（绝对/相对路径）→ 直接用（现行语义）
  2. 注册表绝对路径后缀精确匹配（唯一）→ 用
  3. 注册表目录名精确匹配（唯一）→ 用（worktree 条目目录名天然不同）
  4. 注册表 module 名精确匹配（唯一）→ 用
  多命中 → 报候选列表（不静默）
  未命中 → 原路径错误
```

### 3.4 命令面

```
codeintel list [--worktree-of <主仓库>] [--workspace <目录>]
              [--module <片段>] [--stale] [--unbuilt] [--json]
  台账：短名(=目录名) 路径 module 状态(已构建/过期/未构建/[missing]) 
       worktree归属(⊢主仓库) workspace归属

codeintel workspace init --dir <目录> [--repo <子集>...] [--build] [--branch <b>]
  在 <目录> 下为每个目标仓库 git worktree add（分支默认当前分支，
  --branch 覆盖）；已有 worktree 跳过（幂等）；随后注册 worktree
  条目（is_worktree=1 + worktree_of + workspace 归属）；
  默认不构建索引（条目状态=未构建），--build 时逐个 init 构建。

codeintel workspace prune
  扫描注册条目：目录不存在的删除（worktree 与主仓库条目）；
  list 中这类条目先标记 [missing]。
```

- `--repo` 模糊匹配对 workspace 子集生效（`--repo go2o,ana` 或重复 `--repo`）

### 3.5 状态机（Q8/Q15）

```
未构建（注册未 init）─── init 成功 ──→ 已构建（head 一致）
已构建 ── HEAD 变更 ─────────────→ 过期（--stale 命中）
过期 ── update/init 成功 ────────→ 已构建
任何状态 ── 目录消失（prune 前）──→ [missing]
```

- `--stale` 判定：注册表存 head_commit，与当前 `git rev-parse HEAD`
  对比（不打开索引库）；未构建 ≠ 过期（--unbuilt 单独筛）

### 3.6 缺省 cwd 边界（Q13）

cwd 非仓库（无 go.mod）且无 `.codeintel/` 时，缺省解析报错附引导：
「已注册 N 个仓库，可用 codeintel list 查看；或 --repo <短名> 指定」；
显式 --repo 不附引导。

## 4. 实施范围

1. `internal/infrastructure/sqlite` 或新包：全局注册表仓库（OpenGlobal/
   RegisterRepo/RefreshRepo/UnregisterRepo/ListRepos/ResolveRepo——
   单写者 + 幂等 schema + 自动重建）
2. worktree 检测（`.git` gitdir 指针 / `git worktree list`）+ worktree_of 提取
3. init/reindex/update/clean 钩子（成功注册/刷新/注销）
4. `--repo` 解析扩展（文件系统 → 注册表四步）+ 缺省 cwd 报错引导
5. `codeintel list`（含过滤/--json/状态机）
6. `codeintel workspace init/prune`（幂等 + --build + 汇总报告）
7. usage/文档/skill/记忆同步

## 5. 测试矩阵（形态矩阵）

- 注册生命周期：init 成功注册 / 失败不注册 / update 刷新 / clean 注销（含级联）
- 全局库：缺失自动重建 / 损坏报错提示 / schema 加法迁移 / 列变更重建迁移
- worktree：检测（gitdir 指针 / worktree list）/ 独立索引 / worktree_of 关联 /
  级联注销 / prune 清理 [missing]
- workspace：init 幂等（重跑跳过）/ --repo 子集 / --build 构建 /
  分支默认与覆盖 / 单仓库失败继续汇总
- --repo 解析：路径优先 / 后缀 / 目录名 / module / 多命中候选 / 未命中报错
- 状态机：已构建 / 过期（HEAD 变）/ 未构建 / [missing]；--stale 与 --unbuilt 过滤
- 缺省 cwd 引导提示（缺省附 / 显式不附）

## 6. 未决/开放项

- 无（四轮收敛）。实施中如遇实现级分歧（如 worktree 检测的边界形态），
  按 confirm-before-deciding 先与用户确认。
