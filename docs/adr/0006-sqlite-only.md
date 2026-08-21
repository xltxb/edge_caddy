# 只用 SQLite，不引入 MySQL

- 状态：**已被 [ADR-0011](0011-postgres-supersedes-sqlite.md) 取代**（开发机现已具备 PostgreSQL 16，本条的核心论据失效）

后端文档 §1/§3 选的是 MySQL 8 + sqlx + golang-migrate。改为只用 SQLite（WAL 模式），
开发与生产同一套 SQL、同一种方言。

主控是**单进程、单写入者**：6 个节点、几百条路由、一条审计流水。这个量级 SQLite 绰绰有余，
而 MySQL 带来的是一台要运行、备份、打补丁的服务器，在此处换不到对应的好处。

## Considered Options

- **SQLite 开发 + MySQL 生产**。上一版就是这么做的，结果是 MySQL 路径**从未被验证**——
  开发机装不了 MySQL，那条路上的 SQL 一行也没真正跑过。两套方言真正的代价不是多写一份 SQL，
  而是「本地全绿、线上未知」，且这种未知不会以失败的形式提前暴露。
- **只用 MySQL，本地装一个**。与文档一致，也为将来的多 Master 留了路。但当前开发机上没有
  MySQL、没有 Docker、没有任何容器运行时，连仓储层单测都跑不起来。

## Consequences

- 不存在「未验证的生产路径」：本机跑的就是线上跑的。
- 放弃了多台 Master 做 HA 的可能。这在当前架构里不是损失——Agent 只连一个主控，且主控宕机
  并不中断流量（节点照常转发），PRD 也没有 HA 目标。真要做 HA 时，这条 ADR 需要被推翻。
- DDL 要从文档的 MySQL 写法改写：`ENUM` → `TEXT` + CHECK 约束，`DATETIME(3)` → `TEXT`（RFC3339）
  或整数毫秒，`JSON` 列 → `TEXT` 存 JSON，`AUTO_INCREMENT` → `INTEGER PRIMARY KEY AUTOINCREMENT`。
- 必须显式开启 WAL 并设置 `busy_timeout`：默认的回滚日志模式下，读会阻塞写，
  而心跳写入与页面查询是并发的。
