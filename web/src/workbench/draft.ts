/**
 * 草稿语义 —— 纯函数，与 Vue 无关，因此可以被直接测。
 *
 * 草稿是**叠加在基线之上的 Partial**（契约 §6.4）：`effective = merge(live, draft)`。
 * 最容易写错、也最要紧的一条是：**字段值改回与线上一致时必须把该键从 Partial 里删掉**。
 * 留一个等值的键不会有任何报错，只会让资源树上的蓝点和顶栏「待下发」虚报一个数字——
 * 一个静默的谎，正好长在「怕推错」这条主线上。
 */

import { normalizeLines } from '@/utils/validators'
import { getPath, setPath } from './field-spec'

export type Patch = Record<string, unknown>

function isPlainObject(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v)
}

/**
 * 值是否等价。
 *
 * 字符串数组（白名单、IP 列表）按**去空行、去首尾空白**后比较——多敲一个空行
 * 不该算作一次待下发的改动（契约 §6.4）。
 */
export function valuesEqual(a: unknown, b: unknown): boolean {
  const aStrs = Array.isArray(a) && a.every((x) => typeof x === 'string')
  const bStrs = Array.isArray(b) && b.every((x) => typeof x === 'string')
  if (aStrs && bStrs) {
    const na = normalizeLines(a as string[])
    const nb = normalizeLines(b as string[])
    return na.length === nb.length && na.every((x, i) => x === nb[i])
  }
  if (isPlainObject(a) && isPlainObject(b)) {
    const ka = Object.keys(a)
    const kb = Object.keys(b)
    if (ka.length !== kb.length) return false
    return ka.every((k) => valuesEqual(a[k], b[k]))
  }
  return a === b
}

/**
 * 合并 live 与草稿 Partial。
 *
 * 顶层浅合并，但 `spec` 这类对象值再往下合一层——否则改一个 `spec.header`
 * 就会把同一个 spec 里的其他字段整个抹掉。
 */
export function merge<T extends object>(live: T, patch: Patch | undefined): T {
  if (!patch) return live
  const out: Record<string, unknown> = { ...(live as Record<string, unknown>) }
  for (const [k, v] of Object.entries(patch)) {
    const base = out[k]
    out[k] = isPlainObject(base) && isPlainObject(v) ? { ...base, ...v } : v
  }
  return out as T
}

/**
 * 剪掉 Partial 里与 live 等值的部分，返回新的 Partial。
 *
 * 对象值逐键剪；剪空了就把这个键整个去掉。返回空对象表示「这个资源没有
 * 未下发改动」，调用方据此删掉整条草稿。
 */
export function prune(live: Record<string, unknown>, patch: Patch): Patch {
  const out: Patch = {}
  for (const [k, v] of Object.entries(patch)) {
    const base = live[k]
    if (isPlainObject(base) && isPlainObject(v)) {
      const inner = prune(base, v)
      if (Object.keys(inner).length > 0) out[k] = inner
      continue
    }
    if (!valuesEqual(base, v)) out[k] = v
  }
  return out
}

/**
 * 在草稿上写一个字段，返回剪枝后的新 Partial。
 *
 * 写完立刻剪 —— 而不是等到读的时候再算，因为 Partial 会被 `PUT /drafts/:key`
 * 整个发给后端，发出去的那一份就该是干净的。
 */
export function applyEdit(
  live: Record<string, unknown>,
  patch: Patch,
  path: string,
  value: unknown,
): Patch {
  const next = setPath(patch as object, path, value) as Patch
  // setPath 只写了路径上的那一段，其余键要能与 live 比对得上，先补齐父对象
  const [head, ...rest] = path.split('.')
  if (head !== undefined && rest.length > 0 && isPlainObject(live[head])) {
    const merged = { ...(live[head] as Record<string, unknown>), ...(next[head] as Patch) }
    next[head] = merged
  }
  return prune(live, next)
}

/** 这条草稿改了几个字段。对象值按其内部改动的键数计。 */
export function changeCount(patch: Patch | undefined): number {
  if (!patch) return 0
  return Object.values(patch).reduce<number>(
    (n, v) => n + (isPlainObject(v) ? Object.keys(v).length : 1),
    0,
  )
}

/** 该字段此刻是否与线上不同（用于输入框的改动着色）。 */
export function isFieldDirty(live: unknown, patch: Patch | undefined, path: string): boolean {
  if (!patch) return false
  const inPatch = getPath(patch, path)
  if (inPatch === undefined) return false
  return !valuesEqual(getPath(live, path), inPatch)
}
