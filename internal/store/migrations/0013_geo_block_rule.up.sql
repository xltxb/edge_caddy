-- 地域封禁规则类型。理由与 0011 相同：ALTER TYPE 不能在事务块里跑。
ALTER TYPE rule_type ADD VALUE IF NOT EXISTS 'geo_block';
