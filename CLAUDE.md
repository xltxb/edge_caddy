# Edge Controller

自建 CDN 的控制面：主控 + 边缘节点，把配置意图变成各节点上真实生效的 Caddy 配置，
并让每一次变更可预览、可追溯、可回滚。

领域术语见 [CONTEXT.md](CONTEXT.md)，架构决定见 [docs/adr/](docs/adr/)，
HTTP / WS 契约见 [docs/api-contract.md](docs/api-contract.md)。

## 权威在哪

设计项目（claude.ai/design）里的三份文档——PRD、前端开发文档、后端开发文档——
**冻结在 v1.0**，记录的是 2026-08-15 那一刻的共识，不再更新。

**活的权威是仓库里的这三样**，按优先级：

1. `docs/adr/` —— 架构决定。与设计文档冲突时**以 ADR 为准**，且必须在输出里挑明冲突，
   不要静默覆盖（见 `docs/agents/domain.md`）。
2. `docs/api-contract.md` —— HTTP / WS 契约。前后端的接缝，改它等于改接口，要同步对方。
3. `CONTEXT.md` —— 术语表。

已知设计文档有 9 处与 ADR 相抵触（PRD §4 §7；后端 §2 §3 §4 §5 §6 §8；前端 §6）。
不要照那些段落实现。

## 前后端分工

同一仓库，目录级隔离，两个 agent 并行：

- **后端**（本会话）：`cmd/` `internal/` `proto/` `migrations/` `docs/` 与根级 `go.mod`。
- **前端**（agent 名 `frontend`）：`web/`。

`CONTEXT.md`、`docs/adr/`、`docs/api-contract.md` 两边共享。契约有变动时用 SendMessage
通知对方，别指望它会主动来读 diff。

## Agent skills

### Issue tracker

Issue 存在 GitHub 仓库 `xltxb/edge_caddy`，`origin` 已指向它，`gh` 命令不必再带 `-R`。
外部 PR 不作为请求来源，不进 triage 队列。
见 `docs/agents/issue-tracker.md`。

### Triage labels

五个 triage 角色使用中文标签：待评估 / 待补充信息 / 可交给agent / 待人工实现 / 不做。
见 `docs/agents/triage-labels.md`。

### Domain docs

单上下文：根目录 `CONTEXT.md` + `docs/adr/` + `docs/api-contract.md`（动接口前必读）。
见 `docs/agents/domain.md`。
