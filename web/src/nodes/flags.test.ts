import { describe, expect, it } from 'vitest'
import { blockedNote, canDelete, canToggleDns, nodeFlags, reconnectNote } from './flags'
import type { EdgeNode } from '@/model'

const node = (over: Partial<EdgeNode> = {}): EdgeNode => ({
  id: 'node-hk-01',
  city: '香港',
  vendor: 'DMIT PPro',
  line: 'CN2 GIA',
  ip: '203.0.113.7',
  status: 'ok',
  online: true,
  reconnects1h: 0,
  geoDbOk: null,
  blocked1h: null,
  inRotation: true,
  weightSet: true,
  cpu: 10,
  mem: 20,
  conns: 100,
  cpuSeries: [],
  hbAgeMs: 0,
  hbStampedAt: 0,
  cfgVersion: 'cfg-1',
  agentVersion: 'v0.2.0 (d3da612)',
  drift: false,
  dnsEnabled: true,
  drainedAt: null,
  dnsReason: '' as const,
  dnsActor: null,
  dnsChangedAt: null,
  routes: 1,
  rules: 1,
  ...over,
})

const AT = '2026-08-21T16:52:26+08:00'

const texts = (n: EdgeNode, sync: boolean | null = true) => nodeFlags(n, sync).map((f) => f.text)

describe('下线与离线不合并', () => {
  /*
   * 这一组对应 ADR-0014 的**前提**，不是它的结论。
   *
   * 结论是「分两列存」，那个后端有测试。前提是「这两件事会同时成立，而且各要
   * 各的说法」—— 前提不成立的话，分两列也是白分：界面照样可以把它们揉回一格。
   */
  /*
   * 名字只说这个函数**保证得了**的那一半。
   *
   * 第一版叫「…status 那一格另说」，而「另一格真的渲染了」是模板的事，这个函数
   * 管不着 —— 把 VStatusPill 删掉，这条测试照样绿。从名字推改坏就会撞见这一点：
   * **改坏要从名字推，不从实现推**，否则名字声称的多余部分永远不会被检查。
   */
  it('已下线且在线：旗标里出现人为下线，且不掺进 status 的说法', () => {
    const n = node({ status: 'ok', drainedAt: '2026-08-21T16:52:26+08:00' })
    expect(texts(n)).toContain('已下线（人为）')
    expect(texts(n).join()).not.toContain('离线')
  })

  it('未下线但离线：不冒出「已下线」', () => {
    expect(texts(node({ status: 'down' }))).not.toContain('已下线（人为）')
  })

  it('两者可以同时成立', () => {
    const n = node({ status: 'down', drainedAt: '2026-08-21T16:52:26+08:00' })
    expect(texts(n)).toContain('已下线（人为）')
    expect(n.status).toBe('down')
  })
})

describe('「已退出解析」是关于服务商的断言', () => {
  /*
   * 前提：dns_enabled 只是本地标志位，解析记录变没变是另一件事。
   * 没同步时说「已退出解析」是**常驻的谎** —— toast 会消失，旗标不会。
   */
  it('同步过：说「已退出解析」', () => {
    expect(texts(node({ dnsEnabled: false }), true)).toContain('已退出解析')
  })

  it('没同步：降级成「已标记退出（解析未变）」', () => {
    expect(texts(node({ dnsEnabled: false }), false)).toContain('已标记退出（解析未变）')
  })

  // 没问到就不加限定：宁可少说一句，也不要因为自己没问到就说节点在撒谎
  it('还没问到（null）：按同步过说，不擅自降级', () => {
    expect(texts(node({ dnsEnabled: false }), null)).toContain('已退出解析')
  })

  it('解析开着时根本不出这条旗标', () => {
    expect(texts(node({ dnsEnabled: true }), false)).toHaveLength(0)
  })
})

describe('已下线的节点，解析开关两个方向都不该能按', () => {
  /*
   * 这一组的不变量**换过一次**，而那不是「改严了」。
   *
   * 原先只拦「开」的方向，理由是「关这一下不会把流量送到一台连不上的机器上」
   * —— **那个理由至今成立**，它只是漏了一件事：关这一下会把 dns_reason 改写成
   * manual，而 drained_at 还在。于是节点页说「已下线」、DNS 页说「人手动关的」，
   * 两页各说各的，**而每一句单独看都是对的**。
   *
   * 跟「观测能力变了所以旧约束开始拦真话」那种在 diff 里长得一样（一条断言
   * 反过来了），但性质不同：那种是旧约束现在多余了，这种是旧约束一直有个代价
   * 而我们才看见。
   */
  it('解析关着：拦住，并说清怎么办', () => {
    const r = canToggleDns(node({ dnsEnabled: false, drainedAt: AT }))
    expect(r.ok).toBe(false)
    expect(r.reason).toContain('重新上线')
  })

  it('解析开着：也拦住 —— 关这一下会把归因改写成「人手动关的」', () => {
    const r = canToggleDns(node({ dnsEnabled: true, drainedAt: AT }))
    expect(r.ok).toBe(false)
    expect(r.reason).toContain('本来就不在解析里')
    // 两个方向的话不一样：一句说「怎么用回来」，一句说「这件事没意义」
    expect(r.reason).not.toContain('重新上线')
  })

  it('没下线：两个方向都正常可按', () => {
    expect(canToggleDns(node({ dnsEnabled: false })).ok).toBe(true)
    expect(canToggleDns(node({ dnsEnabled: true })).ok).toBe(true)
  })
})

describe('删记录的前提是「已下线」，不是「已离线」', () => {
  /*
   * 这一组守的是一个**容易被写对结论、写错判据**的地方。
   *
   * 判据必须是 `drainedAt`（意图），不能是 `status === 'down'`（观察）——
   * 而两者在「一台挂掉的机器」上恰好同时成立，所以拿那台机器测，写错也是绿的。
   * 下面第二条专门造出**只有观察成立**的那台机器：心跳超时判定为 down，
   * 但没人下线过它。
   *
   * 为什么那台不能删：down 只说明主控连着几个周期没收到心跳，**隧道随时可能
   * 回来**（Agent 的 Restart=always 会一直重连）。只有 drain 会断隧道并拒绝
   * 重连（ADR-0014）。删掉一台还会重连的机器的记录，它会被按证书认出来、
   * 然后在一张不存在的行上写心跳 —— UPDATE 影响 0 行，不报错。
   */
  it('已下线：可以删', () => {
    expect(canDelete(node({ drainedAt: AT })).ok).toBe(true)
  })

  it('离线但没下线：仍然不能删 —— 判据是意图不是观察', () => {
    const r = canDelete(node({ status: 'down', online: false, drainedAt: null }))
    expect(r.ok).toBe(false)
    expect(r.reason).toContain('先下线')
  })

  it('在线且没下线：不能删', () => {
    expect(canDelete(node({ status: 'ok', drainedAt: null })).ok).toBe(false)
  })

  /*
   * **措辞是前提，不是劝阻。**
   *
   * 判据（后端 domain.md）：去掉这个检查会产生不一致的状态 → 那是前提；
   * 只是「后果严重」→ 那才是确认。写成「你确定吗 / 不可撤销」，人会以为自己
   * 在被劝阻，然后去找地方跳过它 —— 而这一条是跳不得的。
   *
   * 这条测试挡的是**把 reason 改成劝阻话术**：那种改动不会让上面三条变红。
   */
  it('拒绝的话说的是「先做那件事」，不是「你确定吗」', () => {
    const r = canDelete(node({ drainedAt: null }))
    expect(r.reason).toContain('先下线')
    expect(r.reason).not.toMatch(/确定|不可撤销|谨慎|危险/)
  })
})

describe('隧道建立次数：0 和「数不出来」必须分得开', () => {
  /*
   * 这一组守的是**一个假值的危害取决于它引不引起疑问**。
   *
   * 这个字段的存在理由是「在一切看起来正常时指出异常」，而 `0` 恰好是「一切
   * 正常」的样子 —— 把数不出来渲染成 0（或者干脆不显示），等于让它在自己失效
   * 的那一刻伪装成它最想否定的那个状态。而人**不会**去追问一个 0，
   * 不像 `dns_actor` 那个 `"system"` 至少会让人问「那是谁」。
   */
  it('null：必须明说数不出来，而且要否掉 0 这个读法', () => {
    const note = reconnectNote(node({ reconnects1h: null }))
    expect(note).not.toBeNull()
    expect(note).toContain('数不出来')
    // **这一句挡的是「留白」和「说成 0」两种退化** —— 两者在界面上都读作「没问题」
    expect(note).toContain('不是 0')
  })

  it('0：不说话 —— 常态占着地方会稀释掉真正要看的那一行', () => {
    expect(reconnectNote(node({ reconnects1h: 0 }))).toBeNull()
  })

  it('大于 0：说出次数', () => {
    expect(reconnectNote(node({ reconnects1h: 4 }))).toContain('4')
  })

  /*
   * **不能说成「断连 N 次」。**
   *
   * 口径是主控会话表里的隧道建立次数，而同一屏上还有 Agent 日志 —— 那是那台
   * 机器自己的说法，两者结构上就对不齐（一个被 kill 的进程不会记录自己的死亡）。
   * 实测过：主控说 3，Agent 日志里 1 条。写成「断连」会让人去对账那两个数。
   */
  it('措辞说的是主控的观测，不是「断连」—— 那会引人去跟 Agent 日志对账', () => {
    const note = reconnectNote(node({ reconnects1h: 4 }))!
    expect(note).toContain('主控')
    expect(note).not.toContain('断连')
  })

  /*
   * **徽标全绿时它仍然要说话** —— 那正是它唯一的用途。
   *
   * 这条挡的是「顺手给它加个 !online 之类的前置条件」：那会让它只在别的字段
   * 已经报警时才出现，而它存在的意义恰恰是别的字段都不报警的那一刻。
   * 灰度上真发生过：CDN 每十几分钟切一次长连接，status/online/hb_age_ms 全绿。
   */
  it('status ok + 在线 + 心跳新鲜，照样说 —— 那是它唯一的用途', () => {
    const n = node({ status: 'ok', online: true, hbAgeMs: 300, reconnects1h: 4 })
    expect(reconnectNote(n)).toContain('4')
  })
})

/**
 * GeoIP 库那一档。**三个值，只有一个该说话。**
 *
 * 这三条挡的是同一个改坏：把判据写成 `!n.geoDbOk`（或者 `n.geoDbOk === null ||
 * !n.geoDbOk` 之类的等价物）。那个写法会把「主控没有库」也标成红的 ——
 * 一套没在用地域功能的系统，每台节点天天挂一条警告。
 *
 * **人两天就学会忽略它**，连带着忽略掉真出问题那天的那一条。
 */
describe('geoDbOk 三档', () => {
  const geo = (n: EdgeNode) => nodeFlags(n, true).find((f) => f.text.includes('GeoIP'))

  it('false：标出来 —— 它上面的地域规则不生效', () => {
    const f = geo(node({ geoDbOk: false }))
    expect(f, 'false 是唯一该说话的那一档，而它没说').toBeDefined()
    expect(f!.tone).toBe('warn')
    // 光说「未同步」不够：人要知道后果是什么
    expect(f!.title).toContain('不生效')
  })

  it('true：什么都不显示', () => {
    expect(geo(node({ geoDbOk: true }))).toBeUndefined()
  })

  /*
   * **这一条是这三条里唯一不显然的。**
   *
   * `null` = 主控自己没有库 = 这套系统没在用地域功能。它跟 `true` 一样安静，
   * 但理由完全不同：`true` 是「查过了，一致」，`null` 是「没什么可查」。
   *
   * 而同一个 EdgeNode 上 `reconnects1h` 的 `null` 是相反的处理 ——
   * 那个必须说出来（「数不出来，不是 0」）。照着那条的模式写这一条，
   * 得到的就是一个天天报假警的界面。
   */
  it('null（主控自己没有库）：什么都不显示 —— 不是 false', () => {
    expect(
      geo(node({ geoDbOk: null })),
      'null 被当成了「库缺失」—— 没在用这功能的系统会每台节点标一条红',
    ).toBeUndefined()
  })
})

/**
 * 「拦了多少」三档。
 *
 * 这是同一个 `EdgeNode` 上第三个 `null`，而三个的处置各不相同：
 * `reconnects1h` 要说出来、`geoDbOk` 要闭嘴、这一个要说出来。
 * **照着 `geoDbOk` 的模式写这一条会得到一个在挨打时闭嘴的界面。**
 */
describe('blockedNote 三档', () => {
  it('有数：说出来', () => {
    expect(blockedNote(node({ blocked1h: 12480 }))).toContain('12480')
  })

  /*
   * **`null` 不能显示成 0。**
   *
   * 它要回答的是「此刻在不在被打」，而 `0` 正是「没被打」的样子 ——
   * 拿 0 当兜底，等于让它在自己失效的那一刻伪装成它最想否定的那个状态。
   */
  it('null：说「还没有」，不能说成 0', () => {
    const s = blockedNote(node({ blocked1h: null }))!
    expect(s, 'null 被显示成了 0 —— 一台正在挨打的机器看起来很太平').not.toMatch(/\b0\b/)
    expect(s).toContain('还没有')
  })

  /**
   * **`0` 那一档最需要解释。**
   *
   * 处置方式是 `abort`（静默断连）时，被拦的请求**不产生任何响应** ——
   * Caddy 按状态码的计数里没有它。所以一条黑名单规则拦得很起劲，
   * 这个数照样是 0。
   *
   * 不说的话，人会据此以为规则没生效、跑去查规则 ——
   * **而真正该看的是那条路由的处置方式**。
   */
  it('0：要说清「静默断连」那一档数不进来', () => {
    const s = blockedNote(node({ blocked1h: 0 }))!
    expect(s, '0 被说成了「没人来打」—— 而 abort 那一档根本数不到').toContain('静默断连')
  })

  /*
   * 三档的措辞必须两两不同 —— 否则上面三条里有的只是在验同一句话。
   */
  it('三档说的不是同一句话', () => {
    const a = blockedNote(node({ blocked1h: 5 }))
    const b = blockedNote(node({ blocked1h: 0 }))
    const c = blockedNote(node({ blocked1h: null }))
    expect(new Set([a, b, c]).size, '有两档共用了一句话').toBe(3)
  })
})

/**
 * **「未分配权重」：判据是 `weightSet`，不是权重值。**
 *
 * 用户拍板了不自动配权重（哪台机器接哪条线的流量是调度决定 —— 一台只想跑
 * 境外的机器不该默认接电信访问者），**但界面必须提示还没做这个决定**。
 *
 * 而「还没做决定」与「决定是 0」在权重值上一模一样，只有 `weightSet` 分得开。
 */
describe('未分配权重', () => {
  const has = (n: EdgeNode) => nodeFlags(n, true).some((f) => f.text.includes('未分配权重'))

  it('从没有人配过：标出来', () => {
    expect(has(node({ weightSet: false }))).toBe(true)
  })

  /**
   * **人看过、给了 0：什么都不标。**
   *
   * 这一条是这一组里唯一不显然的 —— 也是这个字段存在的全部理由。
   * 按 `weight === 0` 判的话它会亮，而那是一台**人已经决定过**的机器：
   * 一条天天亮着的警告，人两天就学会忽略它，连带着忽略掉真该看的那一条。
   */
  it('人配过（哪怕给的是 0）：什么都不标', () => {
    expect(
      has(node({ weightSet: true })),
      '按权重值判了 —— 人已经决定过的机器会常年挂着这条警告',
    ).toBe(false)
  })

  /*
   * **人为下线的不标**：那台机器现在本来就不该接流量，权重不是此刻要处理的事。
   * 标上去等于把一件已经处理好的事重新摆到人面前。
   */
  it('已下线（人为）：不标', () => {
    expect(has(node({ weightSet: false, drainedAt: '2026-08-25T00:00:00+08:00' }))).toBe(false)
  })

  /*
   * 而**故障离线照标** —— 那是故障不是意图，机器恢复之后它仍然需要一个权重。
   * 这一条挡的是「顺手把 status down 也一起排除掉」。
   */
  it('故障离线：照标 —— 那是故障不是意图', () => {
    expect(has(node({ weightSet: false, status: 'down', online: false }))).toBe(true)
  })
})
