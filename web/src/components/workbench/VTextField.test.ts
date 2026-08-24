import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import VTextField from './VTextField.vue'

/**
 * **一个空的数字框不能变成 0。**
 *
 * 原先是 `emit(numeric ? Number(raw) : raw)`，而 `Number('') === 0` ——
 * 人把一个数字框清空想重新输入，草稿在 400ms 后就写进一个 **0**：
 * 那不是「还没填」，是一个合法的数字。
 *
 * ## 它的症状离它很远
 *
 * 草稿不校验内容（后端刻意的：半成品不该挡住全站下发），所以那个 0 存得进去、
 * 全局可见、活得很久。清空那一刻工作台是红的、底部的下发按钮也禁着 ——
 * **但那只在当时那个人的屏幕上**。他离开之后，下一个人看到的是一条
 * 「有未下发改动」的规则，直到下发被后端 1002 拒。
 *
 * 灰度上撞到的报错是 `spec.window_s` / 「时间窗口要大于 0 秒」。
 *
 * > 一个空框和一个填了 0 的框，在草稿里长得一模一样 ——
 * > 而它们的意思是「还没填」和「我要求 0 秒」。
 *
 * ## 为什么是组件测试
 *
 * 这个转换只发生在 DOM 事件到 emit 之间，抽不出纯函数来测（抽出来就等于
 * 把它挪个地方再问一遍同样的问题）。这是这个仓库第一个组件测试，
 * 而理由是它守的东西只在那一层存在。
 */
describe('数字框：空与 0 是两件事', () => {
  const type = async (value: string, numeric = true) => {
    const w = mount(VTextField, {
      props: { modelValue: 60, dirty: false, invalid: false, numeric },
    })
    await w.find('input').setValue(value)
    return w.emitted('update:modelValue')?.at(-1)?.[0]
  }

  it('清空：给出空串，不是 0', async () => {
    expect(
      await type(''),
      'Number("") 是 0 —— 一个没填的框变成了一个合法的数字，而后端只能看到 0',
    ).toBe('')
  })

  it('只打了空格：也算空', async () => {
    expect(await type('   ')).toBe('')
  })

  it('正常的数字：还是数字，不是字符串', async () => {
    expect(await type('30'), '数字框应该给出 number —— 字符串会让后端 1001').toBe(30)
  })

  /*
   * **不是数字时保持原样。**
   *
   * `Number('abc')` 是 NaN，而 NaN 进 JSON 会变成 `null` —— 那时人输入的东西
   * 整个没了，界面上那个框也会空掉（`modelValue ?? ''`），
   * 看起来像「我打的字被吞了」。
   *
   * 留着原字符串：人看得见自己输入了什么，校验也报得出来。
   */
  it('打了非数字：保持原样，不变成 NaN', async () => {
    expect(await type('abc')).toBe('abc')
  })

  it('非数字框不受影响：原样给出字符串', async () => {
    expect(await type('', false)).toBe('')
    expect(await type('10.8.0.2:8080', false)).toBe('10.8.0.2:8080')
  })
})
