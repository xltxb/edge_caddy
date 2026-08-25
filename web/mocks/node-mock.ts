import type { IncomingMessage, ServerResponse } from 'node:http'
import type { LogLevel, NodeWire } from '../src/api/types'
import * as seed from './seed'

/**
 * 节点状态的 mock —— 同样在 **Node 侧**。
 *
 * 判断依据和 config-mock 一样：节点状态会被 `dns` / `drain` 改写、被心跳帧读、
 * `push` 还要经 WS 推进度。三者跨运行时就会各持一份副本。
 */

type Rec = Record<string, unknown>

/** 各线路的权重配置。weight 是配置值，share 由 dns_enabled 实时算出来。 */
const LINE_NAMES: Record<string, string> = {
  ct: '电信',
  cu: '联通',
  cm: '移动',
  tw: '台湾',
  ov: '境外 / 默认',
}

function freshNodes() {
  return {
    nodes: seed.nodes.map((n) => ({ ...n, cpu_series: n.cpu_series ? [...n.cpu_series] : null })),
    logs: Object.fromEntries(
      Object.entries(seed.agentLogs).map(([k, v]) => [k, v.map((l) => ({ ...l }))]),
    ) as Record<string, { at: string; level: LogLevel; msg: string }[]>,
    /** 节点上 Caddy Admin 的可达性。与隧道可达性分开 —— 两种故障处置不同。 */
    caddyAdmin: Object.fromEntries(seed.nodes.map((n) => [n.id, n.status !== 'down'])) as Record<
      string,
      boolean
    >,
    weights: Object.fromEntries(
      seed.dnsLines.map((l) => [l.line_code, { ...l.weights }]),
    ) as Record<string, Record<string, number>>,
  }
}

export const nodeState = freshNodes()

/**
 * **e2e 用来造同步场景的开关。** 与 `__test/reset` 同类：不在契约里，
 * 真主控上不存在。
 *
 * 为什么需要它：`/dns/weights` 由这个 Node 侧插件提供，`page.route` 拦不到它
 * （实测拦截次数为 0）。而「三个域名坏一个」「targets 为 null 的旧数据」这两个
 * 形状**必须有人走** —— 界面上那段展开列表否则就是没人看过的代码。
 *
 * 夹具本身保持全成功：那是「成功时 detail 不能被丢掉」那条的前提。
 * **一个夹具不能同时是两种场景**，所以另一种由这里注入。
 */
let syncOverride: unknown = null

export function setSyncOverride(v: unknown): void {
  syncOverride = v
}

/** 复位到 seed —— 只给 e2e 用。 */
export function resetNodes(): void {
  Object.assign(nodeState, freshNodes())
  syncOverride = null
}

/**
 * 算出各线路的实际占比。
 *
 * 退出解析的节点 share 为 0，它的权重在该线路内的其余节点间**重新归一化** ——
 * 所以在命令面板 pause 一个节点，这一页的占比条会立刻重排。
 */
/**
 * 候选 = **配过权重的 ∪ 还没下线的节点**（契约 §8）。
 *
 * 这里原先只遍历 `nodeState.weights` —— 与真主控此前一模一样的闭环：节点得先在
 * 权重表里才会出现，而写那张表的唯一入口是这一页，于是**新接入的节点永远进不了
 * 解析**，而页面看上去完全正常，五条线路齐全、只是全空。
 *
 * mock 复刻了那个 bug，所以 dev 下也看不出来 —— 又一次「跟着一起错的替身」。
 *
 * 已下线的节点不进候选（给一台已退出的机器配权重没有意义），但它**配过权重就
 * 仍然出现**：那份配置是人写下的意图，重新上线之后还要用。
 */
function buildLines() {
  return Object.keys(nodeState.weights).map((code) => {
    const weights = nodeState.weights[code] ?? {}
    const candidates = new Set([
      ...Object.keys(weights),
      ...nodeState.nodes.filter((n) => !n.drained_at).map((n) => n.id),
    ])

    const total = [...candidates]
      .filter((id) => nodeState.nodes.find((x) => x.id === id)?.dns_enabled === true)
      .reduce((s, id) => s + (weights[id] ?? 0), 0)

    return {
      code,
      name: LINE_NAMES[code] ?? code,
      entries: [...candidates].map((id) => {
        const n = nodeState.nodes.find((x) => x.id === id)
        const on = n?.dns_enabled === true
        const weight = weights[id] ?? 0
        return {
          node: id,
          weight,
          share: on && total > 0 ? Math.round((weight / total) * 1000) / 10 : 0,
          dns_enabled: on,
          status: n?.status ?? 'down',
          /*
           * **`in_rotation` 是后端算的**（`dns_enabled && status != down &&
           * weight > 0`）—— mock 这边照它复现一份，而那正是前端**不该**做的事：
           * 界面直接用这个字段，判据只在后端一处。
           *
           * mock 复现是没办法的事（替身要造出这个值），但它跟前端的区别是：
           * 这一份错了，`check:shapes` 之外还有 e2e 会撞到；
           * 而前端自己推一份，两边各自都对、症状是界面说「在解析里」
           * 而服务商上没有它。
           */
          in_rotation: on && n?.status !== 'down' && weight > 0,
          /*
           * **有没有人给它配过权重** —— 这是**按节点**的，不按线路。
           *
           * 第一版写的是 `id in weights`，而 `weights` 是这一条线路的表 ——
           * 于是一台在「中国」线有权重、在「台湾」线没有的机器，
           * 在台湾那一行上被标成「新接入」。**界面上 11 处标记，
           * 而只有一台是真的新接入。**
           *
           * 后端那句是「保存解析页时页面上每个节点都会被写行」——
           * 页面上是每条线 × 每个节点，所以人存过一次之后，
           * 一台节点在所有线路上都有行。**「某条线上没有行」在真实数据里
           * 不会单独出现**，它只是我这份替身按线路切分造出来的假象。
           *
           * 节点上那个 `weight_set`（`GET /nodes`）必然也是按节点的 ——
           * 那一层根本没有线路维度。两处同源。
           */
          weight_set: nodeState.nodes.find((x) => x.id === id)?.weight_set !== false,
        }
      }),
    }
  })
}

function log(nodeId: string, level: LogLevel, msg: string): void {
  const l = nodeState.logs[nodeId]
  if (l) l.unshift({ at: new Date().toISOString(), level, msg })
}

const json = (res: ServerResponse, body: unknown) => {
  res.statusCode = 200
  res.setHeader('Content-Type', 'application/json')
  res.end(JSON.stringify(body))
}
const ok = (res: ServerResponse, data: unknown) => json(res, { code: 0, data, msg: '' })
const failCode = (res: ServerResponse, code: number, msg: string) =>
  json(res, { code, data: null, msg })
const paged = (res: ServerResponse, items: unknown[]) => ok(res, { items, next_before_id: null })

async function readBody(req: IncomingMessage): Promise<Rec> {
  const chunks: Buffer[] = []
  for await (const c of req) chunks.push(c as Buffer)
  const raw = Buffer.concat(chunks).toString('utf8')
  return raw ? (JSON.parse(raw) as Rec) : {}
}

export interface NodeMockDeps {
  send: (frame: unknown) => void
  baseline: () => string
}

function pushEvent(deps: NodeMockDeps, node: string | null, kind: string, msg: string): void {
  deps.send({
    type: 'event',
    data: { id: Math.floor(Math.random() * 1e6), at: new Date().toISOString(), node, kind, msg },
  })
}

/** 返回 true 表示已处理。 */
/**
 * mock 里当作同步过一次。
 *
 * 真主控在从没同步过时给 `null`（契约 §0.4）。mock 这边给真实时刻，
 * 「没有时刻」那条路径在单测里覆盖，不靠 mock。
 */
/**
 * `dns_sync.detail` **在成功时也说实际发生了什么**（契约 §8）。
 *
 * 「成功」两个字信息量为零。真后端在这里说的是：记录写到了哪个名字、
 * 这次按什么口径推的 —— 而那个名字是唯一能揭穿「`domain` + `sub` 拼出
 * `cdn.cdn.example.com`」的东西：推送成功、接口 200、`ok` 是 true，
 * 而人在服务商面板上永远找不到它。
 *
 * mock 跟着 kind 走，否则 dev 下永远看不到纯 DNS 那一档的说法 ——
 * 而那一档正是「库里五条不一致」的常态。
 */
interface SyncShape {
  ok: boolean
  at: string
  detail: string
  targets: { hostname: string; ok: boolean; detail: string }[] | null
}

function dnsSync(): SyncShape {
  if (syncOverride) return syncOverride as SyncShape
  const kind = settingsKind()
  const name = 'cdn.example.com'
  if (kind === 'cloudflare_dns') {
    return {
      ok: true,
      at: new Date().toISOString(),
      detail:
        `解析安排已同步到服务商（写入 ${name}）。库里五条线路的配置并不一致` +
        '（多半是之前用别的服务商时配的）；普通 DNS 记录分不出线路，' +
        '这次按各线路节点的并集推送',
      targets: (seed.settings.dns_provider.targets ?? []).map((t) => ({
        hostname: t.sub ? `${t.sub}.${t.domain}` : t.domain,
        ok: true,
        detail: '已同步',
      })),
    }
  }
  /*
   * **一个坏的形状**：两个成功一个失败。`ok` 是「全都成功」，所以它是 false ——
   * 而**另外两个是好的这件事，只有 targets 说得出来**。
   *
   * 夹具造成坏的而不是全绿：全绿的话「展开列出哪一个坏了」那一支在 dev 下
   * 永远走不到，界面上那段就是没人看过的代码。
   */
  const hosts = (seed.settings.dns_provider.targets ?? []).map((t) =>
    t.sub ? `${t.sub}.${t.domain}` : t.domain,
  )
  /*
   * **夹具是全成功的**，「三个里坏一个」那个形状由 e2e 用路由拦截自己造
   * （`tests/e2e/dns.spec.ts`）。
   *
   * 这里造成坏的话，另一条守「成功时那句 detail 不能被丢掉」的 e2e 就没有
   * 夹具了 —— **一个夹具不能同时是两种场景**，而两个场景都得有人走。
   */
  const targets = hosts.map((h) => ({ hostname: h, ok: true, detail: '已同步' }))
  const bad = targets.filter((t) => !t.ok)
  return {
    ok: bad.length === 0,
    at: new Date().toISOString(),
    detail: bad.length
      ? `${targets.length} 个域名里 ${targets.length - bad.length} 个同步成功；` +
        `失败的：${bad.map((t) => t.hostname).join('、')}`
      : `解析安排已同步到服务商（写入 ${name}）`,
    targets,
  }
}

/**
 * 各服务商能表达什么（契约 §8）。**covers 由服务商给** —— 前端不持有任何
 * 服务商的地理模型，加第四家时前端不用改。
 */
const CAPS: Record<string, unknown> = {
  dnspod: {
    kind: 'dnspod',
    lines: [
      { code: 'ct', name: '电信', covers: ['ct'] },
      { code: 'cu', name: '联通', covers: ['cu'] },
      { code: 'cm', name: '移动', covers: ['cm'] },
      { code: 'tw', name: '台湾', covers: ['tw'] },
      { code: 'ov', name: '境外 / 默认', covers: ['ov'] },
    ],
    weights: true,
    notes: 'DNSPod 原生支持线路与权重；线路免费，权重需要付费套餐。',
  },
  cloudflare: {
    kind: 'cloudflare',
    lines: [
      { code: 'cn', name: '中国（电信 / 联通 / 移动合并）', covers: ['ct', 'cu', 'cm'] },
      { code: 'tw', name: '台湾', covers: ['tw'] },
      { code: 'ov', name: '境外 / 默认', covers: ['ov'] },
    ],
    weights: true,
    notes:
      'Cloudflare 的 DNS 记录没有权重与线路概念，加权调度走 Load Balancing，' +
      '其地理维度是国家 / 大洲：电信 / 联通 / 移动无法区分，三者会被合并为「中国」。',
  },
  /*
   * **只有进出轮换**：普通 A / AAAA 记录，分不了线路也表达不了权重。
   * 五条线合成一条 `all`，于是「五条线配得不一样」在界面上造不出来；
   * `weights: false` 让权重输入框换成一句说明。
   */
  cloudflare_dns: {
    kind: 'cloudflare_dns',
    lines: [{ code: 'all', name: '全部（不分线路）', covers: ['ct', 'cu', 'cm', 'tw', 'ov'] }],
    weights: false,
    notes:
      '普通 A / AAAA 记录轮换，免费。分不了线路、也表达不了权重 —— ' +
      '各节点只有「在或不在解析里」，进出等价。',
  },
}

/** 当前配的是哪家 —— 设置页改了 kind，这一页的能力要跟着变。 */
function settingsKind(): string {
  return String((seed.settings as Record<string, unknown>).dns_provider &&
    (seed.settings.dns_provider as { kind?: string }).kind || 'cloudflare')
}

/**
 * 两个字符串是不是**同一个 IP**。
 *
 * 不能直接比字符串：`2001:db8::1` 和 `2001:0db8:0:0:0:0:0:1` 是同一个地址而
 * 写法不同。真后端比的是 `net.IP.Equal`，mock 这边借浏览器/Node 都有的 URL
 * 解析器做同一件事 —— 它会把 IPv6 归一到压缩小写形式。
 *
 * 解析不了就退回字符串比：那种输入已经被 `isIP` 挡在外面了，走到这里说明
 * 两边都是合法 IP，退化路径只是不让它抛。
 */
function sameIP(a: string, b: string): boolean {
  if (a === b) return true
  try {
    return new URL(`http://[${a}]`).hostname === new URL(`http://[${b}]`).hostname
  } catch {
    return false
  }
}

/** 合法 IPv4 / IPv6。写错的值不会在这里出事，会在下一次同步解析时出事。 */
function isIP(v: string): boolean {
  if (/^(\d{1,3}\.){3}\d{1,3}$/.test(v)) {
    return v.split('.').every((p) => Number(p) <= 255 && String(Number(p)) === p)
  }
  try {
    return new URL(`http://[${v}]`).hostname.length > 0
  } catch {
    return false
  }
}

export async function handleNodes(
  req: IncomingMessage,
  res: ServerResponse,
  deps: NodeMockDeps,
): Promise<boolean> {
  const path = (req.url ?? '').split('?')[0] ?? ''
  const m = req.method ?? 'GET'

  if (m === 'GET' && path === '/api/v1/nodes') {
    const baseline = deps.baseline()
    return (
      ok(res, {
        items: nodeState.nodes.map((n) => ({ ...n, drift: n.cfg_version !== baseline })),
        next_before_id: null,
        baseline,
        dns_sync: dnsSync(),
      }),
      true
    )
  }

  if (m === 'POST' && path === '/api/v1/nodes/token') {
    const b = await readBody(req)
    const token = `ec_${Math.random().toString(16).slice(2, 10)}`
    return (
      ok(res, {
        token,
        expires_at: new Date(Date.now() + 30 * 60_000).toISOString(),
        install_cmd: `curl -fsSL https://ec.internal/install.sh | sudo bash -s -- --token ${token} --master ec.internal:9000 --node-id ${String(b.node_id ?? '')}`,
      }),
      true
    )
  }

  const one = /^\/api\/v1\/nodes\/([^/]+)$/.exec(path)

  if (m === 'PUT' && one) {
    const id = decodeURIComponent(one[1]!)
    const node = nodeState.nodes.find((n) => n.id === id)
    if (!node) return failCode(res, 1003, '找不到这个节点'), true

    const b = await readBody(req)

    /*
     * **严格绑定要在 mock 里也严格**，否则 dev 下发得出去、线上被 1001 拒。
     * 后端的报错会点名那个字段，这里照做 —— 一条说不清是哪个字段的 1001，
     * 等于没有这条错误。
     */
    const forbidden = ['node_id', 'status', 'dns_enabled', 'drained_at', 'online'].filter(
      (k) => k in b,
    )
    if (forbidden.length) {
      return failCode(res, 1001, `这些字段不能改：${forbidden.join('、')}`), true
    }

    const fields = ['city', 'vendor', 'line', 'public_ip'] as const
    const missing = fields.filter((k) => typeof b[k] !== 'string' || !(b[k] as string).trim())
    if (missing.length) {
      return failCode(res, 1002, `${missing.join('、')} 不能为空`), true
    }

    const nextIP = (b.public_ip as string).trim()
    if (!isIP(nextIP)) {
      return failCode(res, 1002, `${nextIP} 不是合法的 IP 地址`), true
    }

    /*
     * **按 IP 的值比，不按字符串比。** `2001:db8::1` 和 `2001:0db8:0:0:0:0:0:1`
     * 是同一个地址而字符串不同 —— 按字符串比会回报一句「203.0.113.7 →
     * 203.0.113.7，解析已同步」这样字面为真、读起来是假话的 detail。
     * 真后端用 net.IP.Equal，mock 跟上，否则这个坑只在线上出现。
     */
    const ipChanged = !sameIP(node.public_ip, nextIP)
    const prevIP = node.public_ip

    node.city = (b.city as string).trim()
    node.vendor = (b.vendor as string).trim()
    node.line = (b.line as string).trim()
    node.public_ip = nextIP

    log(id, 'info', `metadata updated by operator`)
    pushEvent(deps, id, 'ok', `${id} 的信息已修改`)

    return (
      ok(res, {
        id,
        city: node.city,
        vendor: node.vendor,
        line: node.line,
        public_ip: node.public_ip,
        dns_synced: ipChanged,
        // 只改城市/机房/线路时是空串 —— **那不是失败**，是这次改动跟解析无关
        detail: ipChanged ? `公网 IP 已改（${prevIP} → ${nextIP}），解析已同步到服务商` : '',
      }),
      true
    )
  }

  if (m === 'DELETE' && one) {
    const id = decodeURIComponent(one[1]!)
    const node = nodeState.nodes.find((n) => n.id === id)
    if (!node) return failCode(res, 1003, '找不到这个节点'), true

    /*
     * **必须先下线。** 不是礼节性确认：还连着的机器手里有隧道证书，删掉记录
     * 之后它会重连、被按证书认出来、然后在一张不存在的行上写心跳（UPDATE 影响
     * 0 行，不报错）—— 一台连着、在服务、而控制台上看不见的机器。
     *
     * **2001 状态冲突**，与「对已下线节点开解析或签 Token」同一类。
     *
     * 这里一度写的是 3002：契约的端点小节里那个数字是错的（§0.3 的码表说
     * 3002 是「节点不可达」，而这里拒绝的理由恰恰是那台机器**还连着** ——
     * 两句话正好说反），而真后端的 `handleDeleteNode` 从头到尾返回的都是
     * `CodeStateConflict`。我照契约复刻了 mock，于是**替身错得和文档一样，
     * 而两边都跟实现对不上**。
     *
     * 接住它的不是任何测试：后端的 e2e 断言 `code != CodeOK`，只验「被拒了」，
     * 不验「以什么理由被拒」—— 而前者在任何一种失败下都成立，包括理由完全
     * 错了的那些。**「被拒了」不是断言，「以什么理由被拒」才是。**
     *
     * **这个数字前端这边没有东西守着，而且不该有。**
     *
     * 想过用 `check-premises`（它专门去问真主控，还有「预期被拒，不会入库」
     * 那个模式）。但验「未下线不让删」的唯一办法，是真的去删一台未下线的节点
     * —— 而**那道检查一旦失效，代价正是这条检查本身要防的事故**：一台还在跑的
     * 机器记录没了，变成幽灵。check-premises 打的是真主控，风险不对称。
     *
     * 所以这里靠的是**替身跟着观测走**，不是跟着理解走。真后端的
     * `handleDeleteNode` 返回 `CodeStateConflict`，后端那边有测试钉住具体的码；
     * 这边照抄那个值和那句 msg。改动它之前先去看一眼后端返回的到底是什么。
     */
    if (!node.drained_at) {
      return (
        failCode(
          res,
          2001,
          '先「下线」这个节点再删除 —— 还连着的机器会带着隧道证书重连，' +
            '而它的记录已经没了，结果是一台连着却看不见的机器',
        ),
        true
      )
    }

    nodeState.nodes = nodeState.nodes.filter((n) => n.id !== id)
    delete nodeState.logs[id]
    delete nodeState.caddyAdmin[id]
    // 跟着删的：解析权重。它是「关于这台机器此刻的安排」，机器没了就没有意义。
    for (const line of Object.values(nodeState.weights)) delete line[id]

    pushEvent(deps, id, 'warn', `${id} 的记录已删除`)

    return (
      ok(res, {
        id,
        detail:
          '已删除记录。注意那台机器上的 Agent 与 Caddy 还在跑，要真正撤掉在那台机器上执行：sudo ./edge-node.sh uninstall',
      }),
      true
    )
  }

  const logs = /^\/api\/v1\/nodes\/([^/]+)\/logs$/.exec(path)
  if (m === 'GET' && logs) {
    return paged(res, nodeState.logs[decodeURIComponent(logs[1]!)] ?? []), true
  }

  const act = /^\/api\/v1\/nodes\/([^/]+)\/(push|dns|probe|drain|rejoin)$/.exec(path)
  if (m === 'POST' && act) {
    const id = decodeURIComponent(act[1]!)
    const node = nodeState.nodes.find((n) => n.id === id)
    if (!node) return failCode(res, 1003, '找不到这个节点'), true

    if (act[2] === 'push') {
      // 对已下线节点重推是状态冲突，不是参数错
      if (node.status === 'down') return failCode(res, 2001, '节点已下线，无法重推配置'), true
      log(id, 'info', `config ${deps.baseline()} received`)
      pushEvent(deps, id, 'ok', `已向 ${id} 重推基线 ${deps.baseline()}`)
      return ok(res, { deploy_id: 0, cfg_version: deps.baseline() }), true
    }

    if (act[2] === 'rejoin') {
      // 解析**不**跟着打开：能接入不等于该马上分流量，它刚回来，配置可能还是旧的
      node.drained_at = null
      log(id, 'info', 'rejoin allowed by operator')
      pushEvent(deps, id, 'ok', `${id} 已重新上线，解析仍是关闭的`)
      return (
        ok(res, {
          id,
          drained_at: null,
          dns_enabled: node.dns_enabled === true,
          detail: '已允许重新接入；解析仍是关闭的，确认配置无误后再打开',
        }),
        true
      )
    }

    if (act[2] === 'dns') {
      const b = await readBody(req)
      /*
       * 已下线的节点**两个方向都拒**（契约 §4）。
       *
       * 「关」原先是放行的 —— 而它会把 dns_reason 改写成 manual，drained_at 还在，
       * 于是节点页说「已下线」、DNS 页说「人手动关的」。mock 放行一个真后端会拒的
       * 动作，等于让开发时走得通、上线才撞墙。
       */
      if (node.drained_at) {
        return (
          failCode(
            res,
            2001,
            b.enabled === true
              ? '该节点已被下线，先「重新上线」再恢复解析'
              : '该节点已被下线，本来就不在解析里',
          ),
          true
        )
      }
      node.dns_enabled = b.enabled === true
      log(id, 'warn', `dns weight ${node.dns_enabled ? 'restored' : 'set to 0'}`)
      pushEvent(
        deps,
        id,
        node.dns_enabled ? 'ok' : 'warn',
        `${id} ${node.dns_enabled ? '已恢复解析' : '已暂停解析'}，其余节点权重已重新归一化`,
      )
      return (
        ok(res, {
          id,
          dns_enabled: node.dns_enabled,
          // mock 里当作已配好服务商 —— 但 detail 照样发，免得前端只在
          // 「没同步」那条路径上才拿得到文案，而那条路径 mock 里走不到。
          dns_synced: true,
          detail: '解析安排已同步到服务商',
        }),
        true
      )
    }

    if (act[2] === 'probe') {
      if (node.status === 'down') return failCode(res, 3002, '探活超时，节点不可达'), true
      const rtt = 20 + Math.floor(Math.random() * 60)
      log(id, 'info', `probe ok · rtt ${rtt}ms`)
      return (
        ok(res, {
          reachable: true,
          rtt_ms: rtt,
          caddy_admin: nodeState.caddyAdmin[id] ?? true,
          cfg_version: node.cfg_version,
        }),
        true
      )
    }

    // drain
    const b = await readBody(req)
    if (b.confirm !== true) return failCode(res, 1001, '下线操作必须显式确认'), true
    const before = Number(node.conns ?? 0)
    node.dns_enabled = false
    // **不动 status。** status 是观察（有没有心跳），drained_at 是意图。
    // mock 早先在这里写 status='down'，那正是被 ADR-0014 拆开的那个混淆 ——
    // 而且它会让「已下线且在线」这个真实存在的组合在 mock 里永远出不来。
    node.drained_at = new Date().toISOString()
    node.conns = 0
    log(id, 'warn', 'tunnel closed by operator')
    pushEvent(deps, id, 'warn', `${id} 已下线：解析摘除、连接排空、隧道关闭`)
    return (
      ok(res, {
        steps: [
          { step: 'dns_removed', ok: true, detail: '解析安排已同步到服务商' },
          {
            step: 'conns_drained',
            ok: true,
            // 这句边界不能省：DNS 有 TTL，「已排空」说的是回报那一刻的连接数
            detail: `已建立的 ${before} 条连接都已结束；解析缓存未过期前仍可能有新连接进来`,
          },
          { step: 'tunnel_closed', ok: true, detail: '隧道已断开，此后拒绝该节点重连' },
        ],
      }),
      true
    )
  }

  return false
}

/** DNS 权重。与节点状态同侧，因为 share 要按 dns_enabled 实时归一化。 */
export async function handleDns(req: IncomingMessage, res: ServerResponse): Promise<boolean> {
  const path = (req.url ?? '').split('?')[0] ?? ''
  if (path !== '/api/v1/dns/weights') return false

  if (req.method === 'GET') {
    // mock 里用 cloudflare，因为它是**能力更少**的那家 —— 拿能力多的那家开发，
    // 界面上那条限制就永远走不到。
    return (
      ok(res, {
        /*
         * **多域名时 `domain`（单数）是不全的**（契约 §11）—— 留着只为不立刻
         * 破坏旧界面。新界面读 `domains`。
         */
        domain: 'cdn.example.com',
        domains: (seed.settings.dns_provider.targets ?? []).map((t) =>
          t.sub ? `${t.sub}.${t.domain}` : t.domain,
        ),
        dns_sync: dnsSync(),
        lines: buildLines(),
        /*
         * **capabilities 跟着 kind 走。**
         *
         * 写死成 cloudflare 的话，`cloudflare_dns`（普通 A/AAAA 轮换）那一档在
         * dev 下永远看不到 —— 而它恰好是 `weights: false` 唯一的来源。
         * 那时界面上「轮换」那一支就是一段没人走过的代码。
         */
        capabilities: CAPS[settingsKind()] ?? CAPS.cloudflare,
      }),
      true
    )
  }

  if (req.method === 'PUT') {
    const b = (await readBody(req)) as { lines?: { code: string; entries: { node: string; weight: number }[] }[] }
    for (const l of b.lines ?? []) {
      const bucket = nodeState.weights[l.code]
      if (!bucket) continue
      for (const e of l.entries) bucket[e.node] = Math.max(0, Math.round(e.weight))
    }
    return ok(res, { lines: buildLines() }), true
  }
  return false
}

/** 供 ws-plugin 发心跳用 —— 与 REST 读的是同一份状态。 */
export function heartbeatNodes(): NodeWire[] {
  return nodeState.nodes
}
