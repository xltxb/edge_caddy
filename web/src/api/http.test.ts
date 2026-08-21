import { describe, expect, it } from 'vitest'
import { ApiError, TransportError, ValidationFailed, errorText } from './http'

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
