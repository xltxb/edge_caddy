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
  AlertsWire,
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
  SettingsWire,
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
  drained_at: string | null = null,
): NodeWire => ({
  id,
  city,
  vendor,
  line,
  public_ip,
  status,
  /*
   * mock 里让它跟着 status 走 —— 正常情况下两者本来就一致（与 caddyAdmin 的
   * 做法同源）。**要造两者不一致的场景，在用例里 override 它**：那是线上真出过
   * 的形状（status 停在 ok 而隧道早断了），不是默认值该长的样子。
   */
  online: status !== 'down',
  /*
   * 默认 0（这条隧道很稳）。**要看那个 UI 得 override**，见下面 node-jp-01——
   * 一台徽标全绿而隧道在反复断的机器，正是这个字段唯一的用途。
   */
  reconnects_1h: 0,
  cpu,
  mem,
  conns,
  cpu_series,
  last_hb_at: ago(hbSec),
  hb_age_ms: Math.round(hbSec * 1000),
  cfg_version,
  // 空串 = 还没接入过。给离线那台留空，让「还没接入过」这一支在界面上走得到。
  agent_version: status === 'down' ? '' : 'v0.2.0 (d3da612)',
  drift: cfg_version !== BASELINE,
  dns_enabled,
  // 下线是**意图**，与 status 各记各的（CONTEXT.md）。默认没人下过线。
  drained_at,
  /*
   * 解析是谁关的（契约 §4）。seed 里按各节点的处境给：
   * 关着且离线的记 auto_offline，关着且在线的记 manual（带操作人），
   * 开着的三样都空 —— **从没人动过与「manual 而操作人不详」是两回事**。
   */
  /*
   * **drained 优先**：被下线的机器解析也是关的，但原因是下线，不是有人手动关。
   * 第一版这里只看 dns_enabled 与 status，于是 node-de-01（有 drained_at）
   * 被推成了 manual —— DNS 页说「已暂停（abiu）」而节点页说「已下线（人为）」，
   * **两页各说各的**。夹具自己自相矛盾时，界面上那两句都是「对的」，
   * 而它们对不上账。
   */
  dns_reason: dns_enabled ? '' : drained_at ? 'drained' : status === 'down' ? 'auto_offline' : 'manual',
  dns_actor: dns_enabled || status === 'down' ? null : 'abiu',
  dns_changed_at: dns_enabled ? null : ago(1_800),
  routes,
  rules,
  created_at: ago(86_400 * 20),
})

export const nodes: NodeWire[] = [
  node('node-hk-01', '香港', 'DMIT PPro', 'CN2 GIA · CMIN2', '103.117.44.18', 'ok', 15.2, 32.8, 12400, 1.2, BASELINE, 4, 3, true, [22, 19, 24, 28, 21, 17, 20, 25, 23, 15, 18, 15]),
  /*
   * **徽标全绿而隧道在反复断。** status ok、在线、心跳新鲜、不漂移 —— 四个字段
   * 全是健康的，而它过去一小时断了 4 次。灰度上真发生过（CDN 每隔十几分钟切一次
   * 长连接），当时界面上看不出任何异常。这台是那个场景的固定夹具。
   */
  { ...node('node-jp-01', '东京', 'V.PS Tokyo', 'CMIN2 · SoftBank', '45.32.108.7', 'ok', 28.6, 41.3, 18900, 0.9, BASELINE, 4, 3, true, [30, 34, 28, 31, 38, 42, 36, 29, 33, 30, 26, 29]), reconnects_1h: 4 },
  node('node-kr-01', '首尔', 'Kdatacenter', 'KT · SK Direct', '158.247.220.94', 'warn', 81.4, 74.2, 31200, 2.1, BASELINE, 4, 3, true, [48, 55, 61, 58, 66, 72, 69, 75, 79, 83, 80, 81]),
  // 漂移节点：routes/rules 停在旧配置的数字上，这正是它有用的地方
  node('node-tw-01', '台北', 'MoonVM', 'HiNet 直连', '103.40.16.203', 'warn', 12.0, 22.5, 2100, 14, PREV, 2, 3, true, [18, 16, 20, 17, 19, 15, 18, 0, 0, 0, 12, 12]),
  node('node-de-01', '法兰克福', 'Hetzner CX42', '国际 BGP', '116.202.75.31', 'ok', 9.7, 28.1, 6700, 1.5, BASELINE, 4, 3, false, [12, 10, 14, 11, 9, 13, 10, 8, 11, 9, 10, 10], '2026-08-21T09:40:00+08:00'),
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

/**
 * 主控自己的构建版本（契约 §3）。**mock 此前漏了它** —— `check:shapes` 接进
 * 检查链、主控重启之后才报出来。
 *
 * 未打标的构建是 `"dev"` 而不是空串：空白读起来像「这个字段还没做」，
 * 而不是「这是个未打标的构建」。
 */
export const MASTER_VERSION = 'cfg-dev (mock)'

export const kpi = () => ({
  // 三档由这一处同时算出来，保证 online + warn + down == total。
  // 真后端也是一条语句产出的（契约 §3），前端不再自行推导。
  nodes_online: nodes.filter((n) => n.status === 'ok').length,
  nodes_warn: nodes.filter((n) => n.status === 'warn').length,
  nodes_down: nodes.filter((n) => n.status === 'down').length,
  nodes_total: nodes.length,
  conns_total: nodes.reduce((s, n) => s + n.conns, 0),
  conns_delta_reason: null,
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
  { id: 'partner-secret', name: '合作方服务密钥', type: 'service_secret', enabled: true, version: 2, spec: { header: 'X-Service-Secret', algo: 'hmac-sha256', ttl_s: 300, replay_protection: true, secret_configured: true }, apply_to: ['api.example.com'] },
  // 故意留一条**未绑定域名**的规则：它是半成品状态（契约 §6.2），
  // 而一个夹具表达不了的状态，等于在开发期不存在 —— 界面上那条「未绑定域名」
  // 分支、以及删除弹层里「本来就不生效」那一支，都靠它才走得到。
  { id: 'staging-wl', name: '预发环境白名单', type: 'ip_whitelist', enabled: true, version: 1, spec: { ips: ['198.51.100.200'] }, apply_to: [] },
  { id: 'app-jwt', name: 'App 客户端 JWT', type: 'jwt_bearer', enabled: true, version: 6, spec: { iss: 'https://auth.example.com/', aud: 'edge-api', jwks_url: 'https://auth.example.com/.well-known/jwks.json', skew_s: 60 }, apply_to: ['api.example.com', 'push.example.com'] },
]

export const policies: PolicyWire[] = [
  { id: 'tls', name: 'TLS / 证书策略', version: 3, spec: { min_version: '1.2', hsts: true, hsts_max_age: 63072000, http3: true, ocsp: false } },
  // rate_limit 关掉时 rate_rps / rate_burst 是**条件字段**，spec 里可以不存在。
  // 这里跟真主控一样是 false —— 官方 Caddy 没有限流模块，true 是个下发一定会被拒的
  // 状态。让 mock 里躺着一份「能成功下发的 true」，等于把真实的失败藏起来。
  { id: 'log', name: '日志与限流', version: 5, spec: { format: 'json', level: 'INFO', roll_size: 50, roll_keep: 5, strip_headers: true, rate_limit: false } },
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
  issuer: string,
  days_left: number,
  challenge: string,
  expected: string[],
  loaded: string[],
  /** 证书实际覆盖的域名。默认就是它自己 —— 多域名 / 通配符的那几张显式传。 */
  domains: string[] = [domain],
  /**
   * 它**正在服务**的路由域名 —— 与 domains 是两件事（契约 §9）。
   * 默认「服务着它自己」；`[]`（没人用）和 `null`（算不出来）要显式传，
   * **三种值界面上的处置各不相同**。
   */
  covers: string[] | null = [domain],
): CertWire => ({
  domain,
  issuer,
  domains,
  // 从 days_left 倒推，两者必须自洽 —— 夹具里说「12 天」而日期在三个月后，
  // 界面上那两个数会互相打脸，而它们本来是同一个事实的两种写法
  covers,
  not_after: new Date(T0 + days_left * 86_400_000).toISOString(),
  days_left,
  challenge,
  expected_nodes: expected.length,
  loaded_nodes: loaded.length,
  missing_nodes: expected.filter((n) => !loaded.includes(n)),
})

/** static / ws 刻意缺回执 —— 「下发到了但没生效」的可见证据。 */
const STATIC_LOADED = ['node-hk-01', 'node-jp-01', 'node-kr-01', 'node-de-01']
const WS_LOADED = ['node-hk-01', 'node-jp-01', 'node-de-01']

export const certs: CertWire[] = [
  /*
   * challenge 三种值都在，因为界面对它们的处置不同（ADR-0015、契约 §9）：
   *
   *   imported   外部平台推进来的 —— **新的常态**
   *   dns-01     主控自己签的     —— 历史数据，界面要标出「（历史）」
   *   内部签发    映射表里没有的   —— **原样显示，不假装认识它**
   *
   * 最后那个是 fallback 那一支的夹具。少了它，一个「映射不到就显示空」的实现
   * 也能让另外两条通过。
   *
   * days_left 覆盖两档：4 / 12 红（<14），19 黄（14–30），其余正常。
   */
  // 契约 §9 的例子：裸域 + 通配符 —— 「N 个域名（含通配符）」那一支的夹具
  cert('api.example.com', '外部证书平台 CA', 47, 'imported', ALL, ALL, [
    'api.example.com',
    '*.api.example.com',
  ]),
  cert('cdn.example.com', '外部证书平台 CA', 61, 'imported', ALL, ALL),
  cert('*.example.com', '外部证书平台 CA', 12, 'imported', ALL, ALL),
  cert('admin.example.com', 'ZeroSSL', 4, 'imported', ['node-hk-01'], ['node-hk-01']),
  cert('push.example.com', '外部证书平台 CA', 73, 'imported', ALL, ALL),
  cert('edge-mtls (内部 CA)', 'Edge Internal CA', 203, '内部签发', ALL, ALL),
  // ADR-0015 之前主控自己签的两张，留着让「（历史）」那一支在界面上走得到
  /*
   * **存着但没人用**（covers 为空）—— 它 expected_nodes 也是 0（仅主控）。
   * 这一张是「没有路由在用它」那一支的夹具：删它是安全的，而界面上这一列
   * 是唯一说得出这件事的地方。
   */
  cert('master.example.com', "Let's Encrypt", 38, 'dns-01', [], [], ['master.example.com'], []),
  cert('static.example.com', "Let's Encrypt", 19, 'dns-01', ALL, STATIC_LOADED),
  /*
   * **covers 算不出来**（null）—— 路由清单读不到时的那一支。
   * 它绝不能被渲染成「没人用」：那会引着人去删一张可能还在服务的证书。
   */
  cert('ws.example.com', '外部证书平台 CA', 55, 'imported', ALL, WS_LOADED, ['ws.example.com'], null),
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
/*
 * 每条都带 `detail` —— 审计页有一行专门渲染它。
 *
 * 原先一个都没给，而那一行是 `v-if="a.detail"`，于是**它的渲染我在 dev 里一次
 * 都没见过**，真主控上却每条都有。一个比真实世界更贫瘠的替身，会让本该被看见
 * 的东西一直不出现。check-shapes 抓出来的。
 */
const au = (
  sec: number,
  operator: string,
  action: string,
  target: string,
  src_ip: string | null,
  result: AuditResult,
  detail?: string,
): AuditWire => ({
  id: auditSeq--,
  at: ago(sec),
  operator,
  action,
  target,
  src_ip,
  result,
  // exactOptionalPropertyTypes：`detail: undefined` 与「没有 detail」不是一回事，
  // 而真主控是后者 —— 不给就是键不在
  ...(detail === undefined ? {} : { detail }),
})

export const audit: AuditWire[] = [
  au(5, 'system', '暂停解析', 'node-us-01', null, 'ok', '连续 3 次心跳超时，自动退出解析'),
  au(232, 'abiu', '下发配置', BASELINE, '203.0.113.7', 'partial', '5 成功 / 1 失败'),
  au(370, 'abiu', '修改路由', 'api.example.com', '203.0.113.7', 'ok', 'body_max 4MB → 8MB'),
  au(1_766, 'abiu', '登录', '—', '203.0.113.7', 'ok'),
  au(2_649, 'ops-bot', '续期证书', 'cdn.example.com', '127.0.0.1', 'ok', '有效期至 2026-11-19'),
  au(5_856, 'abiu', '下发配置', PREV, '203.0.113.7', 'ok', '6 成功 / 0 失败'),
  au(7_181, 'zhang', '登录', '—', '198.51.100.24', 'fail'),
  au(7_198, 'zhang', '登录', '—', '198.51.100.24', 'fail'),
  au(45_720, 'ops-bot', '下发配置', 'cfg-91d4f0', '127.0.0.1', 'ok'),
  au(73_320, 'abiu', '新建路由', 'admin.example.com', '203.0.113.7', 'ok'),
]

/* ── 系统设置与告警 ── */

export const settings: SettingsWire & Record<string, unknown> = {
  master_endpoint: 'ec.internal:9000',
  master_endpoint_readonly: true,
  heartbeat_interval_s: 3,
  offline_threshold_count: 3,
  auto_drop_dns: true,
  // 契约 §11：决定一台节点什么时候进 warn。真主控回这两个，mock 原先漏了
  warn_cpu_pct: 80,
  warn_mem_pct: 90,
  // 凭证只写入不回显：这里永远没有明文，只有「配没配」
  dns_provider: { kind: 'cloudflare', domain: 'example.com', sub: '', credential_mode: 'api_token', configured: true },
  /*
   * 照契约 §11 抄的 —— **而这一份是这条链上唯一没有机械保障的一段**：
   * 真主控那边它由 MissingFields 求值得出，到了这里是我手抄的。
   * `check:shapes` 只比形状不比值，抄错了不会红。抄的时候看着契约。
   */
  dns_provider_requirements: {
    dnspod: { '': ['domain', 'credential'] },
    cloudflare: {
      api_token: ['domain', 'credential', 'account_id', 'zone_id'],
      global_key: ['domain', 'credential', 'account_id', 'zone_id', 'email'],
    },
  },
  ops_bot_token_configured: true,
}

export const alerts: AlertsWire = {
  notify_level: 'warn',
  webhook: { url_configured: true },
  lark: { webhook_configured: true, at_all_on_crit: true },
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
