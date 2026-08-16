/**
 * fetch 封装。统一响应包裹 {code, data, msg}（前端文档 §6）。
 *
 * code 非 0 时 msg 是后端给的用户可读中文，直接抛出去让调用方进 toast——
 * 前端不再翻译一遍：翻译层是错误信息失真最常见的来源。
 */
export const API_BASE = import.meta.env.VITE_API_BASE ?? '/api/v1'

export class ApiError extends Error {
  constructor(
    message: string,
    readonly code: number,
    readonly status: number,
  ) {
    super(message)
  }
}

interface Envelope<T> {
  code: number
  data: T
  msg: string
}

export async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const resp = await fetch(`${API_BASE}${path}`, {
    ...init,
    headers: { 'Content-Type': 'application/json', ...(init.headers ?? {}) },
    // 会话是 HttpOnly Cookie，必须让浏览器带上
    credentials: 'same-origin',
  })

  let body: Envelope<T>
  try {
    body = (await resp.json()) as Envelope<T>
  } catch {
    throw new ApiError(`服务端返回了非 JSON 响应（HTTP ${resp.status}）`, -1, resp.status)
  }
  if (body.code !== 0) {
    throw new ApiError(body.msg || `请求失败（HTTP ${resp.status}）`, body.code, resp.status)
  }
  return body.data
}

export const get = <T>(path: string) => request<T>(path)
export const post = <T>(path: string, body?: unknown) =>
  request<T>(path, { method: 'POST', body: body === undefined ? undefined : JSON.stringify(body) })
export const put = <T>(path: string, body?: unknown) =>
  request<T>(path, { method: 'PUT', body: body === undefined ? undefined : JSON.stringify(body) })
export const del = <T>(path: string) => request<T>(path, { method: 'DELETE' })
