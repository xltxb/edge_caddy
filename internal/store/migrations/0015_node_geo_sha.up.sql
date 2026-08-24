-- 节点报上来的 GeoIP 库哈希。
--
-- **存它是为了让「哪些节点还没有库」看得见。** 不存的话，地域规则形同虚设
-- 这件事只出现在节点日志里 —— 而那要人主动去翻。
ALTER TABLE edge_nodes ADD COLUMN IF NOT EXISTS geo_db_sha TEXT NOT NULL DEFAULT '';
