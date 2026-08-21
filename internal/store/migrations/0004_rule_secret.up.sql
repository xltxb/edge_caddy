-- 服务密钥规则的共享密钥不能放进 spec：spec 会被 GET /rules 原样返回，
-- 而凭证只写入不回显（PRD §7）。单独一列，AES-GCM 加密。
ALTER TABLE access_rules ADD COLUMN secret_sealed BYTEA;
