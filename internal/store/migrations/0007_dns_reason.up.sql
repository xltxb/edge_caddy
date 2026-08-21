-- 「这台机器为什么不参与解析」——谁关的、什么时候关的。
--
-- 此前只有 dns_enabled 一个布尔，而关掉它有三条路径：人手动点、
-- 心跳超时后系统自动摘、人把节点下线。三者在数据里长得一模一样，
-- 于是界面只能说「未参与解析」，说不出为什么——而三者的处置完全不同：
-- 自己关的想开就开，系统摘的要先去修那台机器，下线的要先「重新上线」。
CREATE TYPE dns_change_reason AS ENUM ('manual', 'auto_offline', 'drained');

ALTER TABLE edge_nodes
  ADD COLUMN dns_reason     dns_change_reason,
  ADD COLUMN dns_actor      TEXT,          -- 操作人；系统自动摘除时为空
  ADD COLUMN dns_changed_at TIMESTAMPTZ;
