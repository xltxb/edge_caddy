-- 节点日志（#26）。**只存 Agent 自己的运行日志**，不存 Caddy 的 access log：
-- 一台扛流量的边缘节点 access log 是海量的，全量上报会把隧道和主控数据库压垮。
--
-- 人在控制台上问的是「这台机器上发生了什么」——配置应用、证书加载、校验端点
-- 报错，那些都是 Agent 自己写的。access log 属于另一个问题（流量分析）。
CREATE TABLE node_logs (
  id      BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  node_id TEXT NOT NULL REFERENCES edge_nodes(id) ON DELETE CASCADE,
  at      TIMESTAMPTZ NOT NULL,
  level   TEXT NOT NULL,              -- debug | info | warn | error（契约 §4）
  msg     TEXT NOT NULL
);

-- 查询永远是「某个节点最近的 N 条」，倒序。
CREATE INDEX idx_node_logs_node_id_desc ON node_logs (node_id, id DESC);
