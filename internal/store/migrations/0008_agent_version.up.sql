-- 节点上跑的 Agent 版本。接入时由 Hello 带上来。
--
-- 灰度部署时人最先要问的一件事就是「我推上去的那一版到底上没上」，
-- 而此前那个值只出现在主控自己的日志里（`节点接入 agent_version=…`），
-- 控制台上看不到。
ALTER TABLE edge_nodes ADD COLUMN agent_version TEXT NOT NULL DEFAULT '';
