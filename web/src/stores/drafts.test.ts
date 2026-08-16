import { describe, it, expect, beforeEach } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { useDraftsStore } from './drafts'
import type { Route } from '@/api/types'

const live: Route = {
  domain: 'api.example.com', upstream: '10.8.0.2:8080', block: 'abort',
  mtls: false, compress: true, body_max: '5MB',
  wl: ['203.0.113.7', '10.8.0.0/24'], ver: 7,
}

function newStore() {
  const s = useDraftsStore()
  s.__setFetchers({
    listRoutes: async () => [live],
    listDrafts: async () => [],
    putDraft: async () => {},
  })
  return s
}

describe('草稿语义', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('effective 是 live 叠加草稿的结果', async () => {
    const s = newStore()
    await s.load()
    expect(s.effective('route:api.example.com')?.upstream).toBe('10.8.0.2:8080')

    s.setField('route:api.example.com', 'upstream', '10.0.0.9:9090')
    expect(s.effective('route:api.example.com')?.upstream).toBe('10.0.0.9:9090')
    // 没改的字段仍取线上值
    expect(s.effective('route:api.example.com')?.body_max).toBe('5MB')
  })

  // 值改回与线上一致时必须把该键从草稿里删掉。
  //
  // 不删的话会留下一处**推不掉的幽灵改动**：资源树上的蓝点不消失，diff 是空的，
  // 用户反复点推送也去不掉——因为内容确实没变。设计稿的 sameVal 就是干这个的。
  it('改回原值时删掉该键，资源不再算脏', async () => {
    const s = newStore()
    await s.load()
    const key = 'route:api.example.com'

    s.setField(key, 'upstream', '10.0.0.9:9090')
    expect(s.isDirty(key)).toBe(true)
    expect(s.changeCount(key)).toBe(1)

    s.setField(key, 'upstream', '10.8.0.2:8080') // 改回去
    expect(s.isDirty(key)).toBe(false)
    expect(s.changeCount(key)).toBe(0)
    expect(s.dirtyKeys).toEqual([])
  })

  // 白名单要先规范化再比较：只增删空行、改缩进不算改动。
  //
  // 用户在文本框里敲回车是常态，把它算成一处待下发的改动很烦人，
  // 而且会让「有几处改动」这个数字失去意义。
  it('白名单按规范化后比较，空行与空白不算改动', async () => {
    const s = newStore()
    await s.load()
    const key = 'route:api.example.com'

    s.setField(key, 'wl', ['  203.0.113.7  ', '', '10.8.0.0/24', '   '])
    expect(s.isDirty(key)).toBe(false)

    s.setField(key, 'wl', ['203.0.113.7'])
    expect(s.isDirty(key)).toBe(true)
  })

  it('多个字段各自计数', async () => {
    const s = newStore()
    await s.load()
    const key = 'route:api.example.com'
    s.setField(key, 'upstream', '10.0.0.9:9090')
    s.setField(key, 'body_max', '64MB')
    expect(s.changeCount(key)).toBe(2)
    expect(s.totalChanges).toBe(2)
  })

  // 布尔字段改回原值同样要删键——false 与 undefined 在实现里很容易混淆。
  it('布尔字段改回原值也删键', async () => {
    const s = newStore()
    await s.load()
    const key = 'route:api.example.com'
    s.setField(key, 'compress', false)
    expect(s.isDirty(key)).toBe(true)
    s.setField(key, 'compress', true)
    expect(s.isDirty(key)).toBe(false)
  })
})
