-- phase 不能从「已回报的节点数 > 0」推出来。
--
-- 前端的落定条件是「全部终态且无重试中」——6/6 并不等于结束了，因为重试中的
-- 节点还会再动。要判断这个，必须知道本次下发**应当**有多少个节点回报，
-- 而那个数字只有下发的那一刻知道。
ALTER TABLE deploys ADD COLUMN target_count INT NOT NULL DEFAULT 0;
