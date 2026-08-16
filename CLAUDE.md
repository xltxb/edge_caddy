# Edge Controller

自建 CDN 的控制面：主控 + 边缘节点，把配置意图变成各节点上真实生效的 Caddy 配置，
并让每一次变更可预览、可追溯、可回滚。

领域术语见 [CONTEXT.md](CONTEXT.md)，架构决定见 [docs/adr/](docs/adr/)。

## Agent skills

### Issue tracker

Issue 存在 GitHub 仓库 `xltxb/edge_caddy`。本地仓库**没有** remote，因此所有 `gh`
命令必须显式带 `-R xltxb/edge_caddy`。外部 PR 不作为请求来源，不进 triage 队列。
见 `docs/agents/issue-tracker.md`。

### Triage labels

五个 triage 角色使用中文标签：待评估 / 待补充信息 / 可交给agent / 待人工实现 / 不做。
见 `docs/agents/triage-labels.md`。

### Domain docs

单上下文：根目录 `CONTEXT.md` + `docs/adr/`。见 `docs/agents/domain.md`。
