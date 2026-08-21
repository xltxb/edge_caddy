/**
 * 主控 HTTP / WS 契约的前端镜像 —— 逐字对应 `docs/api-contract.md`。
 *
 * 这里的字段名**故意保持 snake_case**，与线上字节一致：契约变更时可以拿这个文件
 * 跟契约文档直接对照，改动落在一处。转成 camelCase 领域对象是 store 的事
 * （见各 store 里的 `from*()`），那也是收掉 `null`、补默认值的唯一地方。
 */

/* ── 0. 全局约定 ── */

/** 统一响应包裹。错误也是这个形状。 */
export interface Envelope<T> {
  code: number
  data: T
  msg: string
}

/** 契约 §0.3。`1002` 是唯一 data 不为 null 的失败码。 */
export const CODE = {
  OK: 0,
  BAD_PARAM: 1001,
  VALIDATION_FAILED: 1002,
  NOT_FOUND: 1003,
  CONFLICT: 1004,
  STATE_CONFLICT: 2001,
  UPSTREAM_FAILED: 3001,
  NODE_UNREACHABLE: 3002,
} as const

/** 校验失败的结构化明细。`field` 是点号路径，数组下标用 `[n]`，对得上表单字段路径。 */
export interface ValidationError {
  res_key: string
  field: string
  reason: string
}

/** 游标分页。倒序追加的流用它，不用 offset。 */
export interface Paged<T> {
  items: T[]
  next_before_id: number | null
}

/* ── 枚举 ── */

export type NodeStatus = 'ok' | 'warn' | 'down'
/** 四档。`ok` = 成功完成的动作（绿），与 `info` 的流水账区分开。 */
export type EventKind = 'ok' | 'info' | 'warn' | 'crit'
export type BlockMode = 'abort' | '403' | '404'
export type RuleType = 'ip_whitelist' | 'service_secret' | 'jwt_bearer'
/** 线上过程态。数据库里的 deploy_state 只有 ok / fail 两个终态。 */
export type DeployProgressState = 'wait' | 'run' | 'ok' | 'fail'
export type AuditResult = 'ok' | 'fail' | 'partial'
export type LogLevel = 'debug' | 'info' | 'warn' | 'error'

/* ── 1. 会话 ── */

export interface SessionWire {
  username: string
  /** `human` 走 Cookie 会话，`bot` 是 ops-bot 的静态 Bearer（契约 §0.6）。 */
  kind: 'human' | 'bot'
}

/* ── 3. 总览 ── */

export interface OverviewKpiWire {
  nodes_online: number
  nodes_total: number
  conns_total: number
  /**
   * 较昨日同时段的变化百分比。**可为 null** —— 冷启动后第一天没有 24h 历史。
   * null 时脚注留白，不要显示 0%（那会被读成「持平」）。
   */
  conns_delta_pct: number | null
  /**
   * 回源率 = 到达 upstream 的请求 ÷ 边缘收到的总请求。**越低越好**。
   *
   * 没到达的那部分是被访问规则拦下（静默断连 / 403 / 404）或由静态响应处理掉的，
   * **不是缓存命中** —— 节点跑的是 apt 装的官方 Caddy，它没有 HTTP 缓存模块，
   * 而「不自建二进制」正是 ADR-0001 与 ADR-0003 共同的前提。
   */
  origin_rate: number
  /** cfg_version ≠ 基线的节点数。只比版本号，不检查节点上的配置内容（ADR-0002）。 */
  drift_nodes: number
}

export interface OverviewWire {
  /**
   * 当前基线版本号，顶栏常驻显示。
   *
   * **契约 §3 目前没有这个字段** —— 已提给 backend。基线是「最近一次下发确立的
   * 那一版」，只有主控知道，前端从节点上报的 cfg_version 里反推是错的
   * （漂移节点会把它带偏）。在后端补上之前，mock 按这个形状给。
   */
  baseline: string
  kpi: OverviewKpiWire
  events: EventWire[]
}

/* ── 4. 边缘节点 ── */

export interface NodeWire {
  id: string
  city: string
  vendor: string
  line: string
  public_ip: string
  status: NodeStatus
  cpu: number
  mem: number
  conns: number
  /** 12 点 CPU 百分比，最新在末尾。主控重启后可能为 null，按留白处理。 */
  cpu_series: number[] | null
  last_hb_at: string
  hb_age_ms: number
  cfg_version: string
  drift: boolean
  dns_enabled: boolean
  /** 该节点**当前生效配置**里的数量，由 Agent 上报，不是全局数量。 */
  routes: number
  rules: number
  created_at: string
}

export interface NodeLogWire {
  at: string
  level: LogLevel
  msg: string
}

/* ── WS 帧 ── */

export interface EventWire {
  id: number
  at: string
  node: string | null
  kind: EventKind
  msg: string
}

export interface HeartbeatFrame {
  type: 'heartbeat'
  data: {
    id: string
    status: NodeStatus
    cpu: number
    mem: number
    conns: number
    hb_age_ms: number
    cfg_version: string
    routes: number
    rules: number
  }
}

export interface EventFrame {
  type: 'event'
  data: EventWire
}

export interface DeployProgressFrame {
  type: 'deploy_progress'
  data: {
    deploy_id: number
    cfg_version: string
    node: string
    state: DeployProgressState
    detail: string
    /**
     * 这一行还会不会再动。ADR-0005：节点未回应 → true，后面还有帧；
     * 节点回应了但 Caddy 拒绝 → false，detail 是 Caddy 原文，到此为止。
     */
    retrying: boolean
  }
}

export type WsFrame = HeartbeatFrame | EventFrame | DeployProgressFrame

/* ── 前端自己的状态，不来自后端 ── */

/** 实时通道状态。断线降级为 2s 轮询（契约 §2），必须对用户可见。 */
export type LinkState = 'connecting' | 'live' | 'reconnecting' | 'polling'

/* ── 6. 配置资源 ── */

/** res_key 格式：route:<domain> / rule:<id> / global:<id>。三类资源共用一套草稿机制。 */
export type ResKind = 'route' | 'rule' | 'global'

export interface RouteWire {
  domain: string
  upstream: string
  block_mode: BlockMode
  mtls: boolean
  compress: boolean
  /**
   * 人类可读字符串（"5MB"）。真实 Caddy 的 max_size 要 int64 字节数，
   * 那个转换是后端渲染器的事 —— 前端不要自己转，也不要把它当成可下发的值。
   * 这正是 ADR-0007 里举的那个例子。
   */
  body_max: string
  whitelist: string[]
  /** 0 = 尚未下发到任何节点，右栏整块显示为新增。 */
  version: number
}

export interface IpWhitelistSpec {
  ips: string[]
}
export interface ServiceSecretSpec {
  header: string
  algo: string
  ttl_s: number
  replay_protection: boolean
}
export interface JwtBearerSpec {
  iss: string
  aud: string
  jwks_url: string
  skew_s: number
}
export type RuleSpec = IpWhitelistSpec | ServiceSecretSpec | JwtBearerSpec

export interface RuleWire {
  id: string
  name: string
  type: RuleType
  enabled: boolean
  /** 空数组 = 未绑定域名，规则不生效。**不是**「对所有域名生效」。 */
  apply_to: string[]
  version: number
  spec: Record<string, unknown>
}

export interface PolicyWire {
  id: string
  name: string
  version: number
  spec: Record<string, unknown>
}

/* ── 6.4 草稿 ── */

export interface DraftMeta {
  by: string
  at: string
}

export interface DraftsWire {
  /** res_key → Partial。Partial 为空对象时后端会删掉该行。 */
  items: Record<string, Record<string, unknown>>
  /** 谁在什么时候改的，用于「别人刚动过」的提示。 */
  updated: Record<string, DraftMeta>
}

/* ── 7. 下发 ── */

export interface PreviewTarget {
  id: string
  status: NodeStatus
}

export interface PreviewWire {
  /**
   * 后端渲染的字节全文。权威性来自「两份都是后端渲染的」，不来自谁算的 diff。
   *
   * 两者都可能是 `null`，**而且不会是空串**（契约 §0.4：null 表示「没有这个值」）。
   * 区分是必要的 —— 空串在这里是一个**合法的配置内容**（一份空配置），
   * 用它代替「没有」，diff 就分不出这两种情况：
   * - `after === null` —— 校验没过，主控没渲染出可下发的配置。**绝不能拿去 diff**：
   *   把它当空串会让整份配置显示成全红删除，读起来像「这次下发会删光一切」。
   * - `before === null` —— 当前基线自己渲染不出来。这时整份显示为新增是对的，
   *   但要说明原因，否则看起来像「所有配置都是新加的」。
   */
  before: string | null
  after: string | null
  /**
   * 当前基线版本号。
   *
   * **没有新版本号**：新号是在 `POST /deploys` 那一刻才生成的，预览时给出的
   * 必然与实际下发的不符。弹层要表达版本递增时写「基线 X → 新版本（下发时生成）」，
   * 不要编号 —— 那是又一个兑现不了的承诺。
   */
  baseline: string
  targets: PreviewTarget[]
  /**
   * 校验失败在这里返回 `code: 0` —— 预览成功地告诉了你「校验没过」，
   * 不是请求失败。只有 POST /deploys 才用 1002 拒绝。
   */
  validation: { ok: boolean; errors: ValidationError[] }
}

export interface DeployCreatedWire {
  deploy_id: number
  cfg_version: string
  targets: string[]
}

export interface DeployResultWire {
  node: string
  state: DeployProgressState
  detail: string
  retrying: boolean
}

export interface DeployWire {
  id: number
  cfg_version: string
  operator: string
  res_keys: string[]
  ok_count: number
  fail_count: number
  /** 当前基线那一条不可回滚，前端应禁用该行的回滚按钮。 */
  is_baseline: boolean
  created_at: string
}

export interface DeployDetailWire extends DeployWire {
  /**
   * 本次广播到的节点 id 列表 —— 进度表的**骨架**。
   *
   * 用它铺满行、再把 `results` 按 node id 合并进去，未回报的显示为「待下发」。
   * 只有 `target_count` 的话，说得出「少 2 个」，说不出**少的是哪 2 个**，
   * 那几行就画不出来。刷新页面或断线重连之后全靠它。
   */
  targets: string[]
  /** `targets.length` 的投影，不是独立存储。 */
  target_count: number
  phase: 'running' | 'done'
  /** 进行中时是**部分**结果，不是全量 —— 轮询降级要按节点 id 合并，不能整体替换。 */
  results: DeployResultWire[]
}

/* ── 9. 证书 ── */

export interface CertWire {
  domain: string
  scope: string
  issuer: string
  key_type: string
  days_left: number
  auto_renew: boolean
  challenge: string
  /** 主控签发记录上应覆盖的节点数 —— 账本 */
  expected_nodes: number
  /** Agent 回执里真正加载了的节点数 —— 回执，不是账本 */
  loaded_nodes: number
  /** 账面有、回执没有的节点。loaded < expected 时界面要能列出来。 */
  missing_nodes: string[]
}

/* ── 4. 节点操作 ── */

export interface NodeTokenWire {
  /** 仅此一次可见，任何后续接口都不回显。 */
  token: string
  expires_at: string
  install_cmd: string
}

export interface DnsToggleWire {
  id: string
  dns_enabled: boolean
  /** 关闭后其余节点的权重在各线路内重新归一化。 */
  weights_rebalanced: boolean
}

export interface ProbeWire {
  reachable: boolean
  rtt_ms: number
  /**
   * 节点本机 127.0.0.1:2019 的可达性，与隧道可达性**分开**报。
   * 隧道通而 Admin 不通 = Caddy 挂了但 Agent 还活着 —— 两种故障处置完全不同。
   */
  caddy_admin: boolean
  cfg_version: string
}

export interface DrainStep {
  step: 'dns_removed' | 'conns_drained' | 'tunnel_closed'
  ok: boolean
  detail?: string
}

export interface DrainWire {
  steps: DrainStep[]
}
