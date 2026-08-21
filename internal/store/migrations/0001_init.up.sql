-- Edge Controller 初始 schema。
-- 主体来自后端开发文档 §3，偏离处在各表上方注明。

CREATE TYPE node_status  AS ENUM ('ok','warn','down');
CREATE TYPE block_mode   AS ENUM ('abort','403','404');
CREATE TYPE rule_type    AS ENUM ('ip_whitelist','service_secret','jwt_bearer');
CREATE TYPE deploy_state AS ENUM ('ok','fail');
CREATE TYPE op_result    AS ENUM ('ok','fail','partial');
-- 四档，不是三档：ok = 成功完成的动作，info = 流水账。
-- 合并会让下发成功和背景噪音同色（api-contract §2）。
CREATE TYPE event_kind   AS ENUM ('ok','info','warn','crit');

-- ---------- 身份 ----------
-- 文档 §3 没有这两张表，但 §4 的登录换 Cookie 需要它们。

CREATE TABLE users (
  username   TEXT PRIMARY KEY,
  pw_hash    TEXT NOT NULL,                    -- bcrypt
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 会话落库而不是签名 Cookie：单管理员系统里「登出所有设备」和
-- 「凭证泄露后立刻失效」比无状态更值钱。
CREATE TABLE sessions (
  id         TEXT PRIMARY KEY,                 -- 随机 32 字节 hex
  username   TEXT NOT NULL REFERENCES users(username) ON DELETE CASCADE,
  src_ip     INET,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX idx_sessions_expiry ON sessions (expires_at);

-- ---------- 拓扑 ----------

CREATE TABLE edge_nodes (
  id            TEXT PRIMARY KEY,                 -- node-hk-01
  city          TEXT NOT NULL,
  vendor        TEXT NOT NULL,                    -- DMIT PPro
  line          TEXT NOT NULL,                    -- CN2 GIA · CMIN2
  public_ip     INET NOT NULL,
  status        node_status NOT NULL DEFAULT 'ok',
  cfg_version   TEXT NOT NULL DEFAULT '',
  dns_enabled   BOOLEAN NOT NULL DEFAULT TRUE,
  last_hb_at    TIMESTAMPTZ,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 一次性接入 Token。文档 §4 描述了行为（30 分钟 TTL、单次使用）但没给表。
-- 签发时就绑定这台机器的身份，所以 Token 不能张冠李戴。
CREATE TABLE enroll_tokens (
  token_hash TEXT PRIMARY KEY,                 -- 只存哈希，明文仅在签发响应里出现一次
  node_id    TEXT NOT NULL,
  city       TEXT NOT NULL,
  vendor     TEXT NOT NULL,
  line       TEXT NOT NULL,
  public_ip  INET NOT NULL,
  expires_at TIMESTAMPTZ NOT NULL,
  used_at    TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ---------- 配置 ----------

CREATE TABLE proxy_routes (
  domain     TEXT PRIMARY KEY,
  upstream   TEXT NOT NULL,                    -- host:port
  block_mode block_mode NOT NULL DEFAULT 'abort',
  mtls       BOOLEAN NOT NULL DEFAULT FALSE,   -- 回源 mTLS：边缘向源站出示客户端证书（ADR-0008）
  compress   BOOLEAN NOT NULL DEFAULT TRUE,
  body_max   TEXT NOT NULL DEFAULT '5MB',      -- 人类可读；渲染器转成 int64 字节数
  whitelist  JSONB NOT NULL DEFAULT '[]',      -- CIDR 校验在应用层
  version    INT NOT NULL DEFAULT 0            -- 0 = 尚未下发到节点
);

CREATE TABLE access_rules (
  id       TEXT PRIMARY KEY,                   -- office-wl
  name     TEXT NOT NULL,
  type     rule_type NOT NULL,
  enabled  BOOLEAN NOT NULL DEFAULT TRUE,
  spec     JSONB NOT NULL,                     -- 形状随 type 变，见 api-contract §6.2
  apply_to JSONB NOT NULL DEFAULT '[]',        -- 域名数组；空 = 未绑定 = 不生效
  version  INT NOT NULL DEFAULT 0
);

CREATE TABLE global_policies (                 -- tls / log 两行
  id      TEXT PRIMARY KEY,
  name    TEXT NOT NULL,
  spec    JSONB NOT NULL,
  version INT NOT NULL DEFAULT 0
);

CREATE TABLE config_drafts (                   -- 未下发的改动：叠加在基线上的 Partial
  res_key    TEXT PRIMARY KEY,                 -- route:api.example.com / rule:office-wl / global:tls
  patch      JSONB NOT NULL,
  updated_by TEXT,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ---------- 下发 ----------

CREATE TABLE deploys (
  id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  cfg_version TEXT UNIQUE NOT NULL,
  operator    TEXT NOT NULL,                   -- abiu / ops-bot
  res_keys    JSONB NOT NULL,
  snapshot    JSONB NOT NULL,                  -- 本次全量渲染快照，回滚依据
  ok_count    INT NOT NULL DEFAULT 0,
  fail_count  INT NOT NULL DEFAULT 0,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_deploys_created ON deploys (created_at DESC);

CREATE TABLE deploy_results (
  deploy_id BIGINT NOT NULL REFERENCES deploys(id) ON DELETE CASCADE,
  node_id   TEXT NOT NULL,
  state     deploy_state NOT NULL,
  detail    TEXT NOT NULL DEFAULT '',          -- "31ms" / "deadline exceeded"
  -- 只重试传输层失败（ADR-0005）。这一列记的是「还会不会再动」，
  -- 前端据此决定显示「重试中」还是终态红字。
  retrying  BOOLEAN NOT NULL DEFAULT FALSE,
  PRIMARY KEY (deploy_id, node_id)
);

-- 当前基线。单行表：约束保证它只可能有一行。
CREATE TABLE baseline (
  only_row    BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (only_row),
  cfg_version TEXT NOT NULL,
  deploy_id   BIGINT REFERENCES deploys(id)
);

-- ---------- DNS ----------

CREATE TABLE dns_weights (
  line_code TEXT NOT NULL CHECK (line_code IN ('ct','cu','cm','tw','ov')),
  node_id   TEXT NOT NULL REFERENCES edge_nodes(id) ON DELETE CASCADE,
  weight    INT NOT NULL CHECK (weight >= 0),
  PRIMARY KEY (line_code, node_id)
);

-- ---------- 证书 ----------
-- 文档 §3 说「证书状态不建表，从节点上报的清单聚合」。那条已被推翻：
-- 主控是签发方，必须持有 PEM 才能内联下发（ADR-0010）。

CREATE TABLE certs (
  domain     TEXT PRIMARY KEY,
  issuer     TEXT NOT NULL,                    -- Let's Encrypt / ZeroSSL
  challenge  TEXT NOT NULL DEFAULT 'dns-01' CHECK (challenge = 'dns-01'),
  auto_renew BOOLEAN NOT NULL DEFAULT TRUE,
  cert_pem   BYTEA NOT NULL,
  key_pem    BYTEA NOT NULL,                   -- AES-GCM 加密
  not_after  TIMESTAMPTZ NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 节点回执：主控下发的证书，节点上到底加载了没有。
-- loaded < expected 意味着「下发到了但没生效」——这类故障在节点自管模型里看不见。
CREATE TABLE cert_nodes (
  domain      TEXT NOT NULL REFERENCES certs(domain) ON DELETE CASCADE,
  node_id     TEXT NOT NULL REFERENCES edge_nodes(id) ON DELETE CASCADE,
  not_after   TIMESTAMPTZ NOT NULL,            -- 节点上那张的到期时间，可能落后于主控
  fingerprint TEXT NOT NULL,
  reported_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (domain, node_id)
);

-- 两套相互独立的 CA，根私钥只在主控（ADR-0009）。
CREATE TABLE pki_cas (
  kind       TEXT PRIMARY KEY CHECK (kind IN ('upstream','tunnel')),
  cert_pem   BYTEA NOT NULL,
  key_pem    BYTEA NOT NULL,                   -- AES-GCM 加密
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ---------- 观测 ----------

CREATE TABLE events (
  id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  node_id    TEXT,                             -- NULL = 系统级事件
  kind       event_kind NOT NULL,
  msg        TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_events_created ON events (created_at DESC);

-- 全系统唯一落库的时序数据，只为总览的「较昨日同时段」同比。
-- 每分钟一行全局聚合（不分节点），保留 7 天 ≈ 10080 行。
-- 节点级的 CPU sparkline 仍然只在主控进程内存里——那个不需要跨天。
CREATE TABLE traffic_samples (
  at           TIMESTAMPTZ PRIMARY KEY,
  conns_total  BIGINT NOT NULL,
  req_total    BIGINT NOT NULL,
  origin_total BIGINT NOT NULL
);

CREATE TABLE audit_logs (
  id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  operator   TEXT NOT NULL,
  action     TEXT NOT NULL,                    -- 取值表见 docs/api-contract.md §5
  target     TEXT NOT NULL DEFAULT '',
  src_ip     INET,
  result     op_result NOT NULL,
  detail     TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_audit_op ON audit_logs (operator, created_at DESC);
CREATE INDEX idx_audit_created ON audit_logs (created_at DESC);

-- ---------- 设置 ----------
-- 含 alert 渠道配置；凭证字段 AES-GCM 加密后再放进 JSONB。
CREATE TABLE settings (
  k TEXT PRIMARY KEY,
  v JSONB NOT NULL
);
