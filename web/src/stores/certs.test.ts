import { describe, it, expect, beforeEach } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { useCertsStore, type CertsResponse } from './certs'

const seed: CertsResponse = {
  certs: [
    { domain: 'a.example.com', not_after: '2026-08-20T00:00:00Z', days_left: 3, severity: 'crit',
      issuer: "Let's Encrypt", key_type: 'ECDSA', node_count: 6, has_stale: false, stale_nodes: [], oldest_age_sec: 12 },
    { domain: 'b.example.com', not_after: '2026-09-10T00:00:00Z', days_left: 25, severity: 'warn',
      issuer: "Let's Encrypt", key_type: 'ECDSA', node_count: 5, has_stale: true, stale_nodes: ['node-us-01'], oldest_age_sec: 90000 },
    { domain: 'c.example.com', not_after: '2026-11-01T00:00:00Z', days_left: 77, severity: 'ok',
      issuer: "Let's Encrypt", key_type: 'ECDSA', node_count: 6, has_stale: false, stale_nodes: [], oldest_age_sec: 8 },
  ],
  bands: { crit_below_days: 7, warn_below_days: 30 },
  stale_after_sec: 600,
}

describe('证书 store', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('载入后按紧急程度排序，档位来自后端', async () => {
    const s = useCertsStore()
    s.__setIO(async () => seed, async () => ({ domain: 'x', renewed: true }), async () => ({ results: [] }))
    await s.load()

    expect(s.certs.map((c) => c.domain)).toEqual(['a.example.com', 'b.example.com', 'c.example.com'])
    // 档位不在前端算：前端自己算的话，「什么算紧急」就有了两个定义
    expect(s.certs[0].severity).toBe('crit')
    expect(s.bands.crit_below_days).toBe(7)
  })

  // 「色 + 文字」双编码：只有颜色的话，色觉障碍的人看到的是三块一样的灰。
  it('每档都有对应的文字标签', () => {
    const s = useCertsStore()
    expect(s.label('crit')).not.toBe('')
    expect(s.label('warn')).not.toBe('')
    expect(s.label('ok')).not.toBe('')
    expect(new Set([s.label('crit'), s.label('warn'), s.label('ok')]).size).toBe(3)
  })

  // 陈旧要能看见，且说清楚是哪几台。
  it('陈旧数据标出来并给出年龄与节点', async () => {
    const s = useCertsStore()
    s.__setIO(async () => seed, async () => ({ domain: 'x', renewed: true }), async () => ({ results: [] }))
    await s.load()

    const b = s.certs.find((c) => c.domain === 'b.example.com')!
    expect(b.has_stale).toBe(true)
    expect(s.staleHint(b)).toContain('node-us-01')
    // 不假装是最新的：要说出「多久之前」
    expect(s.staleHint(b)).toMatch(/小时|天/)
    const a = s.certs.find((c) => c.domain === 'a.example.com')!
    expect(s.staleHint(a)).toBe('')
  })

  // 单张续期是异步操作，要有进行中状态。
  it('续期时置忙，完成后清掉', async () => {
    const s = useCertsStore()
    let release!: (v: unknown) => void
    s.__setIO(async () => seed, () => new Promise((r) => (release = r)), async () => ({ results: [] }))
    await s.load()

    const done = s.renew('a.example.com')
    expect(s.busy).toBe('a.example.com')
    release({ domain: 'a.example.com', renewed: true })
    await done
    expect(s.busy).toBe('')
  })

  // 续期失败的原因要留在界面上：「DNS 凭据无效」和「域名不在这个账号下」
  // 是两件事，处理方式完全不同。
  it('续期失败保留原因', async () => {
    const s = useCertsStore()
    s.__setIO(async () => seed, async () => {
      throw new Error('续期失败：DNSPod 返回错误（code -1）：登录失败')
    }, async () => ({ results: [] }))
    await s.load()

    await s.renew('a.example.com')
    expect(s.results[0].ok).toBe(false)
    expect(s.results[0].detail).toContain('登录失败')
  })

  // 「全部续期检查」逐项反馈，不是一句「全部成功」。
  it('全部续期检查逐项反馈', async () => {
    const s = useCertsStore()
    s.__setIO(async () => seed, async () => ({ domain: 'x', renewed: true }), async () => ({
      results: [
        { domain: 'a.example.com', ok: true, detail: '已检查' },
        { domain: 'b.example.com', ok: false, detail: '凭据无效' },
      ],
    }))
    await s.load()
    await s.renewAll()

    expect(s.results).toHaveLength(2)
    expect(s.results.find((r) => r.domain === 'b.example.com')!.ok).toBe(false)
  })
})
