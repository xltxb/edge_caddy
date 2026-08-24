-- 节点报上来的「我认得哪些规则类型」。
--
-- **空数组表示旧 Agent**（它压根不报这个）—— 主控按「只认得
-- service_secret / jwt_bearer」处理。默认值给 '{}' 而不是那两个：
-- 一台从没报过的节点，我们对它的能力一无所知，而**假装知道比承认不知道更坏**。
ALTER TABLE edge_nodes ADD COLUMN IF NOT EXISTS verify_kinds TEXT[] NOT NULL DEFAULT '{}';
