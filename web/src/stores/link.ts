import { defineStore } from 'pinia'
import { ref } from 'vue'
import type { LinkState } from '@/api/types'

/**
 * 实时通道状态。
 *
 * 单独一个 store 是有意的：整个控制台的实时性都挂在这条连接上，它断了必须
 * 让用户看见——否则人对着一屏静止的旧数据以为一切正常。这跟 ADR-0002 提醒的
 * 「界面不能给出兑现不了的承诺」是同一类错，所以它不是某个页面的局部状态。
 */
export const useLinkStore = defineStore('link', () => {
  const state = ref<LinkState>('connecting')
  /** 最近一次收到帧的本地时间戳；顶栏用它显示「数据停在 N 秒前」。 */
  const lastFrameAt = ref<number | null>(null)

  function setState(next: LinkState): void {
    state.value = next
  }

  function markFrame(): void {
    lastFrameAt.value = Date.now()
  }

  return { state, lastFrameAt, setState, markFrame }
})

/** 顶栏文案。降级时必须说出「已经不是实时的了」，不能只换个颜色。 */
export function linkLabel(s: LinkState): { text: string; tone: 'ok' | 'warn' | 'danger' } {
  switch (s) {
    case 'live':
      return { text: '实时', tone: 'ok' }
    case 'connecting':
      return { text: '连接中', tone: 'warn' }
    case 'reconnecting':
      return { text: '重连中', tone: 'warn' }
    case 'polling':
      return { text: '实时已断 · 降为 2s 轮询', tone: 'danger' }
  }
}
