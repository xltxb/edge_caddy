-- 下线是人的意图，离线是主控的观察。两个正交的事实各记各的（ADR-0014）。
--
-- 不扩 node_status 枚举：status 已经有两个自动写入方（心跳写 ok/warn，
-- health 写 down），再加一个由人写的取值，下线一台健康节点之后
-- 下一个心跳就会把它冲回 ok。
--
-- 用时间戳而不是布尔：「什么时候下线的」是运维当场会问的第一个问题，
-- 而时间戳同时答得了「有没有」和「什么时候」，代价是零。
ALTER TABLE edge_nodes ADD COLUMN drained_at TIMESTAMPTZ;
