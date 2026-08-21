import { defineStore } from 'pinia'
import { ref } from 'vue'
import type { LinkState } from '@/api/types'

/**
 * 实时通道状态。
 *
 * 单独一个 store 是有意的：整个控制台的实时性都挂在这条连接上，它断了必须
 * 让用户看见——否则人对着一屏静止的旧数据以为一切正常。这跟 ADR-0002 提醒的
 * 「界面不能给出兑现不了的承诺」是同一类错，所以它不是某个页面的局部状态。
 *
 * 这里**没有**「最近一次收到帧的时间」。曾经有过一个 `lastFrameAt`，注释写着
 * 顶栏用它显示「数据停在 N 秒前」—— 而顶栏从来没读过它，那句注释描述的是一个
 * 不存在的功能。
 *
 * 而它就算做出来也是错的：降级之后 REST 每 2s 在刷，屏幕上的数据是新的，
 * 而 `lastFrameAt` 量的是最后一个 **WebSocket 帧**的年龄，会一路涨。
 * 把「实时通道断了多久」显示成「你看的数据有多旧」，是一句更难察觉的假话 ——
 * 它有单位、有数字、每秒都在动，看起来比一句「降为 2s 轮询」更可信。
 *
 * 「你看的数据有多旧」这个问题，现有的标签已经答了：`polling` 那句里就写着 2s。
 */
export const useLinkStore = defineStore('link', () => {
  const state = ref<LinkState>('connecting')

  function setState(next: LinkState): void {
    state.value = next
  }

  return { state, setState }
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
