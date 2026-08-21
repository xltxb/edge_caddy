# Domain Docs

工程技能在探索代码库时，应当如何消费本仓库的领域文档。

## 探索之前先读

- 仓库根目录的 **`CONTEXT.md`** —— 术语表
- **`docs/adr/`** —— 读与你要动的区域相关的 ADR
- **`docs/api-contract.md`** —— HTTP / WS 契约。**动接口之前必读**：它是前后端两个
  agent 之间的接缝，单方面改实现而不改它（或不通知对方）就是悄悄毁约。

本仓库是**单上下文**，没有 `CONTEXT-MAP.md`。master / agent / console 三块职责不同，
但共享同一套术语（基线、草稿、下发、接入、回源 mTLS、校验端点），拆成多上下文只会让
同一个词在三处各写一遍然后慢慢漂开。

这些文件缺失时**静默继续**：不要提示它们不存在，也不要建议预先创建。
`/domain-modeling` 技能会在术语或决定真正被确定下来时惰性创建它们。

## 文件结构

```
/
├── CLAUDE.md                  ← 权威优先级：ADR > api-contract > CONTEXT
├── CONTEXT.md                 ← 术语表
├── docs/
│   ├── adr/                   ← 架构决定（0001–）
│   ├── api-contract.md        ← HTTP / WS 契约，前后端接缝
│   └── agents/                ← 本目录：技能的配置
├── cmd/            master 与 agent 两个可执行体（见 issue #17）
├── internal/       主控与 Agent 的实现
├── proto/          gRPC 隧道
├── migrations/     PostgreSQL 迁移
└── web/            Vue 3 控制台（由 frontend agent 维护，后端不动）
```

`cmd/` `internal/` `proto/` `migrations/` 目前**尚未创建**——上一版实现已整体删除，
重写从 issue #17（后端骨架）开始。看不到它们是正常的，不是仓库损坏。

## 两个 agent 并行

同一仓库、目录级隔离：后端拿 `cmd/` `internal/` `proto/` `migrations/` `docs/` 与根级
`go.mod`，前端（agent 名 `frontend`）拿 `web/`。`CONTEXT.md`、`docs/adr/`、
`docs/api-contract.md` 两边共享——改这三样要通知对方，别指望它会主动读 diff。

ADR 编号也共享：开新 ADR 前先 `ls docs/adr/`，撞号时**后写的那个改**。

## 使用术语表里的措辞

输出里提到领域概念时（issue 标题、重构提案、假设、测试名），用 `CONTEXT.md` 里定义的
那个词，不要漂到术语表明确列在 `_Avoid_` 里的同义词。

这不只是文风问题：`audit_logs.action` 与事件流 `msg` 由后端产生、在前端页面上原样显示，
所以措辞是**契约的一部分**（取值表见 `docs/api-contract.md` §5）。

尤其注意几个已经被明确区分开的：

- **下发** 是把选中的草稿合入基线并广播的完整过程。全站统一用它，
  **不要**说推送 / 发布 / 部署——设计稿里三个词混用过，已经统一掉了。
- **回源 mTLS** 指边缘节点向源站出示客户端证书，**不是**「要求访问者出示证书」。
  单说「mTLS」或「双向认证」在这个项目里方向是歧义的（ADR-0008）。
- **可读表示** 指工作台右栏那份 JSON，它**不是**将要下发的字节。别叫它「预览 JSON」
  或「Caddy JSON 预览」——那两种叫法会让人以为它可下发（ADR-0007）。
- **配置漂移** 只比对版本号，回答「这次下发到没到」，**不检查节点上的配置内容**（ADR-0002）。
- **证书清单** 是 Agent 上报的**回执**，不是账本——主控自己持有全部签发记录（ADR-0010）。

如果你需要的概念还不在术语表里，那是个信号：要么你在发明这个项目不用的语言（重新考虑），
要么确实存在缺口（记下来交给 `/domain-modeling`）。

## 与 ADR 冲突时要挑明

如果你的输出与某条既有 ADR 相抵触，显式说出来，不要静默覆盖：

> _与 ADR-0004（主控不跑 caddy validate）相抵触——但值得重开，因为……_

**设计项目（claude.ai/design）里的三份文档冻结在 v1.0，已知有 9 处与 ADR 相抵触。**
与 ADR 冲突时**以 ADR 为准**，清单见 `CLAUDE.md` 的「权威在哪」与
`docs/api-contract.md` 末尾的差异对账表。
