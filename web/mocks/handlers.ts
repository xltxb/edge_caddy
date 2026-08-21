import { http, HttpResponse } from 'msw'
import * as seed from './seed'

const BASE = '/api/v1'

const ok = <T>(data: T) => HttpResponse.json({ code: 0, data, msg: '' })
/** 业务失败一律 HTTP 200 + code != 0（契约 §0.2）。 */
const fail = (code: number, msg: string) => HttpResponse.json({ code, data: null, msg })
/** 游标分页信封（契约 §0.5）。fixture 量小，一页到底。 */
const paged = <T>(items: T[]) => ok({ items, next_before_id: null })

/** 会话状态。MSW 是进程内的，够 dev 与 e2e 用。 */
let loggedIn = true

export const handlers = [
  /* ── 1. 会话 ── */
  http.get(`${BASE}/auth/session`, () =>
    loggedIn
      ? ok({ username: 'abiu', kind: 'human' })
      : // 这个端点的 401 是「还没登录」这个正常结果，前端在此旁路全局跳转
        HttpResponse.json({ code: 0, data: null, msg: '' }, { status: 401 }),
  ),

  http.post(`${BASE}/auth/login`, async ({ request }) => {
    const body = (await request.json()) as { username?: string; password?: string }
    if (body.username === 'abiu' && body.password === 'edge') {
      loggedIn = true
      return ok({ username: 'abiu', kind: 'human' })
    }
    return fail(1001, '用户名或密码错误')
  }),

  http.post(`${BASE}/auth/logout`, () => {
    loggedIn = false
    return ok(null)
  }),

  /* ── 3. 总览 ── */
  http.get(`${BASE}/overview`, () =>
    ok({ baseline: seed.BASELINE, kpi: seed.kpi(), events: seed.events }),
  ),

  /*
   * 节点也在 Node 侧：dns / drain 会改它、心跳帧要读它、push 要推 WS 进度。
   * 见 mocks/node-mock.ts。
   */

  /*
   * 配置资源、草稿、下发**都不在这里** —— 它们是一簇会被下发改到的可变状态，
   * 统一由 Vite 插件在 Node 侧处理（见 mocks/config-mock.ts 的说明）。
   * 放在 MSW 里的话，浏览器与 Node 各持一份副本，下发之后就对不上了。
   */


  /* ── 7. 下发 ── */
  http.get(`${BASE}/deploys`, () => paged(seed.deploys)),

  // 注意：下发相关的端点（preview / 创建 / 单次详情）**都不在这里**，
  // 由 Vite 插件在 Node 侧处理 —— 进度要经 WS 推回来，而 MSW 活在浏览器里
  // 且先于网络拦截，放在这里的话请求永远到不了 ws 服务端。

  /* DNS 权重在 Node 侧：share 要按节点的 dns_enabled 实时归一化。 */

  /* ── 9. 证书 ── */
  http.get(`${BASE}/certs`, () => paged(seed.certs)),
  // 续期是异步的：立即返回「已受理」，真实结果经 WS event 帧回报（契约 §9）
  http.post(`${BASE}/certs/:domain/renew`, ({ params }) =>
    ok({ domain: decodeURIComponent(String(params.domain)), accepted: true }),
  ),
  http.post(`${BASE}/certs/renew-check`, () => ok({ accepted: true })),

  /* ── 10. 审计 ── */
  http.get(`${BASE}/audit`, ({ request }) => {
    const operator = new URL(request.url).searchParams.get('operator')
    const rows =
      !operator || operator === 'all'
        ? seed.audit
        : seed.audit.filter((a) => a.operator === operator)
    return paged(rows)
  }),

  /* ── 11. 设置与告警 ── */
  http.get(`${BASE}/settings`, () => ok(seed.settings)),
  http.put(`${BASE}/settings`, async ({ request }) => {
    const b = (await request.json()) as Record<string, unknown>
    // master_endpoint 必须是域名不是 IP（契约 §11）
    const ep = String(b.master_endpoint ?? '')
    if (/^\d{1,3}(\.\d{1,3}){3}(:\d+)?$/.test(ep)) {
      return fail(1001, '主控接入地址必须是域名，不能是 IP')
    }
    Object.assign(seed.settings, b)
    // 凭证只写入不回显：带了就是替换（标记为已配置），不带就是保持不变
    if (b.dns_credential) {
      seed.settings.dns_provider = { ...seed.settings.dns_provider, configured: true }
    }
    delete (seed.settings as Record<string, unknown>).dns_credential
    return ok(seed.settings)
  }),

  http.get(`${BASE}/alerts`, () => ok(seed.alerts)),
  http.put(`${BASE}/alerts`, async ({ request }) => {
    const b = (await request.json()) as Record<string, unknown>
    Object.assign(seed.alerts, b)
    return ok(seed.alerts)
  }),
  http.post(`${BASE}/alerts/test`, async ({ request }) => {
    const b = (await request.json()) as { channel?: string }
    if (b.channel === 'lark' && !seed.alerts.lark.webhook_configured) {
      // 下游失败带上服务商原文错误 —— 那是排查 webhook 配错的唯一线索
      return fail(3001, 'Lark 返回 19021: bot not enabled in this chat')
    }
    return ok({ sent: true, detail: '卡片已投递' })
  }),
]
