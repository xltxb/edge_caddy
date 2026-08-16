import { describe, it, expect, beforeEach } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { useDeployStore } from './deploys'

describe('下发进度', () => {
  beforeEach(() => setActivePinia(createPinia()))

  // 进度帧逐节点就地更新，同一节点从「进行中」推进到终态。
  //
  // 与心跳帧同类，但多一个状态机：pushing 之后必然来一个 ok/fail。
  // 写成追加的话，一个节点会在界面上出现两行——一行永远停在「热重载中」。
  it('同一节点从进行中推进到终态，不产生第二行', () => {
    const s = useDeployStore()
    s.start(81, ['node-a', 'node-b'])

    s.applyFrame({ type: 'deploy_progress', data: { deploy_id: 81, node: 'node-a', state: 'pushing', res: '热重载中' } })
    s.applyFrame({ type: 'deploy_progress', data: { deploy_id: 81, node: 'node-a', state: 'ok', res: '31ms' } })

    expect(s.rows).toHaveLength(2)
    const a = s.rows.find((r) => r.node === 'node-a')!
    expect(a.state).toBe('ok')
    expect(a.res).toBe('31ms')
  })

  // 一成一败要各自呈现，不合并成一个总数。
  it('一成一败各自呈现', () => {
    const s = useDeployStore()
    s.start(81, ['node-a', 'node-b'])
    s.applyFrame({ type: 'deploy_progress', data: { deploy_id: 81, node: 'node-a', state: 'ok', res: '31ms' } })
    s.applyFrame({ type: 'deploy_progress', data: { deploy_id: 81, node: 'node-b', state: 'fail', res: 'connection refused' } })

    expect(s.okCount).toBe(1)
    expect(s.failCount).toBe(1)
    expect(s.done).toBe(true)
    expect(s.rows.find((r) => r.node === 'node-b')!.res).toBe('connection refused')
  })

  // 还有节点没落定时不算完成——底栏的「完成」提示不能提前亮。
  it('尚有节点未落定时不算完成', () => {
    const s = useDeployStore()
    s.start(81, ['node-a', 'node-b'])
    s.applyFrame({ type: 'deploy_progress', data: { deploy_id: 81, node: 'node-a', state: 'ok', res: '31ms' } })
    expect(s.done).toBe(false)
  })

  // 别的下发的帧不能串进来。
  //
  // 连推两次时后一次的帧若被前一次接收，界面会显示一堆对不上的结果。
  it('忽略不属于当前下发的帧', () => {
    const s = useDeployStore()
    s.start(81, ['node-a'])
    s.applyFrame({ type: 'deploy_progress', data: { deploy_id: 99, node: 'node-a', state: 'fail', res: '别的下发' } })
    expect(s.rows[0].state).toBe('pending')
  })

  it('未知节点的进度帧不会凭空造出一行', () => {
    const s = useDeployStore()
    s.start(81, ['node-a'])
    s.applyFrame({ type: 'deploy_progress', data: { deploy_id: 81, node: 'node-never-seen', state: 'ok', res: '1ms' } })
    expect(s.rows).toHaveLength(1)
  })
})
