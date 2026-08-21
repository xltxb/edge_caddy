import { CODE, type Envelope, type ValidationError } from './types'

const BASE = import.meta.env.VITE_API_BASE ?? '/api/v1'

/** 业务失败：HTTP 200 但 code !== 0，msg 是用户可读中文。 */
export class ApiError extends Error {
  constructor(
    readonly code: number,
    message: string,
  ) {
    super(message)
    this.name = 'ApiError'
  }
}

/**
 * 校验失败（code 1002）。契约 §0.3 里唯一 data 不为 null 的失败码——
 * 前端要靠 errors 把红框落到具体输入框上，所以它不能和别的 ApiError 混在一起。
 */
export class ValidationFailed extends ApiError {
  constructor(
    message: string,
    readonly errors: ValidationError[],
  ) {
    super(CODE.VALIDATION_FAILED, message)
    this.name = 'ValidationFailed'
  }
}

/** 传输层失败：网络断了、超时、响应不是 JSON。与 ApiError 分开，两者处置不同。 */
export class TransportError extends Error {
  constructor(
    message: string,
    readonly cause?: unknown,
  ) {
    super(message)
    this.name = 'TransportError'
  }
}

let onUnauthorized: () => void = () => {}

/** 由 router 装配时注入，避免 http 层反向依赖 router。 */
export function setUnauthorizedHandler(fn: () => void): void {
  onUnauthorized = fn
}

interface RequestOptions {
  method?: string
  body?: unknown
  signal?: AbortSignal
  /**
   * 跳过全局 401 拦截。只有 `GET /auth/session` 用得上——那个端点的 401
   * 是「还没登录」这个正常结果，不是会话过期，不该触发跳转。
   */
  bypassAuthRedirect?: boolean
}

async function request<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  const { method = 'GET', body, signal, bypassAuthRedirect = false } = opts

  let res: Response
  try {
    res = await fetch(`${BASE}${path}`, {
      method,
      credentials: 'same-origin',
      headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body),
      signal,
    })
  } catch (e) {
    if (signal?.aborted) throw e
    throw new TransportError('无法连接到主控，请检查网络', e)
  }

  // 契约 §0.2：会话失效是唯一需要在这里特判的 HTTP 码。
  if (res.status === 401) {
    if (!bypassAuthRedirect) onUnauthorized()
    throw new ApiError(401, '登录已过期，请重新登录')
  }
  if (res.status === 403) {
    throw new ApiError(403, '无权限执行该操作')
  }

  let payload: Envelope<T>
  try {
    payload = (await res.json()) as Envelope<T>
  } catch (e) {
    throw new TransportError(`主控返回了无法解析的响应（HTTP ${res.status}）`, e)
  }

  /*
   * 契约 §0.2：HTTP 状态码与 code 不重复表达同一件事，所以 404 / 500 的包裹体里
   * code 仍然是 0。只判 code 会让它们走进成功分支并返回 null —— 前端于是分不清
   * 「路由写错了」和「这条资源被别人删了」，而后者用的是 code 1003。
   */
  if (!res.ok) {
    throw new ApiError(res.status, payload.msg || `请求失败（HTTP ${res.status}）`)
  }

  if (payload.code === CODE.VALIDATION_FAILED) {
    const data = payload.data as unknown as { errors?: ValidationError[] } | null
    throw new ValidationFailed(payload.msg || '配置校验未通过', data?.errors ?? [])
  }
  if (payload.code !== CODE.OK) {
    throw new ApiError(payload.code, payload.msg || `请求失败（code ${payload.code}）`)
  }
  return payload.data
}

export const http = {
  get: <T>(path: string, signal?: AbortSignal) => request<T>(path, { signal }),
  post: <T>(path: string, body?: unknown, signal?: AbortSignal) =>
    request<T>(path, { method: 'POST', body, signal }),
  put: <T>(path: string, body?: unknown, signal?: AbortSignal) =>
    request<T>(path, { method: 'PUT', body, signal }),
  del: <T>(path: string, signal?: AbortSignal) => request<T>(path, { method: 'DELETE', signal }),
  /** 只给 `GET /auth/session` 用，见 RequestOptions.bypassAuthRedirect。 */
  getBypassingAuth: <T>(path: string, signal?: AbortSignal) =>
    request<T>(path, { signal, bypassAuthRedirect: true }),
}
