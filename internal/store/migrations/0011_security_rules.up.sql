-- 两种新的访问规则类型：IP 黑名单、请求特征拦截。
--
-- ALTER TYPE ... ADD VALUE 在 PostgreSQL 里不能跑在事务块内。
--
-- 这里能跑通，是因为 golang-migrate 的 postgres 驱动**本来就不包事务**
-- （它直接 Exec 整个文件），**不是因为这个文件做了什么**。
--
-- 这条注释此前写着「所以这个文件里加了 no-transaction 指令」——
-- **而这个文件里没有任何指令**。一句关于所在文件的断言，而它是假的：
-- 读的人会去找那条指令，找不到，然后怀疑自己看漏了。
--
-- 哪天换了迁移驱动、或者驱动改成包事务，这几个文件会报
-- 「cannot run inside a transaction block」—— 那时才需要真的加点什么。
ALTER TYPE rule_type ADD VALUE IF NOT EXISTS 'ip_blacklist';
ALTER TYPE rule_type ADD VALUE IF NOT EXISTS 'request_filter';
