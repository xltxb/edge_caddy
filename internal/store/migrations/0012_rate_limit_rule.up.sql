-- 限流规则类型。理由与 0011 相同：ALTER TYPE 不能在事务块里跑，而驱动不包事务 —— 见 0011。
ALTER TYPE rule_type ADD VALUE IF NOT EXISTS 'rate_limit';
