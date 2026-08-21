-- target_count 只说得出「少了几个」，说不出「少的是哪几个」。
--
-- 轮询降级是为断线与重连准备的：用户在下发进行中刷新页面时，前端手上
-- 没有 POST /deploys 那次响应里的目标列表，于是画不出「待下发」的那几行——
-- 而「还有谁没回来」正是降级时最需要看见的信息。
--
-- 目标列表落库后 target_count 就是 jsonb_array_length(targets)，
-- 两个字段记同一件事迟早会不一致，因此删掉后者。
ALTER TABLE deploys ADD COLUMN targets JSONB NOT NULL DEFAULT '[]';
ALTER TABLE deploys DROP COLUMN target_count;
