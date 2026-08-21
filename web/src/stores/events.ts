import { defineStore } from 'pinia'
import { ref } from 'vue'
import type { EventFrame, EventWire } from '@/api/types'
import { fromEventWire, type ConsoleEvent } from '@/model'

/** 事件流保留最近 40 条（契约 §3 的 events 也是这个量）。 */
const CAP = 40

export const useEventsStore = defineStore('events', () => {
  const items = ref<ConsoleEvent[]>([])

  /** 首屏铺底，走 REST；WS 只送增量。 */
  function seed(wires: EventWire[]): void {
    items.value = wires.map(fromEventWire).slice(0, CAP)
  }

  function applyEvent(frame: EventFrame): void {
    const e = fromEventWire(frame.data)
    // 重连后可能重放，按 id 去重
    if (items.value.some((x) => x.id === e.id)) return
    items.value = [e, ...items.value].slice(0, CAP)
  }

  return { items, seed, applyEvent }
})
