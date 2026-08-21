import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useConfigStore } from './config'
import { ApiError } from '@/api/http'
import type { RuleWire } from '@/api/types'

const putMock = vi.fn()
const getMock = vi.fn()
vi.mock('@/api/http', async (importOriginal) => ({
  // 只替换 http，保留真的 ApiError / errorText —— 后者正是被测行为的一部分
  ...(await importOriginal<typeof import('@/api/http')>()),
  http: {
    get: (...a: unknown[]) => getMock(...a),
    post: vi.fn(),
    put: (...a: unknown[]) => putMock(...a),
    del: vi.fn(),
  },
}))

const RULE: RuleWire = {
  id: 'svc-key-1',
  name: '结算系统签名',
  type: 'service_secret',
  enabled: true,
  apply_to: ['api.example.com'],
  version: 3,
  spec: {
    header: 'X-Service-Key',
    algo: 'hmac-sha256',
    ttl_s: 300,
    secret_configured: true,
  },
}

describe('setRuleSecret', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    putMock.mockReset().mockResolvedValue({ id: 'svc-key-1' })
    // 密钥保存后会 fetchAll 刷新；这里让它安静地失败，不影响被测行为
    getMock.mockReset().mockRejectedValue(new Error('不测这个'))
  })

  it('走 PUT /rules/:id 的顶层 secret，而不是塞进 spec', async () => {
    const store = useConfigStore()
    store.rules = [RULE]
    await store.setRuleSecret('svc-key-1', 'new-shared-key')

    const [url, body] = putMock.mock.calls[0]!
    expect(url).toBe('/rules/svc-key-1')
    const b = body as Record<string, unknown>
    expect(b.secret).toBe('new-shared-key')
    // spec 里出现密钥就等于被 GET /rules 回显 —— 后端把它挪到顶层正是为了躲开这个
    expect(JSON.stringify(b.spec)).not.toContain('new-shared-key')
  })

  /*
   * 这条是这组里最要紧的一条。
   *
   * PUT /rules/:id 是**直写**，不走下发流水线。如果提交的是 effective
   * （live 叠加草稿），那么「保存一个密钥」会把这条规则上所有未下发的改动
   * 一并偷偷生效——人以为自己只动了密钥。
   */
  it('提交 live，不把未下发的草稿一起带出去', async () => {
    const store = useConfigStore()
    store.rules = [RULE]
    store.patches = {
      'rule:svc-key-1': { enabled: false, spec: { ttl_s: 9999 } },
    }

    // 前提：草稿确实盖住了 live，否则这条测试什么也没验到
    expect(store.effective('rule:svc-key-1')?.enabled).toBe(false)

    await store.setRuleSecret('svc-key-1', 'k')
    const body = putMock.mock.calls[0]![1] as RuleWire
    expect(body.enabled).toBe(true)
    expect((body.spec as { ttl_s: number }).ttl_s).toBe(300)
  })

  it('规则不存在时不发请求', async () => {
    const store = useConfigStore()
    store.rules = []
    await expect(store.setRuleSecret('nope', 'k')).rejects.toThrow()
    expect(putMock).not.toHaveBeenCalled()
  })
})


/*
 * 草稿写回失败必须被记下来。
 *
 * 这里原先写着「下一次输入会重试，点下发前还会整体重取一次」—— **后半句是假的**：
 * runPreview 拿的是本地的 dirtyKeys，后端用**它自己那份**草稿渲染。写没成功的话，
 * 顶栏照样显示「N 处未下发改动」，而下发出去的配置里根本没有那处改动。
 *
 * 一句自信的假理由比没有理由更糟：没有理由的地方人会去查，写着理由的地方人会放心。
 */
describe('草稿没写到主控上时不能装作写成了', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    getMock.mockReset().mockRejectedValue(new Error('不测刷新'))
  })

  it('写失败时按 key 记下原因', async () => {
    putMock.mockReset().mockRejectedValue(new ApiError(503, '主控暂时不可用'))
    const store = useConfigStore()
    store.routes = [{ domain: 'a.example.com', upstream: '10.0.0.1:80' } as never]
    store.setField('route:a.example.com', 'upstream', '10.0.0.2:80')
    await store.flush()
    expect(store.unsaved['route:a.example.com']).toContain('主控暂时不可用')
  })

  it('写成功后把那条记录清掉 —— 否则横幅会一直挂着', async () => {
    putMock.mockReset().mockRejectedValueOnce(new ApiError(503, '主控暂时不可用'))
    const store = useConfigStore()
    store.routes = [{ domain: 'a.example.com', upstream: '10.0.0.1:80' } as never]
    store.setField('route:a.example.com', 'upstream', '10.0.0.2:80')
    await store.flush()
    expect(store.unsaved['route:a.example.com']).toBeTruthy()

    putMock.mockResolvedValue(null)
    store.setField('route:a.example.com', 'upstream', '10.0.0.3:80')
    await store.flush()
    expect(store.unsaved['route:a.example.com']).toBeUndefined()
  })
})


/*
 * 资源树上的「新」和工作台标题栏的「尚未下发到任何节点」说的是**同一个事实**：
 * `version === 0`。
 *
 * 它们此前有两处不同答案 —— 树里全局策略那一支写死了 `isNew: false`，而标题栏
 * 照 version 算。于是一条从没下发过的策略在树上没有「新」、点进去却说「尚未
 * 下发到任何节点」。**人会以为树上没「新」就是已经下发过了。**
 *
 * 契约 §6 现在写清了 version 只在**一次成功的下发**里 +1，改草稿不动它 ——
 * 所以 0 的含义是确定的：这条资源从来没被下发过，三类资源一视同仁。
 */
describe('「新」的判据三类资源一致', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('从没下发过的策略在树上也标「新」', () => {
    const store = useConfigStore()
    store.policies = [
      { id: 'tls', name: 'TLS 策略', version: 0, spec: {} },
      { id: 'log', name: '日志与限流', version: 3, spec: {} },
    ] as never
    const tls = store.tree.find((t) => t.key === 'global:tls')!
    const log = store.tree.find((t) => t.key === 'global:log')!
    expect(tls.isNew, 'version 0 的策略应当标新').toBe(true)
    expect(log.isNew, '下发过的不该标').toBe(false)
  })

  it('路由与规则用的是同一条判据', () => {
    const store = useConfigStore()
    store.routes = [{ domain: 'a.example.com', version: 0 }] as never
    store.rules = [{ id: 'r1', name: 'R', type: 'ip_whitelist', version: 2, spec: {}, apply_to: [] }] as never
    expect(store.tree.find((t) => t.key === 'route:a.example.com')!.isNew).toBe(true)
    expect(store.tree.find((t) => t.key === 'rule:r1')!.isNew).toBe(false)
  })
})
