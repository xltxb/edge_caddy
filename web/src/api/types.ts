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
/**
 * 七种。**黑名单与白名单是两种类型，不是一个「方向」开关**（契约 §6.2）。
 *
 * 它们在渲染出的 Caddy 配置里只差一个 `not` —— 合成一个开关的话，
 * 少写那个 `not` 就把「只拦这些」变成「只放这些」，**而配置完全合法、
 * Caddy 照收、站点看起来也正常**（只要访问者恰好在名单里）。
 *
 * `geo_block` 的 `geo_mode` 同理：那一个是后端要求两个方向都显式给、没有默认。
 */
export type RuleType =
  | 'ip_whitelist'
  | 'ip_blacklist'
  | 'request_filter'
  | 'rate_limit'
  | 'geo_block'
  | 'service_secret'
  | 'jwt_bearer'
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
  /**
   * 三档由后端**同一条语句**产出，满足 online + warn + down == total。
   *
   * **不要自己从节点列表推导。** 两边分别推导迟早会算不平 —— 而算不平的账
   * 会在界面上冒出来两次（侧栏一个数、KPI 另一个数），比单个错数字更让人
   * 怀疑整个系统。契约 §3 写明了这条。
   */
  nodes_online: number
  nodes_warn: number
  nodes_down: number
  nodes_total: number
  conns_total: number
  /**
   * 较昨日同时段的变化百分比。**可为 null** —— 冷启动后第一天没有 24h 历史。
   * null 时脚注留白，不要显示 0%（那会被读成「持平」）。
   */
  conns_delta_pct: number | null
  /**
   * `conns_delta_pct` 为 null 时说明是**哪一种** null；有数字时它是 null。
   *
   * 三种对人的意思完全不同：`insufficient_history` 是「再等等就有」，
   * `no_sample` 是「明天同一时刻可能有，除非主控又停」，`zero_baseline` 是
   * 「不是故障，昨天那会儿真没连接」。**只有第一种会自己好起来。**
   *
   * 这个字段是我这边要来的：早先响应里只有一个不带原因的 null，界面只能说一句
   * 谁都不得罪的「暂无同比数据」—— 挑其中一种说会让另外两种的人白等。
   * 现在「为什么没有」是**数据**而不是各自的推断，那一类理由从此不会过期。
   */
  conns_delta_reason: 'insufficient_history' | 'no_sample' | 'zero_baseline' | null
  /**
   * 回源率 = 到达 upstream 的请求 ÷ 边缘收到的总请求。**越低越好**。
   *
   * 没到达的那部分是被访问规则拦下（静默断连 / 403 / 404）或由静态响应处理掉的，
   * **不是缓存命中** —— 节点跑的是 apt 装的官方 Caddy，它没有 HTTP 缓存模块，
   * 而「不自建二进制」正是 ADR-0001 与 ADR-0003 共同的前提。
   *
   * **可为 null**：还没有流量样本时算不出来。不要当成 0 —— 0% 回源意味着
   * 「边缘挡下了全部请求」，那是一个很强的说法。
   */
  origin_rate: number | null
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
  /**
   * **主控自己的构建版本**（契约 §3）。
   *
   * 加它的理由是 2026-08-23 那次复验卡住的地方：那台机器 31 分钟没心跳而控制台
   * 显示「在线」，修复提交了，而我分不清**「修得不对」和「根本没部署」** ——
   * 两者产生的观测一模一样，而处置完全相反。节点侧早就有 `agent_version`，
   * 主控侧此前没有对应的东西。
   *
   * 未打标的构建是 `"dev"` 而**不是空串**：空白读起来像「这个字段还没做」。
   *
   * mock 此前漏了它 —— `check:shapes` 接进检查链、本地主控重启之后才报出来。
   * **一个没被比过的端点，和一个比过的端点，在那之前长得一样。**
   */
  master_version: string
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
  /**
   * **这条隧道此刻连着吗** —— 会话表里实时读的，没有去抖。
   *
   * 与 `status` 回答的是两个不同的问题：`status` 问「这台机器健康吗」，由心跳
   * 判定产出、**有去抖**（连续错过 N 个周期才翻成 down）。所以两者**短暂不一致
   * 是正常的**，那个窗口就是判定的去抖；**持续不一致是 bug**（契约 §4）。
   *
   * **界面上的在线徽标认 `status`，不认这个。** 它是排查用的辅助信号 ——
   * 升格成第二个徽标就是在 ADR-0014 的正交模型上无理由地再加一维，而
   * 「合成一个徽标运维半夜分不清该不该起床」那条论证，反过来同样约束着加维度。
   *
   * 这个字段从第一个切片起就在后端返回里，但直到 2026-08-23 才写进契约 ——
   * 在那之前前端不知道它存在。那天线上一台机器 31 分钟没心跳而控制台显示
   * 「在线」：主控重启后 health 的内存 map 清空，已失联的节点永远进不去，
   * 于是 `status` 永久停在库里的旧值 `ok`，而 `online` 一直是诚实的 `false`。
   * **两个字段早就在打架，只是没有一个地方把它们放在一起看。**
   */
  online: boolean
  /**
   * **过去一小时主控这边记录到几次隧道建立**（契约 §4）。
   *
   * 口径是**主控的观测**（会话表），不是那台机器自己的说法。两者结构上就
   * 对不齐：**一个被 kill 的进程不会记录自己的死亡** —— 实测过一次，主控说
   * 3 次而 Agent 日志里只有 1 条「隧道断开」，另外两次它是被 SIGTERM 停的。
   * 主控那份不依赖对方还活着，所以它才是一手观测。
   *
   * **界面上别写成「断连 N 次」**：那会让人拿它去跟同一屏的 Agent 日志对账，
   * 而那两个数本来就不该相等。
   *
   * `status` / `online` / `hb_age_ms` 都是**瞬时值**。隧道断开到重连只要 1–2 秒，
   * 而离线判定要连续错过 `heartbeat_interval_s × offline_threshold_count`
   * （默认 9 秒）才翻 down —— 所以**一条每十分钟断一次的隧道，在那三个字段上
   * 全部是健康的**。灰度上真发生过：CDN 每隔十几分钟切一次长连接，界面上看不出
   * 任何异常，唯一的痕迹在那台机器的 Agent 日志里。
   *
   * 上一段那句「短暂不一致是正常的，那是去抖窗口」仍然成立，而这个字段是它的
   * 另一面：**去抖分不出「一次抖动」和「反复抖动」**。前者不该惊动人，后者是
   * 故障。区分它们要的不是更灵敏的判定，是一个跨时间的计数。
   *
   * **`null` = 数不出来，不是 0。** 这个字段的存在理由就是「在一切看起来正常时
   * 指出异常」，而 `0` 恰好是「一切正常」的样子 —— 拿 0 当兜底，等于让它在自己
   * 失效的那一刻伪装成它最想否定的那个状态。别的字段退化成 0 只是丢信息，
   * 这一个退化成 0 是主动说反话。
   *
   * 主控太旧（还没有这个字段）时也是 `null`。对看界面的人来说两者一样：
   * 这个数拿不到 —— 而**「拿不到」和「是 0」必须分开**。
   */
  reconnects_1h: number | null
  /**
   * **这台节点上的 GeoIP 库跟主控那份一致吗**（契约 §6.2）。
   *
   * | 值 | 意思 | 界面 |
   * |---|---|---|
   * | `true` | 一致 | 不显示任何东西 |
   * | `false` | **没有库，或者是旧的** | 标出来 —— 它上面的地域规则不生效 |
   * | `null` | **主控自己没有库**（没在用这个功能） | 什么都别显示 |
   *
   * ## 这里的 `null` 跟上面那个 `reconnects_1h` 的 `null` 意思相反
   *
   * 那一个是「数不出来」—— 存在理由就是在一切看起来正常时指出异常，
   * 所以退化成 0 是**主动说反话**，必须显示成「拿不到」。
   *
   * 这一个是「不适用」。一个没在用地域功能的系统，每台节点都标一句
   * 「GeoIP 库缺失」，是在报告一个**不存在的问题** —— 而人两天就学会忽略它，
   * 连带着忽略掉真出问题那天的那一条。
   *
   * 两个 `null` 在同一个 interface 里、隔着二十行，而处理方式正好相反。
   * 写出来是因为照着上面那条的模式处理这一条，会得到一个天天报假警的界面。
   *
   * ## 它为什么存在
   *
   * 后端先写好了整条链路的 e2e（主控放库 → 节点报旧哈希 → 推送 → 落盘 →
   * 热加载 → 地域规则真的拦人），全过。然后把「节点收下库后更新哈希」
   * 那一步去掉 —— **测试没红**：库照样生效、规则照样拦人，
   * 差别只是主控每次心跳都重推一份几 MB 的库，永远推下去。
   *
   * **一个观测不到的状态，它的 bug 也观测不到。** 这一列不只是给人看的，
   * 它是那条链路唯一的验收面。
   */
  geo_db_ok: boolean | null
  cpu: number
  mem: number
  conns: number
  /** 12 点 CPU 百分比，最新在末尾。主控重启后可能为 null，按留白处理。 */
  cpu_series: number[] | null
  last_hb_at: string
  hb_age_ms: number
  cfg_version: string
  drift: boolean
  /**
   * 那台机器上跑的 Agent 版本，接入时上报、每次接入覆写（契约 §4）。
   * **空串 = 还没接入过**，不是「版本未知」——两者在灰度上意思完全不同。
   */
  agent_version: string
  dns_enabled: boolean
  /**
   * 人**明确让它退出服务**的时刻；没下线过就是 null。
   *
   * **与 `status` 正交，不要合成一个徽标。** `status: down` 是**观察**（主控连着
   * 几个周期没收到心跳）；`drained_at` 是**意图**（人按了下线）。一台节点可以
   * 「已下线且在线」（刚按下，隧道还没断干净），也可以「未下线但离线」（它自己
   * 挂了）。前者是「我关的」，后者是故障 —— 合成一个徽标，运维半夜分不清该不该
   * 起床（CONTEXT.md、ADR-0014）。
   *
   * 非 null 时该节点：不参与解析、不进下发目标、不接受接入、也不报离线告警。
   */
  drained_at: string | null
  /**
   * 解析**是谁关的**。空串 = 从没人动过（与「manual 而操作人不详」是两回事）。
   *
   * 三种对人的意思不同，而不同的是**接下来该做什么**：
   *   `manual`       人在控制台点的 → 他心里有数，想开就开回来
   *   `auto_offline` 心跳超时，系统自动摘的 → **先去修那台机器**，开解析没用
   *   `drained`      人把节点下线了 → 要用先「重新上线」
   */
  dns_reason: 'manual' | 'auto_offline' | 'drained' | ''
  /**
   * 操作人账号名。**系统自动摘除时是 `null`，不是 `"system"`** ——
   * 一个叫 system 的操作人会在界面上冒出一个不存在的账号，而人会去问那是谁。
   */
  dns_actor: string | null
  /** 改动时刻。永远是真实时刻或 null，不会是零值时间（契约 §0.4）。 */
  dns_changed_at: string | null
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
/**
 * 黑名单。**跟白名单是两个类型，字段却一模一样** —— 区别全在 `type` 上。
 *
 * 空名单两者都被后端拒（契约 §6.2），而**理由相反**：空白名单拦下所有人
 * （一次事故），空黑名单谁也拦不到（一条静默失效的规则）。
 * 界面上它们长得一模一样：一条启用着的规则。所以两句提示词不同。
 */
export interface IpBlacklistSpec {
  ips: string[]
}

/** 一条请求特征。`field` 决定 `op` 能取哪些 —— 那张表由后端报，不在这里写死。 */
export interface RequestFilter {
  field: string
  op: string
  value: string
  /** `header` / `query` 要指明看哪一个。哪些 field 需要它由后端的表说了算。 */
  name?: string
}

/**
 * 请求特征过滤。**多条之间是「或」**（契约 §6.2）——
 * 界面上要说清，否则人会按「且」去配。想要「且」是配成两条规则。
 */
export interface RequestFilterSpec {
  filters: RequestFilter[]
}

/**
 * 限流。**令牌桶，不是滑动窗口**：`requests` 同时是持续速率的分子
 * （`requests / window_s`）和**允许的突发**。
 *
 * 说「每 60 秒 100 次，允许一次性来 100 个」比说「限速」准确 ——
 * 纯速率会误伤正常用户（一个人打开页面会并发十几个请求），
 * **而被误伤过一次的限流，人会直接把它关掉**。
 *
 * **计数是每节点各算各的**：三台节点、每台限 100，全局实际是 300。
 * 这一点界面必须说出来，而且要在**编辑那一刻**说 —— 一个人配「100」时
 * 就该看见「300」，不是配完了再被告知。
 */
export interface RateLimitSpec {
  requests: number
  window_s: number
  /**
   * `ip` | `ip_path`。
   *
   * `ip_path` 让「猛刷登录接口」和「正常浏览别的页面」互不影响，代价是桶数量
   * 乘以路径基数 —— **而路径是攻击者能控制的**，所以它只在配了 `request_filter`
   * 限定路径时才有意义。界面上要提醒这一点。
   */
  rate_key: string
}

/**
 * 地域封禁 / 放行。
 *
 * `geo_mode` **两个方向都要显式给，没有默认**（契约 §6.2）——
 * 猜错一个就是把站点封了或者敞开了。
 *
 * `geo_countries` 是 **ISO 3166-1 alpha-2 两位大写**。小写会被校验拒 ——
 * mmdb 里存的是大写，`cn` 会**静默匹配不到任何东西**。
 */
export interface GeoBlockSpec {
  geo_mode: string
  geo_countries: string[]
}

export type RuleSpec =
  | IpWhitelistSpec
  | IpBlacklistSpec
  | RequestFilterSpec
  | RateLimitSpec
  | GeoBlockSpec
  | ServiceSecretSpec
  | JwtBearerSpec

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

/**
 * `GET /rules` 的响应 —— 规则列表**外加两张后端报出来的表**（契约 §6.2）。
 *
 * ## 照它渲染，不要抄
 *
 * `filter_fields` 说的是每个 `field` 允许哪些 `op`。它与后端的校验
 * **共用同一张表**（`model.FilterFieldOps`），不是抄本。
 *
 * 界面自己抄一份的代价是两边在某次改动时分叉，而**那个分叉两个方向不对称**：
 *
 * - 给出一个后端会拒的选项 —— 人配完被拒，**还算看得见**。
 * - 藏起一个后端接受的 —— **从界面上完全看不出来**。
 *
 * 后者更贵，而它恰恰是「抄一份然后忘了跟」最常见的结果。
 *
 * 与 `dns_provider_requirements` 同一条路子：**哪家要什么、哪个字段能用哪些
 * 算子，只有一个来源**，界面拿它当渲染的钥匙。
 *
 * ## 这个形状还没在真主控上验过
 *
 * 探针拿到的 `data` 顶层只有 `items` —— 查下来是主控进程比后端那次提交旧
 * （这已经是第三次了）。所以这两个字段的位置是**照契约写的，不是观测到的**。
 * 可选，正是为了兜住「主控太旧」那一档：那时下拉退回到只有 items 能给的东西。
 */
/**
 * 一条规则「还差什么」。与下发那次的校验**共用同一段代码**
 * （后端的 `render.RuleSpecIssues`），不是抄本 —— 分叉的症状相反：
 * 列表说它完整而下发被拒，或者列表标红而它其实能下发。
 */
export interface RuleIssue {
  /** `rule:<id>`，与下发校验回的 `validation.errors[].res_key` 同构。 */
  res_key: string
  /** 点号路径，`spec.window_s` / `apply_to`。 */
  field: string
  /** 原样显示，不要自己编 —— 它说的是这一条为什么不完整。 */
  reason: string
}

export interface RulesWire extends Paged<RuleWire> {
  /** field → 允许的 op 列表。缺失 = 主控太旧，见上。 */
  filter_fields?: Record<string, string[]>
  /**
   * 哪些 field 还要再指明「看哪一个」（`header` 看哪个头、`query` 看哪个参数）。
   * **从表里读，不要再判一次 field 名** —— 那就是又一份会分叉的知识。
   */
  filter_fields_need_name?: Record<string, boolean>
  /**
   * **每条规则还差什么** —— `ruleId → 问题清单`（契约 §6.2）。
   *
   * ## 三档，而它们说的是不同的事
   *
   * | 值 | 意思 | 界面 |
   * |---|---|---|
   * | `[]` | 算过了，它是完整的 | 不显示 |
   * | 非空数组 | 差这几样，下发会被拒 | 标出来，原样列出 reason |
   * | `null` | **这次算不出来** | 不能显示成「没问题」 |
   *
   * 整个字段缺失 = 主控太旧，还没有这个能力 —— 同样不能当成「都完整」。
   *
   * ## 它为什么存在
   *
   * 灰度上一条限流规则下发被拒（`spec.window_s` 要大于 0），而那条规则
   * 可能是几天前建的 —— **校验拦住的地方，离出错的地方隔了很远**。
   *
   * 更硬的一条：那条规则**根本不是从界面建的**（当时那一版没有新建入口）。
   * 所以「建它的那个界面会拦住」指望不上 —— **任何途径进来的半成品，
   * 都得在列表这一层被看见**。
   *
   * ## 「已停用」和「还不完整」不能合成一句
   *
   * 停用的规则也照实列（后端确认过这个判据）。两者是意图与事实：
   * 合成一句「不生效」会让人以为**启用它就能用了**。
   */
  incomplete?: Record<string, RuleIssue[] | null> | null
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

export interface RollbackSkipped {
  res_key: string
  /** 后端给的中文原因，可直接呈给用户。 */
  reason: string
}

export interface RollbackWire {
  /** 被写回草稿的资源。回滚**不直接下发** —— 人要在工作台确认 diff 后走同一条流水线。 */
  res_keys: string[]
  /**
   * 回滚**覆盖不到**的资源：那次下发之后被删的（不会建回来）、之后才新建的
   * （不会删掉）。草稿是叠加在 live 行上的 Partial，那一行不存在就无处可叠。
   *
   * **必须显示出来。** 人点了「回滚到某版本」、界面说成功了，而某条路由其实
   * 没回去 —— 那是一次静默的失败，而且要等到下次出问题才会被发现。
   */
  skipped: RollbackSkipped[]
}

/* ── 10. 审计 ── */

export interface AuditWire {
  id: number
  at: string
  operator: string
  /** 取值见契约 §5 的术语表。由后端产生、前端原样显示，所以措辞是契约的一部分。 */
  action: string
  target: string
  src_ip: string | null
  result: AuditResult
  detail?: string
}

/* ── 9. 证书 ── */

export interface CertWire {
  domain: string
  issuer: string
  /**
   * 这张证书**怎么来的**：`imported`（外部平台推进来）或 `dns-01`
   * （ADR-0015 之前主控自己签的，历史数据）。界面上不直接渲染这个英文值
   * —— 映射见 `CertsView.vue`。
   *
   * **字段名不好**（契约 §9 也记了这一条）：`challenge` 在 ACME 里指校验方式，
   * 而 `imported` 恰恰是「没有经过任何校验」。留着是因为库里那一列就叫这个
   * 名字，改名要动迁移、契约、前端三处。
   */
  challenge: string
  /**
   * 这张证书**实际覆盖的域名**，含通配符（契约 §9）。
   *
   * 后端**从证书本身读出来、不落库** —— 存一份副本意味着两处真相，而两处
   * 迟早分叉：导入时解析对了，后来换了张证书而副本没更新，界面就会说这张
   * `*.a.com` 覆盖的是 `b.com`。
   *
   * 界面上摘要成「单域名 / 通配符 / N 个域名」（`@/certs/coverage`），
   * **但完整列表要留得住**：`*.a.com` 不覆盖 `x.y.a.com`（RFC 6125，通配符
   * 只匹配一级），所以那句摘要回答不了「我这个域名在不在里面」。
   */
  domains: string[]
  /**
   * 到期时刻，RFC3339（契约 §9）。
   *
   * **后端一直在返回它，而前端此前不知道它存在** —— 数据到了没人用，两边都
   * 不报错。这跟 `scope` / `key_type` 那两个（前端声明了而后端从来没发过）
   * 正好是镜像，共同点是**类型定义与真实响应之间没有任何东西在比对**：
   *
   *   前端有、后端没发   → 界面上一个空格子
   *   后端发了、前端没有 → **连空格子都没有**，所以更不会被发现
   *
   * 列表里显示的是 `days_left`（好扫），日期用在快到期的那两档 —— 到期告警的
   * 文案里带的是日期，人拿着告警来对界面时得能一眼对上。
   */
  not_after: string
  days_left: number
  /** 主控签发记录上应覆盖的节点数 —— 账本 */
  expected_nodes: number
  /** Agent 回执里真正加载了的节点数 —— 回执，不是账本 */
  loaded_nodes: number
  /** 账面有、回执没有的节点。loaded < expected 时界面要能列出来。 */
  missing_nodes: string[]
  /**
   * 这张证书**正在服务**的路由域名（契约 §9）。与 `domains` 的区别是承重的：
   *
   *   `domains`  它**能**服务什么      —— 证书自己的 SAN
   *   `covers`   它**实际**服务着什么  —— 与我们的路由清单对过
   *
   * **三种值三个意思，界面必须分开：**
   *
   *   `["a.example.com"]`  正在服务这些        → 正常
   *   `[]`                 **存着但没人用**    → 可以标，删它是安全的
   *   `null`               路由清单读不到      → **不能标**
   *
   * `[]` 是个**会引着人动手**的断言 —— 它说「删这张是安全的」。而 `null` 是
   * 「我算不出来」，把它渲染成 `[]` 会引着人去删一张可能还在用的证书。
   * 与 `reconnects_1h` 同一条理由，只是后果更直接：那个是误判健康，这个是误删。
   *
   * 判据与 `PUT` 那道 `1003` 共用（`VerifyHostname`）—— 分开写的话
   * 「收得进来」和「删得掉」会对不上账。
   */
  covers: string[] | null
  /*
   * **没有 `auto_renew`**（契约 §9）：主控不自动续期，那是这套系统的属性，
   * 不是每一行的属性。一个恒为 false 的字段会暗示「这个概念存在、只是关着」，
   * 于是人会去找打开它的地方。
   *
   * **也没有 `scope` / `key_type`。** 这两个前端曾经声明过、模板也渲染过，
   * 而后端 `certResp` 里**从来没有过它们** —— 线上那两格一直是空的，
   * 而 mock 的 seed 提供了它们，所以 dev 下一直看着正常。
   * **替身比真的更完整，于是这个洞在开发期是隐形的。**
   */
}
/**
 * `DELETE /certs/:domain` 的响应。
 *
 * **成功即已下发。** `load_pem` 是全量替换的，只有下一次下发才会把它从节点上
 * 摘掉 —— 库里删了而不下发的话，**界面说它没了，而它还在服务**。
 *
 * 下发失败时后端返回 `3001`，`msg` 说清「已从库中删除，但节点上仍在服务它」，
 * 审计记 `partial`（那不是「没删成」）—— 界面要把那句原样显示出来。
 */
export interface CertDeleteWire {
  domain: string
  detail: string
}


/* ── 4. 节点操作 ── */

export interface NodeTokenWire {
  /** 仅此一次可见，任何后续接口都不回显。 */
  token: string
  /**
   * 主控隧道 CA 的指纹。与 token 不同，它**不是**秘密，也不随这次签发变化。
   *
   * 单独给一份是因为 `install_cmd` 是**一条要执行的命令**，不是「两个值的来源」——
   * 要单独取值就取这两个字段，不要去解析那条命令。
   */
  ca_pin: string
  expires_at: string
  /**
   * 完整的安装命令，脚本形式。
   *
   * **不是裸的 `edge-agent …`。** 裸命令跑得起来，但跑起来是前台进程、没有
   * systemd 单元、没有 `Restart=always`——而受保护域名的 fail-closed 依赖
   * Agent 存活（ADR-0003）。发一条绕过它的命令，等于让人有机会省掉这个
   * 脚本存在的理由。
   */
  install_cmd: string
  /**
   * 装完之后该跑的检查命令。
   *
   * 单独一个字段而不是由前端拼：它和 `install_cmd` 是同一份知识的两半，
   * 拼在前端就是两处知识。后端有一条测试同时盯着这两条命令与脚本的参数。
   *
   * **界面要和 `install_cmd` 并排呈现，不能折叠。** 它查的是 Caddy Admin 有没有
   * 暴露在回环之外 —— 私钥以 `load_pem` 内联在运行配置里（ADR-0010），能读
   * Admin 就能读到。脚本为「没在监听」和「监听错地方」分了两个返回值，
   * 而**一道没有人会执行的检查，等于不存在**。
   */
  verify_cmd: string
  /**
   * 「你必须自己先办好的事」。**别按当前长度写死**，它会增减。
   *
   * 这些是命令里指着、却没有任何东西负责送上去的文件。它们的共同点不是
   * 「承诺了将来」而是**「假定了当下」**——假定的东西对写命令的人成立，
   * 所以他不会想到要说。
   */
  prerequisites: string[]
}

/**
 * 最近一次把解析安排推给服务商的结果。**常驻**。
 *
 * 与权重里的 `share` 是两件事：`share` 是我们**打算**怎么分，`dns_sync` 说的是
 * **服务商那边真的这样了没有**。界面上「已退出解析」那类徽标也是常驻的，只靠
 * `POST /nodes/:id/dns` 那个一次性的 `dns_synced`，一次失败的同步会留下一个
 * 一直撒谎到下次有人再点开关为止的徽标 —— 常驻的说法需要常驻的真相来源。
 */
export interface DnsSyncWire {
  ok: boolean
  /**
   * 上次同步的时刻。**从来没同步过时是 `null`**（契约 §0.4）。
   *
   * 后端一度在这里给 Go 的零值时间 `0001-01-01T00:00:00Z`。那种值的坏处不是
   * 「不好看」，是它**格式正确而意思是假的**：会被渲染成一个像模像样的
   * `00:00:00`，读起来像「凌晨同步过一次」。空白会让人去查，一个像样的时间不会。
   * `isZeroTime()` 两种都挡，即便后端回退也不会漏出来。
   */
  at: string | null
  detail: string
  /**
   * **每个域名各自的结果**（契约 §11）。
   *
   * `ok` 是「全都成功」—— 三个坏一个时它是 `false`，而**另外两个是好的这件事，
   * 只有这里说得出来**。一个布尔说不出「三个里哪一个没上」，而人会按那个布尔
   * 决定要不要去查。
   *
   * **`null` 是旧数据**（写于只支持单域名的版本），**不是「一个目标都没有」** ——
   * 那一档退回只显示 `detail`。跟 `covers` / `reconnects_1h` 同一条：
   * 空数组是个断言，`null` 是「这次说不了」。
   */
  /**
   * **这个键可能整个不存在** —— 不只是 `null`。
   *
   * `check:shapes` 打真 dev server 之后撞出来的：真主控在「尚未配置 DNS
   * 服务商」时回的 `dns_sync` 只有 `ok` / `at` / `detail` 三个键。
   * 契约 §11 的示例里它是在的，所以这里原先声明成必有。
   *
   * 三档要分开（跟 `covers` / `reconnects_1h` 同一条）：
   * 有数组是「按域名分开记的结果」，`null` 是「这次说不了」，
   * **键不存在是「这个主控在这一档下不发它」** —— 而前端读到的都是 falsy，
   * 照着「必有」写出来的 `.map()` 会崩在一台没配服务商的主控上。
   */
  targets?: DnsSyncTargetWire[] | null
}

/** 单个域名的同步结果。`detail` 原样显示 —— 它说的是这一个为什么没上。 */
export interface DnsSyncTargetWire {
  hostname: string
  ok: boolean
  detail: string
}

/** `GET /nodes` 的响应。除了分页，还带着两个全局事实。 */
export interface NodesPageWire extends Paged<NodeWire> {
  baseline: string
  dns_sync: DnsSyncWire
}

/**
 * `PUT /nodes/:id` 的响应 —— 改元数据。
 *
 * **`dns_synced` 说的是解析真的变了，不是库里写成功了**，与 `DnsToggleWire`
 * 同一条规矩。改 `public_ip` 会立刻把解析推到服务商，那一步可能失败（没配服务商、
 * 服务商报错），而**库里的 IP 已经改了** —— 不呈现 `detail` 的话，人以为改完就
 * 生效了，实际访问者还被送到旧地址。
 *
 * 只改城市/机房/线路时 `dns_synced` 是 `false`、`detail` 是空串。**那不是失败**，
 * 是这次改动跟解析无关 —— 界面不能把它显示成一次没成功的操作。
 */
export interface NodeUpdateWire {
  id: string
  city: string
  vendor: string
  line: string
  public_ip: string
  dns_synced: boolean
  detail: string
}

/**
 * `DELETE /nodes/:id` 的响应 —— 只删记录，不碰那台机器。
 *
 * `detail` 里那句话**要原样显示**：它说的是这个操作**只做了一半** —— 记录没了，
 * 而那台机器上的 Agent 与 Caddy 还在跑，还在监听 80/443、还在用最后一次拿到的
 * 配置服务。人点完删除会以为干净了。
 *
 * **一个只做了一半的操作，必须自己说出另一半是什么。**
 */
export interface NodeDeleteWire {
  id: string
  detail: string
}

/**
 * `PUT /settings` 的响应（契约 §11）。
 *
 * **改完服务商会立刻推一次解析**，而这两个字段说的是那一步成没成 ——
 * 与 `NodeUpdateWire` / `DnsToggleWire` 同一条规矩：**库里写成功 ≠ 解析真的变了**。
 *
 * 契约里那段话记的是灰度上撞到的事：
 *
 * > 换服务商、填好凭证、保存成功、徽标变绿 —— 而服务商那边一条记录都没有。
 *
 * 触发同步的是「保存权重 / 动节点开关 / 改节点 IP / 心跳摘挂」，
 * **改服务商本身不在其中**，人得再去随便碰一个别的东西才会推。
 *
 * `detail` 在**与 DNS 无关的那次修改里是空串**（§0.4）—— 只改了心跳间隔时
 * 弹一句「解析已同步」，是在报告一件没发生的事。所以呈现时要判空，
 * 不能无条件把它拼进 toast。
 */
export interface SettingsUpdateWire {
  dns_synced: boolean
  /** 说清 dns_synced 为什么是那个值。直接呈现，不要自己编。 */
  detail: string
}

export interface DnsToggleWire {
  id: string
  dns_enabled: boolean
  /**
   * **服务商那边真的变了没有。**
   *
   * 标志位和解析记录是两件事：标志位决定归一化里谁参与，同步才让流量真的改道。
   * 没配服务商时是 false，同步失败时也是 false —— 两种情况下这台机器都**照旧在
   * 解析里**。不呈现它的话，人点完开关会以为流量已经不走那台机器了。
   *
   * 与「Caddy 接受了配置 ≠ 流量在跑」是同一类：**做完了一步，不等于那件事成了。**
   */
  dns_synced: boolean
  /** 说清 dns_synced 为什么是那个值。直接呈现，不要自己编。 */
  detail: string
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
  /**
   * **这件事真的发生了**，不是「我这边的记录改成功了」。
   *
   * 没配 DNS 服务商时 `dns_removed` 就是 false，`conns_drained` 会被整个跳过
   * —— 还在进水的池子排不干净。
   */
  ok: boolean
  /**
   * **原样显示，不要截断、不要拼接。**
   *
   * 它是人接下来那个决定的判据：`conns_drained` 会说「解析缓存未过期前仍可能有
   * 新连接进来」—— 那是「已排空」这句话的边界，不说的话人会据此关机；没排干净时
   * 它带着还剩多少条，而「还剩 2 条」和「还剩 8000 条」要做的是两个不同的决定。
   */
  detail?: string
}

export interface DrainWire {
  steps: DrainStep[]
}

/** 撤销下线。**解析不会自动打开** —— 能接入不等于该马上分流量。 */
export interface RejoinWire {
  id: string
  drained_at: null
  dns_enabled: boolean
  detail: string
}

/* ── 8. DNS 调度 ── */

export interface DnsEntryWire {
  node: string
  /** 配置值 —— 输入框绑这个。 */
  weight: number
  /**
   * 实际占比 —— 占比条画这个。
   *
   * 与 weight **不是一回事**：`dns_enabled: false` 的节点（手动暂停或心跳超时
   * 自动摘除）share 为 0，它的权重在该线路内的其余节点间重新归一化。
   * 把两者混成一个数字，就说不清「我配了 40，为什么它没在扛流量」。
   */
  share: number
  dns_enabled: boolean
  status: NodeStatus
}

export interface DnsLineWire {
  /** 固定五个：ct 电信 / cu 联通 / cm 移动 / tw 台湾 / ov 境外 */
  code: string
  name: string
  entries: DnsEntryWire[]
}

/**
 * 服务商能力。**两家服务商的能力不对等**，而 PRD 把它们并列了。
 *
 * Cloudflare 的 DNS 记录没有线路与权重概念，加权调度要上独立付费的
 * Load Balancing，而它的地理维度是国家 / 大洲 —— 电信 / 联通 / 移动在那边
 * 根本表达不了，三者会被合并成「中国」。
 *
 * 这跟「回源率靠缓存」是同一类：**假设了一个服务商没有的能力**。
 * 选了 Load Balancing 不会让这个限制消失，所以它必须出现在界面上。
 */
/**
 * 服务商能表达的一条线路，以及它覆盖了契约里的哪几条。
 *
 * `covers` 由后端给，**前端不再持有任何服务商的地理模型** —— 加第三家服务商
 * 时前端不用改：它可能是 `apac` 覆盖若干条，前端只需照着渲染。
 */
export interface DnsCapabilityLineWire {
  code: string
  name: string
  covers: string[]
}

export interface DnsCapabilitiesWire {
  /** 空串 = 尚未配置服务商。此时权重仍可保存（那是本地意图），但推不出去。 */
  kind: string
  /** 服务商能表达的线路。null / 空 = 未配置服务商，按契约五条原样渲染。 */
  lines: DnsCapabilityLineWire[] | null
  weights: boolean
  /** 可直接呈给用户的中文说明。 */
  notes: string
}

export interface DnsWeightsWire {
  domain?: string
  lines: DnsLineWire[]
  capabilities?: DnsCapabilitiesWire
  /** 服务商那边真的这样了没有。与 lines 里的 share（我们打算怎么分）是两件事。 */
  dns_sync?: DnsSyncWire
  /**
   * **这份轮换会被写到哪几个名字上**（契约 §11）。
   *
   * 与单数的 `domain` 的区别是承重的：那个是旧形态，**多域名时它是不全的**。
   * 界面用这个。
   */
  domains: string[]
}

/* ── 11. 系统设置与告警 ── */

export type CredentialMode = 'api_token' | 'global_key'

/**
 * `GET /settings` 回的那一份。**凭证不在里面**，只有「配没配」。
 *
 * 契约 §11 的示例只列了 kind / credential_mode / configured，而主控实际还回
 * `domain` 与 `sub` —— 那两个是**配置这件事本身需要的**（要往哪个 zone 写记录），
 * 不带它们的话界面配不出一个完整的服务商。
 */
/**
 * 一条要管的记录写到哪儿（契约 §11）。
 *
 * **凭证与 `kind` 只有一份**（同一个服务商账号），变的是每条记录的落点。
 * `zone_id` **每条各填**（仅 Cloudflare）：同一 zone 下的多个子域填同一个，
 * 不同顶级域各填各的；DNSPod 不需要它。
 */
export interface DnsTargetWire {
  domain: string
  /** 子域前缀，可空 —— 记录写在 `<sub>.<domain>` 上。 */
  sub: string
  zone_id: string
}

export interface DnsProviderWire {
  kind: string
  /**
   * **要管的全部主机名。** 配过的话永远非空 —— 照它渲染。
   *
   * 轮换是**共享**的：所有域名指向同一组边缘节点，权重表不分域名。
   */
  /**
   * **`null` 是「一条域名都没配」，不是空数组** —— 真主控在库里没有记录时
   * 回的是 `null`（`check:shapes` 撞出来的：mock 一直回数组，于是类型里也
   * 写成了数组，而界面拿它直接 `.map()` 会炸在一台刚装好的主控上）。
   *
   * 两者在界面上要表现成同一件事（都是「还没有域名」），但类型上不能合并 ——
   * 合并的代价是下一个人照着类型写出一个只在新装环境里崩的页面。
   */
  targets: DnsTargetWire[] | null
  /**
   * @deprecated 旧形态，只剩兼容用途 —— **多域名时它是不全的**。
   *
   * 库里有旧数据（写于只支持单域名的版本），主控在 `targets` 为空而它非空时
   * 合成一条。**不直接删是因为一次读不出来的配置会表现成「解析突然不同步了」**，
   * 而没有任何一处说得出为什么。界面别拿它当权威。
   */
  domain: string
  /** @deprecated 见 `domain`。 */
  sub: string
  credential_mode: CredentialMode | ''
  /** 凭证只写入不回显 —— 这里永远没有明文，只有「配没配」。 */
  configured: boolean
}

/**
 * `PUT /settings` 的 `dns_provider`。**字段嵌在这个对象里，不在顶层。**
 *
 * 我此前把凭证发成了顶层的 `dns_credential` —— 后端不认识那个 key，
 * **静默忽略并回 code 0**，于是界面弹「设置已保存」而什么都没存进去。
 * 一个成功的假象比一个报错难查得多：报错会让人再试，假象让人走开。
 *
 * 两家服务商需要的字段不一样，界面据此切换（契约 §11）：
 *   dnspod      kind + domain + sub + credential（形如 `ID,Token`）
 *   cloudflare  上面这些 + credential_mode + **zone_id + account_id（两种模式都必填）**
 *               global_key 再加 email
 *
 * **`account_id` 这里此前写着「可选」，那是错的**，而设置页那个输入框是照它做的
 * —— 于是 api_token 模式下界面上根本没有那个框。灰度上的症状：保存成功、
 * 设置页显示已配置，而推权重时 Cloudflare 回
 * `GET /accounts//load_balancers/pools` → 7003「Could not route to ...」。
 * 那个双斜杠就是空字段，而它的错误消息不认识我们的字段名。
 *
 * 两个都是拼进 URL 的路径段（pool 是账号级的，load balancer 挂在 zone 上），
 * 少任何一个是**整条推送不成立**，不是「功能弱一点」。
 *
 * `credential` 留空 = 保持不变，带了 = 替换。
 */
export interface DnsProviderFields {
  kind?: string
  /**
   * **给了就整个替换，不是逐条合并**（契约 §11）。
   *
   * 合并的话「删掉一个域名」没法表达：给一个少一条的列表会被读成「这几条不变」，
   * 而那个域名会永远留在配置里继续被同步。
   */
  targets?: DnsTargetWire[]
  /** @deprecated 旧形态，新界面发 `targets`。 */
  domain?: string
  /** @deprecated 见 `domain`。 */
  sub?: string
  credential_mode?: CredentialMode
  zone_id?: string
  account_id?: string
  email?: string
  credential?: string
  clear?: never
}

/**
 * 清掉整份配置（含凭证）。**独立动作，不能与其他字段同给**——同给返回 `1002`。
 *
 * 后端不肯替人在两种读法（先清再设 / 清掉一切）里挑一种。写成联合类型是为了
 * 让这条约束由类型检查器管：`{ clear: true, kind: 'dnspod' }` 编译就不过，
 * 而不是等运行时换回一个 1002。
 */
export interface DnsProviderClear {
  clear: true
}

export type DnsProviderPatch = DnsProviderFields | DnsProviderClear

export interface SettingsWire {
  /**
   * 主控**真正在用**的那个地址，**只读**（契约 §11：`PUT` 带它一律 `1002`）。
   *
   * 契约 §11 写清了运行时改不了的理由：这个地址进了主控**服务端证书的 SAN**，
   * 而证书是启动时签的 —— 改设置改不了证书，那时节点会连上一个证书里没有它
   * 的地址，握手直接失败。「必须是域名不是 IP」那条规则移到了启动时
   * （`EC_ADVERTISE` 填 IP 主控拒绝启动）。
   *
   * （它此前是可写的，而那是个假字段：存进库、读出来，**而拼安装命令用的是
   * `EC_ADVERTISE`** —— 人改那一栏，节点的连接地址一个字没变。）
   */
  master_endpoint: string
  /** 恒为 true。写出来是为了界面不必靠约定去猜它能不能编辑。 */
  master_endpoint_readonly: boolean
  heartbeat_interval_s: number
  offline_threshold_count: number
  auto_drop_dns: boolean
  dns_provider: DnsProviderWire
  /**
   * 每个 `kind` × `credential_mode` **还要人填哪些字段**（契约 §11）。
   *
   * 键名与 `PUT` 的 `dns_provider` body 键一模一样 —— 界面拿它当**找输入框的
   * 钥匙**（每个输入框上标着 `data-field="<键名>"`，中间没有映射表）。
   * **不含 `kind`**：那是选择器本身，不是要填的字段。
   *
   * 它由后端的 `store.MissingFields` 对一份空配置**求值得出**，不是抄本，
   * 所以不可能和校验分叉。
   *
   * ## 它不是用来标红星的
   *
   * 星号解决的是「人不知道要填」，而 2026-08-23 撞上的是**人填不了**：
   * `account_id` 被误关在 `global_key` 分支里，`api_token` 模式下那个框根本
   * 不渲染。红星标在一个不存在的框上等于没标。
   *
   * 它真正的用途是那条 e2e：**这里列出的每个字段，在对应的 kind × mode 下
   * 界面上都得有一个能填的框**。前端的 `v-if` 从此不再是第二份知识 ——
   * 它仍然由前端写（标签、占位符、提示是界面的知识），
   * 而它与后端那份判据的一致性有东西守着。
   */
  dns_provider_requirements: Record<string, Record<string, string[]>>
  ops_bot_token_configured: boolean
}

export type NotifyLevel = 'all' | 'warn' | 'crit'

export interface AlertsWire {
  /** 渠道**共用**这一个级别。 */
  notify_level: NotifyLevel
  webhook: { url_configured: boolean }
  lark: { webhook_configured: boolean; at_all_on_crit: boolean }
}

export interface AlertTestWire {
  sent: boolean
  detail: string
}
