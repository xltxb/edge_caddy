import { defineStore } from 'pinia'
import { ref } from 'vue'
import type { Frame } from '@/api/types'

export interface EventItem {
  t: string
  node: string
  /** ok / warn / crit */
  kind: string
  msg: string
}

/**
 * MAX 是环形缓冲的上限。
 *
 * 事件流会一直涨；不限长会把内存吃光，页面也会越来越卡。40 条够覆盖
 * 「刚刚发生了什么」，更久远的事应该去审计与下发记录里查。
 */
const MAX = 40

export const useEventsStore = defineStore('events', () => {
  const events = ref<EventItem[]>([])

  function applyFrame(frame: Frame) {
    if (frame.type !== 'event') return
    const d = frame.data as Partial<EventItem>
    events.value = [
      { t: d.t ?? '', node: d.node ?? '', kind: d.kind ?? 'ok', msg: d.msg ?? '' },
      ...events.value,
    ].slice(0, MAX)
  }

  return { events, applyFrame }
})
