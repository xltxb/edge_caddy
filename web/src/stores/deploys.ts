import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import type { Frame } from '@/api/types'

/** 一个节点在本次下发中的状态。pending 是「已下发、还没回报」。 */
export interface ProgressRow {
  node: string
  state: 'pending' | 'pushing' | 'ok' | 'fail'
  res: string
}

export const useDeployStore = defineStore('deploys', () => {
  const deployId = ref<number | null>(null)
  const rows = ref<ProgressRow[]>([])

  /**
   * start 在发起下发时登记目标节点。
   *
   * 先把节点列出来（pending），而不是等帧到了再逐个冒出来：目标是已知的，
   * 一开始就展示全部让人看得出「还差谁」，否则界面只会显示已回报的那几个，
   * 卡住的节点反而看不见——而那正是最需要被看见的。
   */
  function start(id: number, nodes: string[]) {
    deployId.value = id
    rows.value = nodes.map((node) => ({ node, state: 'pending', res: '' }))
  }

  /**
   * applyFrame 处理一条进度帧。
   *
   * 就地更新对应行，不追加：追加的话一个节点会出现两行，其中一行永远停在
   * 「热重载中」。未知节点忽略——凭一条帧造出的行没有上下文。
   * 不属于当前下发的帧也忽略：连推两次时，后一次的帧串进前一次会让界面
   * 显示一堆对不上的结果。
   */
  function applyFrame(frame: Frame) {
    if (frame.type !== 'deploy_progress') return
    const d = frame.data as { deploy_id?: number; node?: string; state?: string; res?: string }
    if (deployId.value === null || d.deploy_id !== deployId.value) return
    const i = rows.value.findIndex((r) => r.node === d.node)
    if (i < 0) return
    rows.value[i] = {
      ...rows.value[i],
      state: (d.state as ProgressRow['state']) ?? rows.value[i].state,
      res: d.res ?? rows.value[i].res,
    }
  }

  const okCount = computed(() => rows.value.filter((r) => r.state === 'ok').length)
  const failCount = computed(() => rows.value.filter((r) => r.state === 'fail').length)
  /** 全部节点都落定才算完成——底栏的「完成」提示不能提前亮。 */
  const done = computed(
    () => rows.value.length > 0 && rows.value.every((r) => r.state === 'ok' || r.state === 'fail'),
  )

  return { deployId, rows, start, applyFrame, okCount, failCount, done }
})
