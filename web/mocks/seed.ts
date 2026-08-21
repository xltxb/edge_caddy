/**
 * Mock fixture —— 站在主控的位置说话，所以一律是**线格式**（snake_case，
 * 与 `docs/api-contract.md` 逐字对应）。移植自高保真设计稿 `Edge Console.dc.html`
 * 的 seed()，按契约与已定的 ADR 做了对齐。
 *
 * 这份数据是被刻意设计过的，不要「顺手修干净」：
 *   - node-kr-01 CPU 81% 且 status=warn；node-tw-01 心跳 14s；node-us-01 离线
 *   - node-tw-01 与 node-us-01 的 cfg_version 落后基线 → 配置漂移 KPI 亮 2
 *   - node-tw-01 的 routes/rules 是**旧配置**里的数字（2/3），漂移节点就该显示旧值
 *   - static / ws 两张证书回执少于账面 → 证书页「N / M」告警态
 *   - 审计里 2 次失败登录 → 审计页顶部提示
 *   - drafts 预置两条 → 顶栏「待下发」非零、工作台资源树带蓝点
 *   - deploy 81 有 2 个失败节点，一个 retrying 一个不是 → 覆盖 ADR-0005 两条分支
 * 每一条都对应界面上一个需要被看见的状态。抹平了就再也测不到那条分支。
 *
 * 与设计稿的有意偏离：
 *   1. 术语统一「下发」（CONTEXT.md 把「推送 / 发布 / 部署」列为 _Avoid_）
 *   2. challenge 一律 dns-01（ADR-0001：主控集中签发，节点做不了 HTTP-01）
 *   3. 证书节点数拆成 expected / loaded / missing 三个字段（契约 §9）
 */

import type {
  AuditResult,
  CertWire,
  DeployDetailWire,
  DeployProgressState,
  DeployResultWire,
  EventKind,
  EventWire,
  LogLevel,
  NodeStatus,
  NodeWire,
  PolicyWire,
  RouteWire,
  RuleWire,
} from '../src/api/types'

export const BASELINE = 'cfg-2f9a1c'
const PREV = 'cfg-8b03e7'

/** fixture 的时间基准。模块加载时定一次，之后所有时间都相对它算。 */
const T0 = Date.now()
const ago = (sec: number) => new Date(T0 - sec * 1000).toISOString()

export const NODE_IDS = [
  'node-hk-01',
  'node-jp-01',
  'node-kr-01',
  'node-tw-01',
  'node-de-01',
  'node-us-01',
] as const

const node = (
  id: string,
  city: string,
  vendor: string,
  line: string,
  public_ip: string,
  status: NodeStatus,
  cpu: number,
  mem: number,
  conns: number,
  hbSec: number,
  cfg_version: string,
  routes: number,
  rules: number,
  dns_enabled: boolean,
  cpu_series: number[],
): NodeWire => ({
  id,
  city,
  vendor,
  line,
  public_ip,
  status,
  cpu,
  mem,
  conns,
  cpu_series,
  last_hb_at: ago(hbSec),
  hb_age_ms: Math.round(hbSec * 1000),
  cfg_version,
  drift: cfg_version !== BASELINE,
  dns_enabled,
  routes,
  rules,
  created_at: ago(86_400 * 20),
})

export const nodes: NodeWire[] = [
  node('node-hk-01', '香港', 'DMIT PPro', 'CN2 GIA · CMIN2', '103.117.44.18', 'ok', 15.2, 32.8, 12400, 1.2, BASELINE, 4, 3, true, [22, 19, 24, 28, 21, 17, 20, 25, 23, 15, 18, 15]),
  node('node-jp-01', '东京', 'V.PS Tokyo', 'CMIN2 · SoftBank', '45.32.108.7', 'ok', 28.6, 41.3, 18900, 0.9, BASELINE, 4, 3, true, [30, 34, 28, 31, 38, 42, 36, 29, 33, 30, 26, 29]),
  node('node-kr-01', '首尔', 'Kdatacenter', 'KT · SK Direct', '158.247.220.94', 'warn', 81.4, 74.2, 31200, 2.1, BASELINE, 4, 3, true, [48, 55, 61, 58, 66, 72, 69, 75, 79, 83, 80, 81]),
  // 漂移节点：routes/rules 停在旧配置的数字上，这正是它有用的地方
  node('node-tw-01', '台北', 'MoonVM', 'HiNet 直连', '103.40.16.203', 'warn', 12.0, 22.5, 2100, 14, PREV, 2, 3, true, [18, 16, 20, 17, 19, 15, 18, 0, 0, 0, 12, 12]),
  node('node-de-01', '法兰克福', 'Hetzner CX42', '国际 BGP', '116.202.75.31', 'ok', 9.7, 28.1, 6700, 1.5, BASELINE, 4, 3, true, [12, 10, 14, 11, 9, 13, 10, 8, 11, 9, 10, 10]),
  node('node-us-01', '洛杉矶', 'Contabo', '国际 BGP', '194.238.19.62', 'down', 0, 0, 0, 372, PREV, 2, 3, false, [14, 12, 15, 13, 11, 14, 9, 0, 0, 0, 0, 0]),
]

let eventSeq = 4127
const ev = (sec: number, nodeId: string | null, kind: EventKind, msg: string): EventWire => ({
  id: eventSeq--,
  at: ago(sec),
  node: nodeId,
  kind,
  msg,
})

export const events: EventWire[] = [
  ev(5, 'node-us-01', 'crit', '心跳连续超时 3 次，已自动暂停 DNS 解析'),
  ev(17, 'node-kr-01', 'warn', 'CPU 持续高于 80%，建议扩容或分流'),
  ev(232, null, 'ok', `配置 ${BASELINE} 下发完成，4/6 节点热重载成功`),
  ev(233, 'node-de-01', 'ok', 'Caddy 热重载成功，耗时 42ms'),
  ev(233, 'node-jp-01', 'ok', 'Caddy 热重载成功，耗时 38ms'),
  ev(234, 'node-hk-01', 'ok', 'Caddy 热重载成功，耗时 31ms'),
  ev(370, null, 'info', '管理员 abiu 修改 api.example.com 白名单，新增 2 个 IP'),
]

export const kpi = () => ({
  nodes_online: nodes.filter((n) => n.status !== 'down').length,
  nodes_total: nodes.length,
  conns_total: nodes.reduce((s, n) => s + n.conns, 0),
  conns_delta_pct: 12.4,
  /** 回源率：越低越好。8.7% 到达源站，其余 91.3% 被边缘拦掉。 */
  origin_rate: 8.7,
  drift_nodes: nodes.filter((n) => n.drift).length,
})

/* ── 配置资源 ── */

export const routes: RouteWire[] = [
  { domain: 'api.example.com', upstream: '10.8.0.2:8080', block_mode: 'abort', mtls: false, compress: true, body_max: '5MB', whitelist: ['203.0.113.7', '198.51.100.24', '192.0.2.15'], version: 7 },
  { domain: 'cdn.example.com', upstream: '10.8.0.5:9000', block_mode: '403', mtls: false, compress: true, body_max: '64MB', whitelist: ['0.0.0.0/0'], version: 3 },
  { domain: 'admin.example.com', upstream: '127.0.0.1:7788', block_mode: 'abort', mtls: true, compress: false, body_max: '256MB', whitelist: ['203.0.113.7', '198.51.100.24'], version: 12 },
  { domain: 'push.example.com', upstream: '10.8.0.9:8443', block_mode: 'abort', mtls: true, compress: false, body_max: '1MB', whitelist: ['10.8.0.0/24'], version: 2 },
]

export const rules: RuleWire[] = [
  { id: 'office-wl', name: '办公出口白名单', type: 'ip_whitelist', enabled: true, version: 4, spec: { ips: ['203.0.113.7', '198.51.100.24', '192.0.2.15', '203.0.113.88', '198.51.100.161', '10.8.0.0/24'] }, apply_to: ['api.example.com', 'admin.example.com'] },
  { id: 'partner-secret', name: '合作方服务密钥', type: 'service_secret', enabled: true, version: 2, spec: { header: 'X-Service-Secret', algo: 'hmac-sha256', ttl_s: 300, replay_protection: true }, apply_to: ['api.example.com'] },
  { id: 'app-jwt', name: 'App 客户端 JWT', type: 'jwt_bearer', enabled: true, version: 6, spec: { iss: 'https://auth.example.com/', aud: 'edge-api', jwks_url: 'https://auth.example.com/.well-known/jwks.json', skew_s: 60 }, apply_to: ['api.example.com', 'push.example.com'] },
]

export const policies: PolicyWire[] = [
  { id: 'tls', name: 'TLS / 证书策略', version: 3, spec: { ca: 'letsencrypt', email: 'ops@example.com', key_type: 'p256', min_version: '1.2', hsts: true, hsts_max_age: 63072000, http3: true, ocsp: false } },
  // rate_limit 关掉时 rate_rps / rate_burst 是**条件字段**，spec 里可以不存在
  { id: 'log', name: '日志与限流', version: 5, spec: { format: 'json', level: 'INFO', roll_size: 50, roll_keep: 5, strip_headers: true, rate_limit: true, rate_rps: 200, rate_burst: 400 } },
]

/** 预置草稿：顶栏「待下发」非零，工作台资源树带蓝点。 */
export const draftItems: Record<string, Record<string, unknown>> = {
  'route:api.example.com': {
    whitelist: ['203.0.113.7', '198.51.100.24', '192.0.2.15', '203.0.113.88', '198.51.100.161'],
    body_max: '10MB',
  },
  'route:cdn.example.com': { upstream: '10.8.0.7:9000' },
}

export const draftUpdated: Record<string, { by: string; at: string }> = {
  'route:api.example.com': { by: 'abiu', at: ago(370) },
  'route:cdn.example.com': { by: 'ops-bot', at: ago(900) },
}

/* ── 证书（契约 §9）── */

const ALL = [...NODE_IDS]

const cert = (
  domain: string,
  scope: string,
  issuer: string,
  key_type: string,
  days_left: number,
  auto_renew: boolean,
  challenge: string,
  expected: string[],
  loaded: string[],
): CertWire => ({
  domain,
  scope,
  issuer,
  key_type,
  days_left,
  auto_renew,
  challenge,
  expected_nodes: expected.length,
  loaded_nodes: loaded.length,
  missing_nodes: expected.filter((n) => !loaded.includes(n)),
})

/** static / ws 刻意缺回执 —— 「下发到了但没生效」的可见证据。 */
const STATIC_LOADED = ['node-hk-01', 'node-jp-01', 'node-kr-01', 'node-de-01']
const WS_LOADED = ['node-hk-01', 'node-jp-01', 'node-de-01']

export const certs: CertWire[] = [
  cert('api.example.com', '单域名', "Let's Encrypt", 'ECDSA P-256', 47, true, 'dns-01', ALL, ALL),
  cert('cdn.example.com', '单域名', "Let's Encrypt", 'ECDSA P-256', 61, true, 'dns-01', ALL, ALL),
  cert('*.example.com', '通配符', "Let's Encrypt", 'ECDSA P-256', 12, true, 'dns-01', ALL, ALL),
  cert('admin.example.com', '单域名', 'ZeroSSL', 'RSA 2048', 4, false, 'dns-01', ['node-hk-01'], ['node-hk-01']),
  cert('push.example.com', '单域名', "Let's Encrypt", 'ECDSA P-256', 73, true, 'dns-01', ALL, ALL),
  cert('edge-mtls (内部 CA)', '客户端', 'Edge Internal CA', 'ECDSA P-256', 203, true, '内部签发', ALL, ALL),
  cert('master.example.com', '单域名', "Let's Encrypt", 'ECDSA P-256', 38, true, 'dns-01', [], []),
  cert('static.example.com', '单域名', "Let's Encrypt", 'ECDSA P-256', 19, true, 'dns-01', ALL, STATIC_LOADED),
  cert('ws.example.com', '单域名', "Let's Encrypt", 'ECDSA P-256', 55, true, 'dns-01', ALL, WS_LOADED),
]

/* ── DNS 调度 ── */

export interface DnsLineWire {
  line_code: string
  line_name: string
  detail: string
  weights: Record<string, number>
}

export const dnsLines: DnsLineWire[] = [
  { line_code: 'ct', line_name: '电信', detail: 'CN2 GIA · AS4809', weights: { 'node-hk-01': 60, 'node-jp-01': 40 } },
  { line_code: 'cu', line_name: '联通', detail: 'AS9929 CUVIP', weights: { 'node-jp-01': 50, 'node-hk-01': 30, 'node-kr-01': 20 } },
  { line_code: 'cm', line_name: '移动', detail: 'CMIN2 · AS58807', weights: { 'node-hk-01': 50, 'node-jp-01': 50 } },
  { line_code: 'tw', line_name: '台湾', detail: 'HiNet 直连', weights: { 'node-tw-01': 100 } },
  { line_code: 'ov', line_name: '境外 / 默认', detail: '国际 BGP', weights: { 'node-de-01': 60, 'node-us-01': 40 } },
]

/* ── 下发记录 ── */

const rows = (raw: [string, DeployProgressState, string, boolean][]): DeployResultWire[] =>
  raw.map(([node, state, detail, retrying]) => ({ node, state, detail, retrying }))

export const deploys: DeployDetailWire[] = [
  {
    id: 81,
    cfg_version: BASELINE,
    created_at: ago(232),
    operator: 'abiu',
    res_keys: ['route:api.example.com'],
    ok_count: 4,
    fail_count: 2,
    // 当前基线那一条不可回滚
    is_baseline: true,
    targets: [...NODE_IDS],
    target_count: 6,
    phase: 'done',
    // 两条失败刻意走 ADR-0005 的两条分支：一条超时（还会重试），一条被 Caddy 拒（终态）
    results: rows([
      ['node-hk-01', 'ok', '31ms', false],
      ['node-jp-01', 'ok', '38ms', false],
      ['node-kr-01', 'ok', '45ms', false],
      ['node-de-01', 'ok', '42ms', false],
      ['node-tw-01', 'fail', 'deadline exceeded', true],
      ['node-us-01', 'fail', '节点不可达', true],
    ]),
  },
  { id: 80, cfg_version: PREV, created_at: ago(5_856), is_baseline: false, phase: 'done', targets: [...NODE_IDS], target_count: 6, operator: 'abiu', res_keys: ['route:cdn.example.com', 'route:push.example.com'], ok_count: 6, fail_count: 0, results: rows([['node-hk-01', 'ok', '29ms', false], ['node-jp-01', 'ok', '44ms', false], ['node-kr-01', 'ok', '51ms', false], ['node-de-01', 'ok', '39ms', false], ['node-tw-01', 'ok', '47ms', false], ['node-us-01', 'ok', '36ms', false]]) },
  { id: 79, cfg_version: 'cfg-91d4f0', created_at: ago(45_720), is_baseline: false, phase: 'done', targets: [...NODE_IDS], target_count: 6, operator: 'ops-bot', res_keys: ['route:api.example.com'], ok_count: 6, fail_count: 0, results: rows([['node-hk-01', 'ok', '27ms', false], ['node-jp-01', 'ok', '33ms', false], ['node-kr-01', 'ok', '40ms', false], ['node-de-01', 'ok', '38ms', false], ['node-tw-01', 'ok', '35ms', false], ['node-us-01', 'ok', '34ms', false]]) },
  { id: 78, cfg_version: 'cfg-77ac31', created_at: ago(73_320), is_baseline: false, phase: 'done', targets: [...NODE_IDS], target_count: 6, operator: 'abiu', res_keys: ['route:admin.example.com'], ok_count: 5, fail_count: 1, results: rows([['node-hk-01', 'ok', '30ms', false], ['node-jp-01', 'ok', '41ms', false], ['node-kr-01', 'ok', '48ms', false], ['node-de-01', 'ok', '43ms', false], ['node-tw-01', 'ok', '52ms', false], ['node-us-01', 'fail', '节点不可达', false]]) },
]

/* ── 审计（action 取值照契约 §5 的术语表）── */

export interface AuditWire {
  id: number
  at: string
  operator: string
  action: string
  target: string
  src_ip: string | null
  result: AuditResult
}

let auditSeq = 1837
const au = (
  sec: number,
  operator: string,
  action: string,
  target: string,
  src_ip: string | null,
  result: AuditResult,
): AuditWire => ({ id: auditSeq--, at: ago(sec), operator, action, target, src_ip, result })

export const audit: AuditWire[] = [
  au(5, 'system', '暂停解析', 'node-us-01', null, 'ok'),
  au(232, 'abiu', '下发配置', BASELINE, '203.0.113.7', 'partial'),
  au(370, 'abiu', '修改路由', 'api.example.com', '203.0.113.7', 'ok'),
  au(1_766, 'abiu', '登录控制台', '—', '203.0.113.7', 'ok'),
  au(2_649, 'ops-bot', '续期证书', 'cdn.example.com', '127.0.0.1', 'ok'),
  au(5_856, 'abiu', '下发配置', PREV, '203.0.113.7', 'ok'),
  au(7_181, 'zhang', '登录控制台', '—', '198.51.100.24', 'fail'),
  au(7_198, 'zhang', '登录控制台', '—', '198.51.100.24', 'fail'),
  au(45_720, 'ops-bot', '下发配置', 'cfg-91d4f0', '127.0.0.1', 'ok'),
  au(73_320, 'abiu', '新建路由', 'admin.example.com', '203.0.113.7', 'ok'),
]

/* ── 系统设置与告警 ── */

export const settings = {
  api_endpoint: 'https://cdn.example.com:8001',
  grpc_listen: '0.0.0.0:9000',
  heartbeat_sec: 3,
  probe_fail_times: 3,
  auto_pause_dns: true,
  dns_provider: 'cloudflare',
  dns_email: 'ops@example.com',
  dns_cred_type: 'global_key',
}

export const alerts = {
  webhook: 'https://hooks.example.com/edge/alert',
  notify_level: 'warn',
  lark_enabled: true,
  lark_webhook: 'https://open.larksuite.com/open-apis/bot/v2/hook/7f3a-demo-token',
  lark_at_all: true,
}

/* ── Agent 日志 ── */

const lg = (sec: number, level: LogLevel, msg: string) => ({ at: ago(sec), level, msg })

export const agentLogs: Record<string, { at: string; level: LogLevel; msg: string }[]> = {
  'node-hk-01': [lg(3, 'info', 'heartbeat ok · cpu 15.2 mem 32.8'), lg(234, 'info', 'caddy /load 200 · 31ms'), lg(235, 'info', `config ${BASELINE} received`), lg(630, 'info', 'tls renew api.example.com ok'), lg(1_809, 'info', 'abort 8 req from 45.77.x.x')],
  'node-jp-01': [lg(4, 'info', 'heartbeat ok · cpu 28.6 mem 41.3'), lg(233, 'info', 'caddy /load 200 · 38ms'), lg(235, 'info', `config ${BASELINE} received`), lg(2_641, 'info', 'upstream 10.8.0.2:8080 rtt 41ms'), lg(3_710, 'info', 'abort 3 req from 172.16.x.x')],
  'node-kr-01': [lg(5, 'info', 'heartbeat ok · cpu 81.4 mem 74.2'), lg(17, 'warn', 'cpu > 80% for 120s'), lg(234, 'info', 'caddy /load 200 · 45ms'), lg(1_193, 'warn', 'conn pool near limit (31.2k)'), lg(2_232, 'info', 'abort 51 req from scanner range')],
  'node-tw-01': [lg(10, 'warn', 'heartbeat delayed 14s'), lg(24, 'warn', 'grpc tunnel reconnecting…'), lg(161, 'error', 'grpc tunnel closed by peer'), lg(232, 'error', 'config push FAILED · deadline exceeded'), lg(1_318, 'info', 'caddy /load 200 · 52ms')],
  'node-de-01': [lg(3, 'info', 'heartbeat ok · cpu 9.7 mem 28.1'), lg(233, 'info', 'caddy /load 200 · 42ms'), lg(235, 'info', `config ${BASELINE} received`), lg(730, 'info', 'tls renew cdn.example.com ok'), lg(5_860, 'info', 'abort 2 req from 8.210.x.x')],
  'node-us-01': [lg(5, 'warn', 'dns weight set to 0 (auto)'), lg(20, 'error', 'heartbeat timeout 3/3'), lg(172, 'warn', 'heartbeat timeout 2/3'), lg(324, 'warn', 'heartbeat timeout 1/3'), lg(360, 'info', 'last heartbeat ok')],
}
