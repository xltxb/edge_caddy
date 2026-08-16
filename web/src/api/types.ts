export interface Node {
  id: string
  city: string
  vendor: string
  line: string
  ip: string
  /** ok / warn / down */
  status: string
  /** 节点上报的配置版本 */
  cfg: string
  dns: boolean
  /** 与当前基线不一致。只比对版本号，不检查配置内容（ADR-0002）。 */
  drifted: boolean
  last_hb: string
  /**
   * 负载三项来自 **WS 心跳帧**，不来自 /nodes——列表接口只给静态信息与状态。
   * 因此它们是可选的：刚载入、还没收到心跳时它们不存在，store 会补 0。
   */
  cpu?: number
  mem?: number
  conns?: number
}

export interface NodesResponse {
  nodes: Node[]
  baseline: string
  drifted: string[]
}

/** WS 帧（前端文档 §6）。 */
export interface Frame {
  type: string
  data: Record<string, unknown>
}

export interface Route {
  domain: string
  upstream: string
  block: 'abort' | '403' | '404'
  mtls: boolean
  compress: boolean
  body_max: string
  wl: string[]
  /** 0 表示尚未下发到任何节点 */
  ver: number
}

export interface DeployResultRow {
  node: string
  /** ok / fail */
  state: string
  /** 成功时是耗时（"31ms"），失败时是 Caddy 的原文报错 */
  res: string
}

export interface DeployResponse {
  deploy_id: number
  cfg_version: string
  results: DeployResultRow[]
}
