---
name: handoff
license: 'MIT'
description: '生成交接文档：把当前会话总结成一份新 agent 可续作的文档，保存到 OS 临时目录（非 workspace）。包含 Suggested Skills 部分；引用而非重复既有工件（specs/文档/commit）；脱敏敏感信息。当用户说「handoff」「写交接文档」「Write a handoff document」或要求会话交接时使用本 skill。'
---

# 交接文档（handoff）

把当前会话的上下文总结成交接文档，保存到 OS 临时目录（非当前 workspace），
供新 agent 续作。

## 前置

- git 仓库（commit 序列收集）
- 本 skill 无外部依赖

## 流程

1. **确定 commit 范围**：读 `/tmp/codeintel-handoff-*` 最新一份交接文档，
   取其中记录的 HEAD 作为起点；`git log <起点>..HEAD --oneline` 收集
   本会话 commit 序列（无上一份则从会话起点开始）
2. **收集当前状态**：`git log -1 --oneline`（HEAD）、验证基线、
   索引状态、未提交变更
3. **写文档**（模板见下）到
   `/tmp/codeintel-handoff-<YYYYMMDD>-<字母>.md`（字母按当天已有文件
   递增：a、b、c……）
4. **脱敏检查**：无 API 密钥/密码/PII；私有仓库名用「验证仓库（私有）」
   指代；fixture 脱敏表名勿还原

## 文档模板（六节）

```markdown
# codeintel 交接文档 <字母>（<日期>，Q<范围>）

本会话从 <上一份> 延续至 <Q范围>，共 <N> 个 commit（<起点> → <HEAD>，
HEAD = <HEAD>）。此前交接见 <上一份路径>；<已归档设计文档> 决策记录在
field_trace.md §<编号>。

## 1. 本会话交付（按 commit，细节引 field_trace.md，不重复）

| commit | 内容 | 落档 |
|---|---|---|
| <hash> | <一句话> | §<编号> |

**端到端验证**：<关键验证结果，一行>

## 2. 当前状态

- HEAD：<hash>（main），工作区干净与否
- 验证基线：13 包全绿（`go test -race -count=1 -p 1 ./...`）
- 索引/注册表状态

## 3. 已知遗留 / 待评估（按优先级）

<N> 条：<Q 编号候选 + 一句话 + 依赖>

## 4. 工作方式约定（引用 AGENTS.md，不重复）

- 测试先行、改前给效果对比、改完验证后 git push（三令五申）
- 新功能：先 design-qXXX.md + 访谈（Q 编号 + 推荐答案，逐轮确认）→
  实施后归档并入 field_trace.md §N
- 本会话新教训（已入 runbook/文档）：<列出，每条一行>

## 5. Suggested Skills

- <skill 名>（<位置>）：<用途>——<本会话新增的相关命令/参数>
- ...

## 6. 敏感信息

- 无 API 密钥/密码/PII
- 私有验证仓库名称未出现（以「验证仓库（私有）」指代）
- fixture 业务表名已脱敏（中性名）——勿还原
```

## 要点

- **不重复既有工件**：设计文档/field_trace.md/commit 已记录的内容只
  引用（路径或 § 编号），不复制正文
- **Suggested Skills 节必填**：列出会话相关 skill（项目内 skills/ 与
  全局 ~/.claude/skills/），标注新增命令/参数
- **上一份交接的字母续号**：`ls /tmp/codeintel-handoff-<今天>*` 取最大
  字母 +1
- 文档语言与项目一致（中文）；commit 表用 `git log --oneline`
