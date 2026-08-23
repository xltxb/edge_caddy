/**
 * 「这张证书还有人用吗」—— `covers` 那一列的判断（契约 §9）。
 *
 * 抽出来的理由与 `@/nodes/flags` 一样：这是一句**断言**，而写在模板里的断言
 * 只有人盯着截图看的时候才会被检查一次。
 *
 * ## 三种值三个意思，而中间那个会引着人动手
 *
 *   `["a.example.com"]`  正在服务这些        → 不说话
 *   `[]`                 **存着但没人用**    → 说出来，删它是安全的
 *   `null`               路由清单读不到      → **不说话**
 *
 * `[]` 和 `null` 的区别是这里的全部重点。`[]` 是一个**断言**：没有任何站点在
 * 用它，所以删掉不会弄坏什么。而 `null` 是「我算不出来」—— 把它当成 `[]`，
 * 等于引着人去删一张**可能还在服务二十条路由**的证书。
 *
 * 与 `reconnects_1h` 那条同形（`0` 冒充「很稳」），但后果更直接：
 * 那个是误判健康，**这个是按下删除**。
 *
 * ## 为什么「没人用」值得单独说一句
 *
 * 一张没人用的证书不会自己消失：它仍然被内联进每台节点的配置、仍然进到期扫描、
 * 仍然每天报警，而且**它还是一把有效的私钥**，分发在每台机器上，
 * 对应的站点却已经不存在了（删掉一条路由，它的证书原样留着）。
 *
 * 界面上这一列是**唯一说得出这件事的地方**。
 */

export interface OrphanNote {
  text: string
  /** 提示性的，不是故障 —— 它是「可以清理」，不是「出问题了」。 */
  tone: 'muted'
}

/**
 * 返回 `null` 表示不必说。
 *
 * **`null`（算不出来）与非空数组一样不说话**，而理由完全不同：后者是「正常」，
 * 前者是「不知道」。两者在这里合并是有意的 —— 界面上「不说话」意味着
 * 「没有可以动手的结论」，而这两种情况下都确实没有。
 *
 * 想区分它们的话该加的是另一句（「这一列这次算不出来」），不是把 `null`
 * 渲染成「没人用」。
 */
export function orphanNote(covers: string[] | null | undefined): OrphanNote | null {
  if (covers === null || covers === undefined) return null
  if (covers.length > 0) return null
  return { text: '没有路由在用它', tone: 'muted' }
}

/** 删除这张证书会不会弄坏正在服务的站点 —— 决定确认弹层说哪一套话。 */
export function isInUse(covers: string[] | null | undefined): boolean {
  return Array.isArray(covers) && covers.length > 0
}
