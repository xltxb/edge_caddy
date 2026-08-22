DELETE FROM certs WHERE challenge = 'imported';
ALTER TABLE certs DROP CONSTRAINT IF EXISTS certs_challenge_check;
ALTER TABLE certs ADD CONSTRAINT certs_challenge_check CHECK (challenge = 'dns-01');
