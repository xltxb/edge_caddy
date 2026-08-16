# Issue tracker: GitHub

本仓库的 issue 与 PRD 存在 GitHub 仓库 **`xltxb/edge_caddy`**，用 `gh` CLI 操作。

## 必须显式指定仓库

本地 git 仓库**没有配置 remote**，`gh` 无法从 `git remote -v` 推断出目标仓库。
所有命令都要带 `-R xltxb/edge_caddy`：

```bash
gh issue list -R xltxb/edge_caddy
```

不加 `-R` 时 `gh` 会报「无法确定仓库」而不是猜错仓库，所以不会写错地方——
但每条命令都会失败一次，别忘了带。

**这只决定 issue 放哪，不代表代码也推那个仓库。** 两件事目前是分开的：
`xltxb/edge_caddy` 的代码正准备清空，而本项目是重建版、尚未决定推向何处。
哪天决定了代码也放那儿，再 `git remote add origin` 并把本节的 `-R` 说明删掉。

## Conventions

- **创建 issue**：`gh issue create -R xltxb/edge_caddy --title "..." --body "..."`。
  多行正文用 heredoc。
- **读 issue**：`gh issue view <number> -R xltxb/edge_caddy --comments`，需要标签时一并取。
- **列 issue**：
  ```bash
  gh issue list -R xltxb/edge_caddy --state open \
    --json number,title,body,labels,comments \
    --jq '[.[] | {number, title, body, labels: [.labels[].name], comments: [.comments[].body]}]'
  ```
  按需加 `--label` / `--state` 过滤。
- **评论**：`gh issue comment <number> -R xltxb/edge_caddy --body "..."`
- **打／去标签**：`gh issue edit <number> -R xltxb/edge_caddy --add-label "..."` / `--remove-label "..."`
  标签是中文，**必须加引号**（见 `triage-labels.md`）。
- **关闭**：`gh issue close <number> -R xltxb/edge_caddy --comment "..."`

## Pull requests as a triage surface

**PRs as a request surface: no.** _（若本仓库把外部 PR 也当作功能请求，改成 `yes`；`/triage` 读这个标志。）_

这是一个内部运维系统，请求来源只有 issue。设成 `yes` 时，PR 会走与 issue 相同的
标签与状态，用 `gh pr` 系列命令：

- **读 PR**：`gh pr view <number> -R ... --comments`，diff 用 `gh pr diff <number> -R ...`
- **列出待 triage 的外部 PR**：`gh pr list -R ... --state open --json number,title,body,labels,author,authorAssociation,comments`
  然后只保留 `authorAssociation` 为 `CONTRIBUTOR` / `FIRST_TIME_CONTRIBUTOR` / `NONE`
  的（丢掉 `OWNER` / `MEMBER` / `COLLABORATOR`——协作者在飞的 PR 不该被打扰）。
- **评论／标签／关闭**：`gh pr comment`、`gh pr edit --add-label`/`--remove-label`、`gh pr close`

GitHub 的 issue 与 PR 共用一个编号空间，裸的 `#42` 可能是任一种：先 `gh pr view 42`，
失败再回落到 `gh issue view 42`。

## 技能里说「publish to the issue tracker」时

创建一个 GitHub issue（带 `-R xltxb/edge_caddy`）。

## 技能里说「fetch the relevant ticket」时

`gh issue view <number> -R xltxb/edge_caddy --comments`

## Wayfinding operations

供 `/wayfinder` 使用。**map** 是一个 issue，**child** 是挂在它下面的工单。

- **Map**：一个打了 `wayfinder:map` 标签的 issue，正文含 Notes / Decisions-so-far / Fog。
  `gh issue create -R xltxb/edge_caddy --label wayfinder:map`。
- **Child ticket**：以 GitHub sub-issue 形式挂到 map（`gh api` 的 sub-issues 端点）。
  未启用 sub-issues 时，把 child 加进 map 正文的任务列表，并在 child 正文顶部写
  `Part of #<map>`。标签用 `wayfinder:<type>`（`research`/`prototype`/`grilling`/`task`）。
  认领后指派给驱动它的开发者。
- **Blocking**：用 GitHub 的**原生 issue dependencies**（UI 可见的规范表示）。加边：
  ```bash
  gh api --method POST repos/xltxb/edge_caddy/issues/<child>/dependencies/blocked_by \
    -F issue_id=<blocker-db-id>
  ```
  `<blocker-db-id>` 是阻塞者的**数据库 id**（`gh api repos/xltxb/edge_caddy/issues/<n> --jq .id`），
  **不是** `#number`，也不是 `node_id`。GitHub 会在 `issue_dependencies_summary.blocked_by`
  里报告仍未关闭的阻塞者数量（这是实时闸门）。不支持 dependencies 时，回落到在 child
  正文顶部写 `Blocked by: #<n>, #<n>`。所有阻塞者都关闭后该工单才算解除阻塞。
- **Frontier query**：列出 map 的未关闭 children，去掉仍有未关闭阻塞者的
  （`issue_dependencies_summary.blocked_by > 0`，或 `Blocked by` 行里还有未关闭的），
  再去掉已有 assignee 的；按 map 里的顺序取第一个。
- **Claim**：`gh issue edit <n> -R xltxb/edge_caddy --add-assignee @me`——本次会话的第一次写操作。
- **Resolve**：`gh issue comment <n> -R ... --body "<answer>"`，然后 `gh issue close <n> -R ...`，
  再把上下文指针（gist + 链接）追加到 map 的 Decisions-so-far。
