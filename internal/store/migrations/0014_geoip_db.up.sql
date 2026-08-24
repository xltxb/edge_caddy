-- GeoIP 库。**存库不存盘**：主控可能被重装、换机器，而一份放在磁盘上的
-- 6MB 文件不会跟着数据库一起被备份走 —— 那时地域规则会在没人动过它的
-- 情况下失效，且没有任何一处说得出为什么。
--
-- 只存一行：id 恒为 1。用一张表而不是 settings 里的一个键，是因为
-- settings 是 JSONB，而往 JSONB 里塞几 MB 的二进制要先 base64，白涨三分之一。
CREATE TABLE geoip_db (
  id         SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
  mmdb       BYTEA        NOT NULL,
  sha256     TEXT         NOT NULL,
  source     TEXT         NOT NULL,  -- maxmind | manual
  updated_at TIMESTAMPTZ  NOT NULL DEFAULT now()
);
