import { describe, expect, it } from 'vitest'
import { linkLabel } from './link'
import type { LinkState } from '@/api/types'

/*
 * 顶栏那句话守的是整个控制台里最硬的一条：**屏幕上的数字是不是活的**。
 *
 * 它说错的代价与别处不同 —— 别处说错让人误判一件事，这里说错让人**误判满屏
 * 的每一件事**：一屏静止的旧数据看起来和一屏正常数据一模一样。
 *
 * 所以这组按**不变量**写，不照抄那张表。照抄的话，改文案就得改测试，
 * 而真正不能变的那条（只有 live 才能说「实时」）反而没被单独钉住。
 */

const ALL: LinkState[] = ['live', 'connecting', 'reconnecting', 'polling']

describe('实时状态标签', () => {
  it('每个状态都有标签 —— 漏一个会渲染出空白，而空白读起来像「没问题」', () => {
    for (const s of ALL) {
      const l = linkLabel(s)
      expect(l, `${s} 没有标签`).toBeTruthy()
      expect(l.text.length, `${s} 的文案是空的`).toBeGreaterThan(0)
    }
  })

  /*
   * 这条是这组的理由。
   *
   * 「实时」这两个字是一句断言：你现在看到的东西是刚刚发生的。只有 live 有资格
   * 说它 —— 其余三种状态下，屏幕上的东西要么在等重连、要么来自 2s 轮询。
   */
  it('只有 live 说得出「实时」这个断言', () => {
    expect(linkLabel('live').text).toBe('实时')
    for (const s of ALL.filter((x) => x !== 'live')) {
      // 「实时已断」里含「实时」二字但不是那个断言 —— 所以查的是「以实时开头且到此为止」
      expect(linkLabel(s).text, `${s} 说了「实时」`).not.toBe('实时')
    }
  })

  it('只有 live 是 ok 色 —— 绿点本身也是一句断言', () => {
    expect(linkLabel('live').tone).toBe('ok')
    for (const s of ALL.filter((x) => x !== 'live')) {
      expect(linkLabel(s).tone, `${s} 是 ok 色`).not.toBe('ok')
    }
  })

  /*
   * 降级必须**用字说出来**，不能只换个颜色。
   *
   * 色觉障碍、暗色主题下的低对比、或者人根本没在看那个角落 —— 颜色是最容易
   * 丢掉的一路信息。而这条状态一丢，人就在对着旧数据做决定。
   */
  it('降为轮询时文案里要有字说明，不只是变红', () => {
    const t = linkLabel('polling').text
    expect(t).toContain('断')
    expect(t).toContain('轮询')
    // 把间隔写出来：它同时回答了「我看的数据有多旧」
    expect(t).toMatch(/\d+s/)
  })

  it('连接中与重连中要能被分开 —— 一个是还没连上，一个是断过', () => {
    expect(linkLabel('connecting').text).not.toBe(linkLabel('reconnecting').text)
  })
})
