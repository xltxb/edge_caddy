import { describe, expect, it } from 'vitest'
import { isInUse, orphanNote } from './orphan'

describe('covers 那一列：[] 和 null 必须分得开', () => {
  it('有人在用：不说话', () => {
    expect(orphanNote(['api.example.com'])).toBeNull()
  })

  it('空数组：说「没有路由在用它」—— 那是删它安全的唯一依据', () => {
    const n = orphanNote([])
    expect(n).not.toBeNull()
    expect(n!.text).toContain('没有路由在用它')
  })

  /*
   * **这一条是整组的重点。**
   *
   * `null` 是「路由清单读不到，算不出来」。把它渲染成「没有路由在用它」，
   * 等于引着人去删一张**可能还在服务二十条路由**的证书 —— 而 `[]` 那句话
   * 的全部作用就是让人放心动手。
   *
   * 与 reconnects_1h 那条同形（0 冒充「很稳」），但后果更直接：
   * 那个是误判健康，这个是按下删除。
   */
  it('null：不说话 —— 「算不出来」不能长成「没人用」的样子', () => {
    expect(orphanNote(null)).toBeNull()
    expect(orphanNote(undefined)).toBeNull()
  })

  /*
   * 上面那条只说明 null 不产生「没人用」。但 null 与非空数组同样是「不说话」，
   * 所以还要证明这两者不是因为同一个原因 —— 否则一个「永远返回 null」的实现
   * 也能让上面三条全过。
   */
  it('只有空数组会说话，另外两种都不会', () => {
    const said = [['a.com'], [], null].map((v) => orphanNote(v as string[] | null) !== null)
    expect(said).toEqual([false, true, false])
  })
})

describe('isInUse：决定确认弹层说哪一套话', () => {
  it('非空 = 在用', () => {
    expect(isInUse(['api.example.com'])).toBe(true)
  })

  /*
   * **`null` 算「不在用」是有意的，而它不是「可以放心删」的意思。**
   *
   * 这个函数只决定弹层的措辞走哪一支。算不出来时前端不该替人断言「它还在服务」，
   * 那会挡住一次合法的删除；真正的拦截在后端（`covers` 非空 → 2001），
   * 而那一份判据能看到路由清单。**前端不复刻它。**
   */
  it('null 与空数组都算「不在用」—— 拦截由后端做，前端不复刻那份判据', () => {
    expect(isInUse([])).toBe(false)
    expect(isInUse(null)).toBe(false)
  })
})
