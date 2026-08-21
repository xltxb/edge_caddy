# 改用 PostgreSQL 16，取代 ADR-0006 的「只用 SQLite」

- 状态：已接受，取代 [ADR-0006](0006-sqlite-only.md)
- 日期：2026-08-21

ADR-0006 选 SQLite 的核心论据不是「SQLite 更好」，而是**「另一条路从未被验证」**：
当时开发机上没有数据库服务、没有 Docker、没有任何容器运行时，生产方言的 SQL 一行也
跑不起来，结果是「本地全绿、线上未知」。

这个论据现在失效了——开发机上已装有 `postgresql@16`，本机跑的就是线上跑的。
论据没了，结论跟着走：改用 PostgreSQL 16（pgx/v5 + sqlx + golang-migrate），
与后端开发文档 §1/§3 一致。

## Consequences

- 后端文档 §3 的 DDL 可以原样落地，不必再做 ADR-0006 里列的那套降级改写
  （`ENUM` → `TEXT` + CHECK、`JSONB` → `TEXT`、`TIMESTAMPTZ` → RFC3339 字符串）。
  原生 ENUM / JSONB / `GENERATED ALWAYS AS IDENTITY` 直接可用。
- ADR-0006 关于 WAL 与 `busy_timeout` 的那条注意事项作废；换成 pgxpool（max 10）。
- 仓储层单测需要一个真库。**不要为此引入内存假实现**——那等于把 ADR-0006 想避免的
  「未验证路径」以另一种形式请回来。测试直连本机 PG，每个测试用独立 schema 或事务回滚隔离。
  代价是单测不再零依赖，CI 上要起一个 PG。
- ADR-0006 里「放弃多 Master HA」那条**不再由存储层决定**。但 HA 仍不是目标：
  Agent 只连一个主控，主控宕机不中断流量，PRD 也没有这个要求。真要做时另开 ADR。
