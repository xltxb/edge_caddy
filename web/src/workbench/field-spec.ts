/**
 * 字段描述表的类型 —— ADR-0012 的落点。
 *
 * 工作台不写六份表单组件（路由 1 套、访问规则按 type 分 3 套、全局策略 2 套），
 * 而是一张描述表配一个渲染器。三种统一行为——**改动着色**、**错误着色**、
 * **hint 随值变化**——因此只实现一次。
 *
 * ADR-0012 提醒过 schema 驱动容易滑进 `value: any`。这里的处理是：
 * `field` 保持字符串路径（要与契约 §0.3 的 `validation.errors[].field` 对得上，
 * 那是 `spec.ips[2]` 这种点号路径），而**回调全部按资源类型定型** —— hint /
 * visible / validate 拿到的是真实的草稿类型，不是 any。类型安全放在真正会出错的
 * 地方，而不是为了给路径也套上类型而造一座类型迷宫。
 */

export interface FieldBase<T> {
  /** 点号路径，如 `upstream` / `spec.ips`。与后端回的错误路径同构。 */
  field: string
  label: string
  /** 随当前值变化的说明。设计稿里处置方式、最低 TLS 版本、HSTS 都是这种。 */
  hint?: string | ((v: T) => string)
  /** 条件字段。`rate_rps` / `rate_burst` 只在限流开启时出现。 */
  visible?: (v: T) => boolean
  /** 返回错误文案表示非法，返回 null 表示通过。 */
  validate?: (v: T) => string | null
  /**
   * 这个字段**当前的组件做不到**，返回原因文案；返回 null 表示可用。
   *
   * 与 `validate` 的区别是时机：validate 是「你填错了」，unavailable 是
   * 「这件事我们做不到」。后者必须在人动手**之前**说 —— 让人打开一个开关
   * 再被下发拒绝，是最差的时机。
   *
   * 写成函数而不是常量，因为「做不到」不等于「锁死」：库里已经是非法值时
   * （比如有人直接调 API 打开了限流），控件必须还能被操作，否则人被困在一个
   * 改不掉的非法状态里，唯一出路是去调 API —— 那等于界面把自己关在门外。
   */
  unavailable?: (v: T) => string | null

  /**
   * 分组标题。同一张表里语义分层时用，比如 TLS 策略的 8 个字段其实分两拨：
   * 主控签发参数（不下发给节点）与真正渲染进节点配置的。不分组的话，
   * 人会以为改 `email` 也要下发一次。
   */
  group?: string
}

export interface TextField<T> extends FieldBase<T> {
  kind: 'text'
  width?: string
  numeric?: boolean
}

export interface AreaField<T> extends FieldBase<T> {
  kind: 'area'
  rows: number
}

export interface SegField<T> extends FieldBase<T> {
  kind: 'seg'
  options: readonly (readonly [value: string, label: string])[]
}

export interface SwitchField<T> extends FieldBase<T> {
  kind: 'switch'
  onText: string | ((v: T) => string)
  offText: string
  /** 关闭时是否用 warning 色提示（「已停用」这类需要被看见的关闭态）。 */
  offWarn?: boolean
}

/** 域名绑定。空数组 = 未绑定，规则不生效 —— 不是「对所有域名生效」。 */
export interface ChipsField<T> extends FieldBase<T> {
  kind: 'chips'
}

export type FieldSpec<T> =
  | TextField<T>
  | AreaField<T>
  | SegField<T>
  | SwitchField<T>
  | ChipsField<T>

/** 声明一张字段表。存在的意义是把 T 绑上去，让回调里的 v 有类型。 */
export function fieldsOf<T>(specs: FieldSpec<T>[]): FieldSpec<T>[] {
  return specs
}

/* ── 路径读写 ── */

/** 按点号路径取值，`spec.ips` / `whitelist`。路径不存在时给 undefined。 */
export function getPath(obj: unknown, path: string): unknown {
  return path.split('.').reduce<unknown>((cur, key) => {
    if (cur === null || typeof cur !== 'object') return undefined
    return (cur as Record<string, unknown>)[key]
  }, obj)
}

/**
 * 按点号路径写值，返回**新对象**，不改原对象。
 *
 * 草稿是叠加在 live 上的 Partial，就地改会让「当前值」和「基线值」指向同一份
 * 引用，等值比较立刻失效 —— 而那个比较正是「改回一致就删键」的依据。
 */
export function setPath<T extends object>(obj: T, path: string, value: unknown): T {
  const [head, ...rest] = path.split('.')
  if (head === undefined) return obj
  const copy: Record<string, unknown> = { ...(obj as Record<string, unknown>) }
  if (rest.length === 0) {
    copy[head] = value
  } else {
    const child = copy[head]
    const base = child !== null && typeof child === 'object' ? (child as object) : {}
    copy[head] = setPath(base, rest.join('.'), value)
  }
  return copy as T
}

/** 该字段此刻是否可见。没写 visible 就是永远可见。 */
export function isVisible<T>(spec: FieldSpec<T>, v: T): boolean {
  return spec.visible ? spec.visible(v) : true
}

/** 求值 hint —— 它可以是常量，也可以随当前值变。 */
export function resolveHint<T>(spec: FieldSpec<T>, v: T): string {
  const h = spec.hint
  if (h === undefined) return ''
  return typeof h === 'function' ? h(v) : h
}

/**
 * 该字段此刻是否做不到。返回原因文案，或 null。
 *
 * 原因来自组件能力（官方 Caddy 没有限流模块），由产品前提决定 —— 节点跑官方包、
 * 不自建二进制，是 ADR-0001 与 ADR-0003 共同的前提。它不随部署变化，所以写在
 * 字段表里，而不是像 DNS 服务商能力那样由后端下发。
 */
export function resolveUnavailable<T>(spec: FieldSpec<T>, v: T): string | null {
  return spec.unavailable ? spec.unavailable(v) : null
}
