import { describe, expect, it } from 'vitest'
import { ApiError, TransportError, ValidationFailed, errorText, http } from './http'

/*
 * 这组测的是一件事：**错误里最具体的那一句必须走到人眼前**。
 *
 * 1002 的 msg 永远是笼统的「未通过校验」，真正说清怎么回事的是 errors[].reason。
 * catch 里只取 e.message 的话，人看到「未通过校验」却不知道去哪儿改 —— 那和一条
 * field 指不到任何字段的错误是同一个毛病。
 */
describe('errorText', () => {
  it('1002 取 reason，不取笼统的 msg', () => {
    const e = new ValidationFailed('访问规则未通过校验', [
      { res_key: 'rule:svc-key-1', field: 'secret', reason: '尚未设置共享密钥' },
    ])
    expect(errorText(e, '保存失败')).toBe('尚未设置共享密钥')
  })

  it('多条 reason 全给出来 —— 只报第一条会让人改一次再撞一次', () => {
    const e = new ValidationFailed('未通过校验', [
      { res_key: 'route:a', field: 'upstream', reason: '回源地址必须形如 host:port' },
      { res_key: 'route:a', field: 'body_max', reason: '不是合法的大小' },
    ])
    expect(errorText(e)).toBe('回源地址必须形如 host:port；不是合法的大小')
  })

  it('errors 为空的 1002 退回 msg，不显示空白', () => {
    expect(errorText(new ValidationFailed('未通过校验', []), '保存失败')).toBe('未通过校验')
  })

  it('普通 ApiError 用它自己的 msg', () => {
    expect(errorText(new ApiError(404, '没有这条路由'), '加载失败')).toBe('没有这条路由')
  })

  it('传输层失败保留原文 —— 那是「断网了」和「后端拒绝」的唯一区别', () => {
    expect(errorText(new TransportError('主控暂时不可达'), '加载失败')).toBe('主控暂时不可达')
  })

  it('不是 Error 的东西用兜底', () => {
    expect(errorText('随便什么', '加载失败')).toBe('加载失败')
    expect(errorText(undefined, '加载失败')).toBe('加载失败')
  })

  it('message 是空串时也用兜底，不留一片空白', () => {
    expect(errorText(new Error(''), '加载失败')).toBe('加载失败')
  })
})

/*
 * 这一组测的是**那个 bug 的结论**：`!res.ok` 必须抛。
 *
 * 它的**前提**是关于世界的 —— 契约 §0.2 说 HTTP 状态码与 code 不重复表达同一件
 * 事，所以真主控在 404 / 500 时包裹体里的 code **仍然是 0**。前提在这里验不了
 * （fetch 是我自己造的），它由 `scripts/check-premises.mjs` 去碰真主控。
 *
 * 分开写是因为两者会各自失效：我这段代码可能被改回只判 code（结论没了），
 * 后端也可能哪天改成 404 带非零 code（前提没了）。只测一边，另一边照样能塌。
 */
describe('HTTP 不 ok 时必须抛，哪怕 code 是 0', () => {
  const withFetch = async (status: number, body: unknown, fn: () => Promise<unknown>) => {
    const orig = globalThis.fetch
    globalThis.fetch = (async () =>
      new Response(JSON.stringify(body), {
        status,
        headers: { 'Content-Type': 'application/json' },
      })) as typeof fetch
    try {
      return await fn()
    } finally {
      globalThis.fetch = orig
    }
  }

  it('404 且 code 为 0 —— 不能返回 null 走进成功分支', async () => {
    await withFetch(404, { code: 0, data: null, msg: '没有这条路由' }, async () => {
      await expect(http.get('/routes/nope')).rejects.toThrow('没有这条路由')
    })
  })

  it('500 且 code 为 0 —— 同样要抛', async () => {
    await withFetch(500, { code: 0, data: null, msg: '' }, async () => {
      await expect(http.get('/nodes')).rejects.toThrow(/HTTP 500/)
    })
  })

  // 反向自检：这组测试若因为 fetch 没被替换而空转，这一条会露馅
  it('200 且 code 为 0 正常返回 —— 证明上面两条不是因为一律抛才绿的', async () => {
    await withFetch(200, { code: 0, data: { ok: 1 }, msg: '' }, async () => {
      await expect(http.get('/nodes')).resolves.toEqual({ ok: 1 })
    })
  })
})
