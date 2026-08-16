-- Edge Controller 表结构（SQLite）
--
-- 由后端开发文档 §3 的 MySQL DDL 改写而来（docs/adr/0006）：
--   ENUM            → TEXT + CHECK 约束（约束要写出来，否则非法值会静默入库）
--   DATETIME(3)     → TEXT，存 RFC3339Nano（可排序、可读、时区明确）
--   JSON            → TEXT 存 JSON 串
--   AUTO_INCREMENT  → INTEGER PRIMARY KEY AUTOINCREMENT
--
-- 首切片只用到其中一部分表；未用到的先不建，避免出现「建了但没人写」的空表
-- 误导后来者以为某个功能已经落地。

CREATE TABLE IF NOT EXISTS edge_nodes (
  id          TEXT PRIMARY KEY,
  city        TEXT NOT NULL DEFAULT '',
  vendor      TEXT NOT NULL DEFAULT '',
  line        TEXT NOT NULL DEFAULT '',
  public_ip   TEXT NOT NULL DEFAULT '',
  status      TEXT NOT NULL DEFAULT 'down' CHECK (status IN ('ok','warn','down')),
  cfg_version TEXT NOT NULL DEFAULT '',
  dns_enabled INTEGER NOT NULL DEFAULT 1 CHECK (dns_enabled IN (0,1)),
  last_hb_at  TEXT,
  created_at  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS proxy_routes (
  domain     TEXT PRIMARY KEY,
  upstream   TEXT NOT NULL,
  block_mode TEXT NOT NULL DEFAULT 'abort' CHECK (block_mode IN ('abort','403','404')),
  mtls       INTEGER NOT NULL DEFAULT 0 CHECK (mtls IN (0,1)),
  compress   INTEGER NOT NULL DEFAULT 1 CHECK (compress IN (0,1)),
  body_max   TEXT NOT NULL DEFAULT '5MB',
  whitelist  TEXT NOT NULL DEFAULT '[]',
  version    INTEGER NOT NULL DEFAULT 0   -- 0 = 尚未下发到任何节点
);

-- 草稿全局可见：不按操作人分表也不按操作人过滤，任何人都能看到别人正在改什么。
-- updated_by 只用于在确认弹层里标出每条改动的作者，不参与可见性判断。
CREATE TABLE IF NOT EXISTS config_drafts (
  res_key    TEXT PRIMARY KEY,            -- route:api.example.com
  patch      TEXT NOT NULL,
  updated_by TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS deploys (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  cfg_version TEXT NOT NULL UNIQUE,
  operator    TEXT NOT NULL,
  res_keys    TEXT NOT NULL,              -- 本次勾选下发的资源键
  snapshot    TEXT NOT NULL,              -- 当次全量渲染快照，回滚以它为源
  ok_count    INTEGER NOT NULL DEFAULT 0,
  fail_count  INTEGER NOT NULL DEFAULT 0,
  created_at  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS deploy_results (
  deploy_id INTEGER NOT NULL REFERENCES deploys(id) ON DELETE CASCADE,
  node_id   TEXT NOT NULL,
  state     TEXT NOT NULL CHECK (state IN ('ok','fail')),
  detail    TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (deploy_id, node_id)
);

-- 键值设置。PKI 的根证书与私钥也存这里，值在写入前加密（见 internal/pki）。
CREATE TABLE IF NOT EXISTS settings (
  k TEXT PRIMARY KEY,
  v BLOB NOT NULL
);

-- 一次性接入 Token。存哈希而非明文：拿到库的人不该能直接拿它去接入。
-- consumed_by 为 NULL 表示尚未使用——「用过即失效」靠这一列的条件更新保证原子性，
-- 而不是靠先查后写（两台机器同时粘贴同一条安装命令时，先查后写会让两个都通过）。
CREATE TABLE IF NOT EXISTS enroll_tokens (
  token_hash  TEXT PRIMARY KEY,
  expires_at  TEXT NOT NULL,
  consumed_by TEXT,
  consumed_at TEXT
);

-- 审计流水。只记写操作（见 model.AuditLog）。
CREATE TABLE IF NOT EXISTS audit_logs (
  id       INTEGER PRIMARY KEY AUTOINCREMENT,
  operator TEXT NOT NULL DEFAULT '',
  action   TEXT NOT NULL,
  target   TEXT NOT NULL DEFAULT '',
  src_ip   TEXT NOT NULL DEFAULT '',
  result   TEXT NOT NULL CHECK (result IN ('ok','fail','partial')),
  detail   TEXT NOT NULL DEFAULT '',
  at       TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_audit_op ON audit_logs (operator, at DESC);

-- 访问规则。spec 里可能含共享密钥，因此该列**不回显**给接口调用方（见 api/rules.go）。
CREATE TABLE IF NOT EXISTS access_rules (
  id       TEXT PRIMARY KEY,
  name     TEXT NOT NULL DEFAULT '',
  type     TEXT NOT NULL CHECK (type IN ('ip_whitelist','service_secret','jwt_bearer')),
  enabled  INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
  spec     TEXT NOT NULL DEFAULT '{}',
  apply_to TEXT NOT NULL DEFAULT '[]',
  version  INTEGER NOT NULL DEFAULT 0
);

-- 服务端证书。主控集中签发（ADR-0001），经隧道下发到各节点。
--
-- cert_pem 是**明文**：证书是公开信息，加密只会让排查时看不到内容。
-- key_pem 是**密文**（AES-GCM，主密钥来自 EDGE_SECRET_KEY）：拿到私钥就能
-- 冒充这个域名，明文躺在库文件里，一次备份、一次误传就出去了。
CREATE TABLE IF NOT EXISTS certificates (
  domain    TEXT PRIMARY KEY,
  cert_pem  BLOB NOT NULL,
  key_pem   BLOB NOT NULL,
  not_after TEXT NOT NULL,
  issuer    TEXT NOT NULL DEFAULT '',
  key_type  TEXT NOT NULL DEFAULT '',
  serial    TEXT NOT NULL DEFAULT '',
  -- auto=1 表示由主控自动续期；手工导入的证书为 0
  auto      INTEGER NOT NULL DEFAULT 1 CHECK (auto IN (0,1)),
  issued_at TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_certs_expiry ON certificates (not_after);

-- DNS 解析权重。每条线路上每个节点一行。
--
-- 权重是**相对值**，不是百分比：下发前会在本条线路内归一化（internal/dnssched）。
-- 存百分比的话，加一台节点就得把其余全部改一遍，而那正是最容易改漏的操作。
CREATE TABLE IF NOT EXISTS dns_weights (
  domain  TEXT NOT NULL,
  node_id TEXT NOT NULL,
  line    TEXT NOT NULL,
  weight  INTEGER NOT NULL DEFAULT 0 CHECK (weight >= 0),
  PRIMARY KEY (domain, node_id, line)
);
