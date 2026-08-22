-- 证书可以从外部证书平台导入，不只由主控自己签（ADR-0001 的补充）。
--
-- challenge 此前钉死在 'dns-01'，而**导入的证书不是通过任何 challenge
-- 拿到的**。放宽约束并加一个诚实的取值。
ALTER TABLE certs DROP CONSTRAINT IF EXISTS certs_challenge_check;
ALTER TABLE certs ADD CONSTRAINT certs_challenge_check
  CHECK (challenge IN ('dns-01', 'imported'));
