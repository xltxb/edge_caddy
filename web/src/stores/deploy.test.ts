import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useDeployStore } from './deploy'
import type { DeployProgressFrame, DeployProgressState } from '@/api/types'

const getMock = vi.fn()
vi.mock('@/api/http', () => ({
  http: {
    get: (...a: unknown[]) => getMock(...a),
    post: vi.fn(),
    put: vi.fn(),
    del: vi.fn(),
  },
}))

const frame = (
  node: string,
  state: DeployProgressState,
  detail = '',
  retrying = false,
): DeployProgressFrame => ({
  type: 'deploy_progress',
  data: { deploy_id: 1, cfg_version: 'cfg-x', node, state, detail, retrying },
})

function seedRunning(nodes: string[]) {
  const store = useDeployStore()
  store.current = {
    id: 1,
    cfgVersion: 'cfg-x',
    resKeys: ['route:a'],
    rows: nodes.map((node) => ({ node, state: 'wait', detail: '待下发', retrying: false })),
    phase: 'running',
  }
  store.phase = 'running'
  return store
}

describe('useDeployStore', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    getMock.mockReset()
  })

  describe('applyProgress', () => {
    it('全部终态且无重试才算落定', () => {
      const s = seedRunning(['a', 'b'])
      s.applyProgress(frame('a', 'ok', '31ms'))
      expect(s.phase).toBe('running')
      s.applyProgress(frame('b', 'ok', '40ms'))
      expect(s.phase).toBe('done')
    })

    it('每一行都到了终态、但有一条还在重试 —— 没结束', () => {
      // 「6/6」不等于「结束了」。不区分的话，人会以为落定了然后关掉弹层，
      // 而那一行之后还会变（ADR-0005）。
      const s = seedRunning(['a', 'b'])
      s.applyProgress(frame('a', 'ok', '31ms'))
      s.applyProgress(frame('b', 'fail', 'deadline exceeded', true))
      expect(s.doneCount).toBe(2)
      expect(s.retryingCount).toBe(1)
      expect(s.phase).toBe('running')
    })

    it('重试结束后落定', () => {
      const s = seedRunning(['a', 'b'])
      s.applyProgress(frame('a', 'ok', '31ms'))
      s.applyProgress(frame('b', 'fail', 'deadline exceeded', true))
      expect(s.phase).toBe('running')
      s.applyProgress(frame('b', 'fail', '节点不可达 · 已放弃重试', false))
      expect(s.phase).toBe('done')
    })

    it('忽略不属于本次下发的帧', () => {
      const s = seedRunning(['a'])
      const other: DeployProgressFrame = {
        type: 'deploy_progress',
        data: { deploy_id: 999, cfg_version: 'x', node: 'a', state: 'ok', detail: '', retrying: false },
      }
      s.applyProgress(other)
      expect(s.current!.rows[0]!.state).toBe('wait')
    })
  })

  describe('轮询降级', () => {
    it('部分结果按节点合并，不抹掉还没回报的行', async () => {
      // 后端逐条落库，进行中拿到的是**部分**结果。整体替换会让「还有谁没回来」
      // 消失，而那正是断线降级时最需要看见的东西。
      const s = seedRunning(['a', 'b', 'c'])
      getMock.mockResolvedValue({
        targets: ['a', 'b', 'c'],
        target_count: 3,
        phase: 'running',
        results: [{ node: 'a', state: 'ok', detail: '31ms', retrying: false }],
      })

      await s.pollOnce()

      expect(s.current!.rows).toHaveLength(3)
      expect(s.current!.rows.find((r) => r.node === 'a')).toMatchObject({
        state: 'ok',
        detail: '31ms',
      })
      // b / c 还没回报，必须原样留着
      expect(s.current!.rows.find((r) => r.node === 'b')!.state).toBe('wait')
      expect(s.current!.rows.find((r) => r.node === 'c')!.state).toBe('wait')
      expect(s.phase).toBe('running')
    })

    it('后端报 done 时停止轮询并落定', async () => {
      const s = seedRunning(['a', 'b'])
      getMock.mockResolvedValue({
        targets: ['a', 'b'],
        target_count: 2,
        phase: 'done',
        results: [
          { node: 'a', state: 'ok', detail: '31ms', retrying: false },
          { node: 'b', state: 'fail', detail: '不可达', retrying: false },
        ],
      })

      await s.pollOnce()

      expect(s.phase).toBe('done')
      expect(s.okCount).toBe(1)
      expect(s.failCount).toBe(1)
    })

    it('轮询失败不打断界面，行保持原样', async () => {
      const s = seedRunning(['a'])
      getMock.mockRejectedValue(new Error('网络断了'))

      await expect(s.pollOnce()).resolves.toBeUndefined()
      expect(s.current!.rows[0]!.state).toBe('wait')
    })
  })

  describe('mergeRows', () => {
    it('targets 铺骨架，未回报的留成「待下发」', () => {
      const s = useDeployStore()
      const rows = s.mergeRows(
        ['a', 'b', 'c'],
        [{ node: 'b', state: 'ok', detail: '31ms', retrying: false }],
      )
      expect(rows.map((r) => `${r.node}:${r.state}`)).toEqual(['a:wait', 'b:ok', 'c:wait'])
    })

    it('行序跟 targets 走，不跟回报先后走 —— 否则每次轮询行都在跳', () => {
      const s = useDeployStore()
      const rows = s.mergeRows(
        ['a', 'b', 'c'],
        [
          { node: 'c', state: 'ok', detail: '', retrying: false },
          { node: 'a', state: 'ok', detail: '', retrying: false },
        ],
      )
      expect(rows.map((r) => r.node)).toEqual(['a', 'b', 'c'])
    })
  })

  describe('刷新后恢复', () => {
    beforeEach(() => sessionStorage.clear())

    it('没有记录时不恢复', async () => {
      const s = useDeployStore()
      expect(await s.resume()).toBe(false)
      expect(s.current).toBeNull()
    })

    it('有进行中的下发时用 targets 重建完整的行', async () => {
      // 正常路径下行来自 POST /deploys 的响应，本来就完整；
      // targets 唯一的用武之地恰恰是「那次响应已经没了」的场景。
      sessionStorage.setItem('ec.deploy.running', '42')
      getMock.mockResolvedValue({
        id: 42,
        cfg_version: 'cfg-x',
        res_keys: ['route:a'],
        targets: ['a', 'b', 'c'],
        target_count: 3,
        phase: 'running',
        results: [{ node: 'a', state: 'ok', detail: '31ms', retrying: false }],
      })

      const s = useDeployStore()
      expect(await s.resume()).toBe(true)
      expect(s.current!.rows).toHaveLength(3)
      expect(s.current!.rows.map((r) => r.state)).toEqual(['ok', 'wait', 'wait'])
      expect(s.phase).toBe('running')
    })

    it('已经落定的不再恢复，并清掉记录', async () => {
      sessionStorage.setItem('ec.deploy.running', '42')
      getMock.mockResolvedValue({
        id: 42,
        cfg_version: 'cfg-x',
        res_keys: [],
        targets: ['a'],
        target_count: 1,
        phase: 'done',
        results: [{ node: 'a', state: 'ok', detail: '', retrying: false }],
      })

      const s = useDeployStore()
      expect(await s.resume()).toBe(false)
      expect(sessionStorage.getItem('ec.deploy.running')).toBeNull()
    })

    it('那次下发查不到了就放弃恢复，不卡住界面', async () => {
      sessionStorage.setItem('ec.deploy.running', '42')
      getMock.mockRejectedValue(new Error('1003'))
      const s = useDeployStore()
      expect(await s.resume()).toBe(false)
      expect(s.current).toBeNull()
      expect(sessionStorage.getItem('ec.deploy.running')).toBeNull()
    })
  })

  describe('校验错误索引', () => {
    it('带数组下标的路径同时按基路径登记 —— 否则红框落不到输入框上', async () => {
      const s = useDeployStore()
      const { http } = await import('@/api/http')
      vi.mocked(http.post).mockResolvedValue({
        before: '',
        after: '',
        baseline: 'cfg-2f9a1c',
        targets: [],
        validation: {
          ok: false,
          errors: [
            { res_key: 'route:a', field: 'whitelist[0]', reason: '不是合法 CIDR' },
            { res_key: 'rule:r', field: 'spec.ips[2]', reason: '不是合法 IP' },
          ],
        },
      } as never)

      await s.runPreview(['route:a', 'rule:r'])

      expect(s.fieldErrors['route:a']!['whitelist[0]']).toBe('不是合法 CIDR')
      expect(s.fieldErrors['route:a']!['whitelist']).toBe('不是合法 CIDR')
      expect(s.fieldErrors['rule:r']!['spec.ips']).toBe('不是合法 IP')
      expect(s.canDeploy).toBe(false)
    })

    it('校验没过时 after 是 null —— 预览仍然成功返回，只是不可下发', async () => {
      // 契约 §7.1：校验失败在预览里返回 code 0，不是请求失败。
      // after 为 null 是因为那份配置根本不存在（不是空串 —— 空串是一份合法的
      // 空配置）。弹层必须据此**不画 diff**，否则会渲染成整份删除。
      const s = useDeployStore()
      const { http } = await import('@/api/http')
      vi.mocked(http.post).mockResolvedValue({
        before: '{"a":1}',
        after: null,
        baseline: 'cfg-2f9a1c',
        targets: [],
        validation: {
          ok: false,
          errors: [{ res_key: 'route:a', field: 'upstream', reason: '必须形如 host:port' }],
        },
      } as never)

      await s.runPreview(['route:a'])

      expect(s.phase).toBe('confirm')
      expect(s.preview!.after).toBeNull()
      expect(s.canDeploy).toBe(false)
    })

    it('before 为 null 时仍可下发 —— 基线渲染不出来不影响 after 的校验结果', async () => {
      const s = useDeployStore()
      const { http } = await import('@/api/http')
      vi.mocked(http.post).mockResolvedValue({
        before: null,
        after: '{"a":1}',
        baseline: 'cfg-2f9a1c',
        targets: [{ id: 'n1', status: 'ok' }],
        validation: { ok: true, errors: [] },
      } as never)

      await s.runPreview(['route:a'])
      expect(s.preview!.before).toBeNull()
      expect(s.canDeploy).toBe(true)
    })

    it('targets 为空数组是合法结果 —— 预览不要求有在线节点', async () => {
      const s = useDeployStore()
      const { http } = await import('@/api/http')
      vi.mocked(http.post).mockResolvedValue({
        before: 'a',
        after: 'b',
        baseline: 'cfg-2f9a1c',
        targets: [],
        validation: { ok: true, errors: [] },
      } as never)

      await s.runPreview(['route:a'])
      expect(s.preview!.targets).toEqual([])
      expect(s.canDeploy).toBe(true)
    })

    it('校验通过时可以下发', async () => {
      const s = useDeployStore()
      const { http } = await import('@/api/http')
      vi.mocked(http.post).mockResolvedValue({
        before: 'a',
        after: 'b',
        baseline: 'cfg-2f9a1c',
        targets: [{ id: 'n1', status: 'ok' }],
        validation: { ok: true, errors: [] },
      } as never)

      await s.runPreview(['route:a'])
      expect(s.canDeploy).toBe(true)
      expect(s.phase).toBe('confirm')
    })
  })
})
