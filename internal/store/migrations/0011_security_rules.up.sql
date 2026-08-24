-- 两种新的访问规则类型：IP 黑名单、请求特征拦截。
--
-- ALTER TYPE ... ADD VALUE 不能在事务块里跑，golang-migrate 默认每个文件
-- 一个事务 —— 所以这个文件里加了 no-transaction 指令。
-- 不加的话报的是「ALTER TYPE ... cannot run inside a transaction block」，
-- 而那句话不会让人想到迁移框架。
ALTER TYPE rule_type ADD VALUE IF NOT EXISTS 'ip_blacklist';
ALTER TYPE rule_type ADD VALUE IF NOT EXISTS 'request_filter';
