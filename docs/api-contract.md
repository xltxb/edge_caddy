# Edge Controller — HTTP / WS 契约 v1

前后端的接缝。**这份文档是权威**：设计项目里的后端开发文档 §4 与前端开发文档 §6
冻结在 v1.0，与本文冲突处以本文为准（见 [CLAUDE.md](../CLAUDE.md) 的「权威在哪」）。

改动本文 = 改接口，必须同步另一侧的 agent。

- 基址：`/api/v1`（同源部署，前端 `VITE_API_BASE` 默认即此）
- WS：`/api/v1/ws`
- 相关：[CONTEXT.md](../CONTEXT.md) 术语表、[docs/adr/](adr/) 架构决定

---

## 0. 全局约定

### 0.1 响应包裹

一切 HTTP 响应都是这个形状，**包括错误**：

```json
{ "code": 0, "data": { }, "msg": "" }
```

- `code` 为 `0` 时成功，`data` 有效，`msg` 为空串。
- `code` 非 `0` 时 `data` 为 `null`，`msg` 是**用户可读的中文**，前端可直接进 toast。

### 0.2 HTTP 状态码与 `code` 的分工

这两者**不重复表达同一件事**：

| 层 | 用什么 | 前端怎么处理 |
|---|---|---|
| 会话 | HTTP `401` | 跳登录页（带 `?redirect=`），**唯一**需要在 http.ts 里特判的码 |
| 权限 | HTTP `403` | toast「无权限」；ops-bot 访问人类专属端点会拿到它 |
| 端点不存在 | HTTP `404` | 不该发生，报 bug |
| 未捕获异常 | HTTP `500` + 包裹体 | toast `msg`（通用文案，不泄露内部细节） |
| **业务失败** | HTTP `200` + `code != 0` | toast `msg`；`1002` 另有结构化 `errors`，见 §0.3 |

**资源不存在用 `code: 1003`，不用 HTTP 404。** 404 只表示「这个 URL 后端没实现」，
把两者混在一起会让前端分不清「路由写错了」和「这条路由被别人删了」。

> **前端务必注意**：401 / 403 / 404 / 500 的包裹体里 `code` **是 0**——它们用 HTTP
> 状态码表达，不重复用 `code`。因此**不要只判 `code !== 0` 来决定成败**，那会让
> 404 走进成功分支并返回 `null`。判据是「先看 `res.ok`，再看 `code`」。
> （这条是前端 agent 在接真 master 时踩出来的，不是假想。）

**未实现的端点不注册，也不给返回空数据的桩。** 桩会被读成「还没有节点」，
而 404 说得出「这个端点还没做」——两者的处置完全不同。

### 0.3 `code` 取值

| code | 含义 | 典型场景 |
|---|---|---|
| `0` | 成功 | |
| `1001` | 参数格式错 | 域名不合法、`upstream` 不是 `host:port` |
| `1002` | **校验失败**（带结构化 `errors`） | Go 层渲染前校验不通过 |
| `1003` | 资源不存在 | `PUT /routes/nope.com` |
| `1004` | 资源冲突 | 新建路由重名 |
| `2001` | 状态冲突 | 对已下线节点推配置 |
| `3001` | 下游服务失败 | DNS 服务商 / Lark / ACME 返回错误 |
| `3002` | 节点不可达 | 探活超时 |

`1002` 的 `data` **不为 null**，这是唯一的例外——前端需要它把错误定位到具体输入框：

```json
{
  "code": 1002,
  "msg": "配置校验未通过，共 2 处问题",
  "data": {
    "errors": [
      { "res_key": "route:api.example.com", "field": "upstream",  "reason": "回源地址必须形如 host:port" },
      { "res_key": "rule:office-wl",        "field": "spec.ips[2]", "reason": "10.8.0.0/33 不是合法的 CIDR" }
    ]
  }
}
```

`field` 用点号路径，数组下标用 `[n]`，与前端表单的字段路径一一对应。

### 0.4 类型约定

- **时间**：RFC3339 带时区偏移，字段名以 `_at` 结尾。例：`"2026-08-21T10:42:07+08:00"`
- **时长**：整数毫秒，字段名以 `_ms` 结尾。例：`"hb_age_ms": 1200`
- **百分比**：0–100 的浮点，一位小数。例：`"cpu": 15.2`
- **枚举**：小写 snake_case 字符串，取值在各端点处列全。
- `null` 表示「没有这个值」，不用空串或 `0` 代替。

### 0.5 分页

倒序追加的流（审计、下发记录）用 **cursor**，不用 offset——它们在你翻页时还在往头上加，
offset 会漏行或重复。

请求：`?limit=50&before_id=1837`（首页不传 `before_id`）
响应：`{ "items": [...], "next_before_id": 1787 }`，`next_before_id` 为 `null` 表示到底了。

### 0.6 鉴权

- **人**：`POST /auth/login` 换 HttpOnly + SameSite=Strict Cookie（`ec_session`）。
- **ops-bot**：`Authorization: Bearer <static-token>`，token 在系统设置里配。
- WS 复用同一个 Cookie，握手时校验；未登录直接 `401`，不升级。

控制台自身的准入是「只绑内网 + 会话 Cookie + 全写审计」，mTLS 是默认关的开关，
见 [ADR-0013](adr/0013-console-access-is-network-plus-session.md)。

---

## 1. 会话

### `POST /auth/login`

```json
// 请求
{ "username": "abiu", "password": "……" }
// 响应 data
{ "username": "abiu", "kind": "human" }
```

失败返回 HTTP `200` + `code: 1001`、`msg: "用户名或密码错误"`——**不区分**用户名不存在
与密码错误。成功与失败都写审计（含来源 IP），失败的登录在审计页单独提示。

### `POST /auth/logout`

无请求体，`data` 为 `null`。清 Cookie，写审计。

### `GET /auth/session`

前端启动时调它决定是否跳登录（后端文档 §4 没列这个端点，是前端 store 的实际需要）。

```json
// data —— 已登录
{ "username": "abiu", "kind": "human" }
```

未登录返回 HTTP `401`。这是唯一一个「401 是正常结果」的端点，前端在这里不要跳转，
只把 session 标记为未登录。

---

## 2. WebSocket `/api/v1/ws`

三类帧，与前端开发文档 §6 一致。**没有初始快照帧**——首屏数据走 REST
（`GET /overview` + `GET /nodes`），WS 只送增量。这样首屏不依赖 WS 建连速度，
也不需要维护两条产出同样数据的代码路径。

帧一律是 `{ "type": …, "data": … }`，服务端单向推送，客户端不发业务帧。

### `heartbeat` —— 每节点每个心跳周期一帧（默认 3s）

```json
{ "type": "heartbeat", "data": {
  "id": "node-hk-01", "status": "ok",
  "cpu": 15.2, "mem": 32.8, "conns": 12400,
  "hb_age_ms": 40, "cfg_version": "cfg-2f9a1c",
  "routes": 7, "rules": 3
} }
```

- `hb_age_ms` 是**服务端计算的、距上次心跳的毫秒数**。在刚到达的帧里它接近 0；
  前端收到后从这个值开始本地计时，显示「心跳 3.0s 前」。不要用浏览器时钟减 `last_hb_at`——
  会被时钟偏差污染。
- `routes` / `rules` 是**该节点当前生效配置里**的数量，由 Agent 上报，不是全局数量。
  漂移的节点会显示旧的数字，这正是它有用的地方。

### `event` —— 事件流

```json
{ "type": "event", "data": {
  "id": 4127, "at": "2026-08-21T10:42:07+08:00",
  "node": "node-us-01", "kind": "crit",
  "msg": "心跳连续超时 3 次，已自动暂停 DNS 解析"
} }
```

`kind`：`ok` | `info` | `warn` | `crit`，**四档**。`node` 可为 `null`（系统级事件）。

`ok` 不是 `info` 的近义词，两者的分工是事件流的设计意图：**`ok` = 成功完成的动作**
（「Caddy 热重载成功，耗时 31ms」「配置 cfg-2f9a1c 下发完成，4/6 节点」），
`info` = 流水账（「回源 rtt 41ms」）。塞进同一档会让下发成功和背景噪音同色。

### `deploy_progress` —— 下发逐节点进度

```json
{ "type": "deploy_progress", "data": {
  "deploy_id": 81, "cfg_version": "cfg-9b31e7",
  "node": "node-tw-01", "state": "fail",
  "detail": "deadline exceeded", "retrying": true
} }
```

`state`：`wait`（待下发）| `run`（热重载中）| `ok` | `fail`。四态与设计稿一一对应。

> **`ok` 的含义是「Caddy 接受了这份配置」，不是「流量正在被服务」。** 2026-08-21 在
> Caddy 2.11.4 上实测发现：端口被别的进程占用时 Caddy 返回 **200**、配置里有那个
> server、日志一条 error 都没有，而它收不到任何流量（详见
> [ADR-0004](adr/0004-no-master-side-caddy-validate.md) 的复核一节）。
> 界面措辞不要把 `ok` 说成「已生效」——这与「配置漂移只比对版本号」是同一类问题：
> 一个听起来更强的承诺，兑现不了。
注意这**不是**数据库里的 `deploy_state`——那个枚举只有 `ok` / `fail` 两个终态，
`wait` / `run` 是过程态，只存在于线上帧里，不落库。
- `ok` 时 `detail` 是耗时字符串，如 `"31ms"`。
- `fail` 时 `detail` 是原因。**`retrying` 决定这一行还会不会再动**：
  节点未回应（超时 / 连接断开）→ `retrying: true`，后面还会有帧；
  节点回应了但 Caddy 拒绝 → `retrying: false`，`detail` 是 Caddy 的原文报错，这一行到此为止。
  见 [ADR-0005](adr/0005-retry-only-transport-failures.md)。

### 断线降级

WS 断开时前端按指数退避重连；重连期间对进行中的下发降级为每 2s 轮询
`GET /deploys/:id`（§7.4），其字段与本帧一一对应。

---

## 3. 总览

### `GET /overview`

```json
// data
{
  "baseline": "cfg-2f9a1c",
  "kpi": {
    "nodes_online":  5,   "nodes_total": 6,
    "conns_total":   48200,
    "conns_delta_pct": 12.4,
    "origin_rate":   8.7,
    "drift_nodes":   1
  },
  "events": [ { "id": 4127, "at": "…", "node": "node-us-01", "kind": "crit", "msg": "…" } ]
}
```

- `baseline` 是**当前基线**的版本号，顶栏常驻显示。放在顶层而不是 `kpi` 里——
  它不是一个指标，是全局上下文。

  **前端反推不出来，必须由后端给。** 从各节点上报的 `cfg_version` 取众数看着可行，
  但那在「一次下发只到了少数节点」时会指向旧版：6 个节点里 2 个是新版、4 个落后，
  众数给出旧版，于是 `drift_nodes` 会算成 2 而真相是 4——**方向正好反了**。
  基线的定义是「最近一次成功下发确立的那一版」（CONTEXT.md），只有主控知道。
  `drift_nodes` 也以它为准，两者同源才不会互相打架。
- `conns_delta_pct` 是**较昨日同时段**的连接数变化百分比，可为负，**可为 `null`**
  （历史不足 24 小时时——冷启动后的第一天就是这种情况，前端要按空态处理而不是显示 0%）。
  数据来自 `traffic_samples` 表：每分钟一行全局聚合，保留 7 天，约 1440 行/天。
  这是唯一一处需要落库的时序数据；节点级的 `cpu_series` 仍然只在内存里（§4）。
- `origin_rate` 是**回源率**百分比：**到达 upstream 的请求 ÷ 边缘收到的总请求**。
  越低越好，前端按「高于阈值转 warning」着色。

  > **注意它不是缓存命中率。** 设计稿的脚注写着「静态缓存承载 91.3% 请求」——
  > 那个说法在本架构下不成立：**官方 Caddy 没有 HTTP 缓存模块**（`reverse_proxy`
  > 不缓存，能做这件事的 `caddy-cache-handler` / Souin 是第三方插件）。而「节点跑
  > apt 装的官方 Caddy」正是 [ADR-0001](adr/0001-master-issues-certificates.md) 与
  > [ADR-0003](adr/0003-edge-auth-via-agent-forward-auth.md) 共同的前提，装插件要
  > 连着推翻这两条。
  >
  > 重定义后这个数字回答的是**边缘挡掉了多少**：没到达 upstream 的那部分，是被访问
  > 规则拦下（静默断连 / 403 / 404）或由静态响应处理掉的。脚注应改为「边缘拦截 N%」。
- `drift_nodes` 是**配置漂移**节点数：`cfg_version != 基线` 的节点计数。
  **它只比对版本号，不检查节点上的配置内容**——见
  [ADR-0002](adr/0002-drift-is-version-comparison.md)。界面上必须说清这个局限，
  「全部一致」的含义只是「最近一次下发都到达了」，不是「没人 SSH 上去改过」。
- `events` 是最近 40 条，与 WS `event` 帧同构，供首屏铺底。

---

## 4. 边缘节点

### `GET /nodes`

```json
// data.items[]
{
  "id": "node-hk-01",
  "city": "香港", "vendor": "DMIT PPro", "line": "CN2 GIA",
  "public_ip": "203.0.113.7",
  "status": "ok",
  "cpu": 15.2, "mem": 32.8, "conns": 12400,
  "cpu_series": [12,14,13,18,15,15,16,14,13,15,15,15],
  "last_hb_at": "2026-08-21T10:42:05+08:00",
  "hb_age_ms": 1200,
  "cfg_version": "cfg-2f9a1c",
  "drift": false,
  "dns_enabled": true,
  "routes": 7, "rules": 3,
  "created_at": "2026-08-01T09:00:00+08:00"
}
```

- `status`：`ok` | `warn` | `down`。
- `cpu_series` 是 **12 个点的 CPU 百分比整数，最新在末尾**，点间隔 = 心跳周期。
  数据存在主控**进程内存的环形缓冲**里，**不落库**——心跳是纯粹易失的数据，
  离线判定用的是 `last_hb_at` 与连续超时计数，不是这个序列。主控重启后它会空几十秒，
  前端按 `null` 处理（画一条平线或留白，别报错）。补齐之后前端用 WS `heartbeat` 帧
  自行往后追加。
- `drift` = `cfg_version != 当前基线`，与 `GET /overview` 的 `drift_nodes` 同源。

### `GET /nodes/:id/logs`

```json
// data.items[] —— 最近 200 条，倒序
{ "at": "2026-08-21T10:41:58+08:00", "level": "info", "msg": "config applied cfg-2f9a1c in 31ms" }
```

`level`：`debug` | `info` | `warn` | `error`。

### `POST /nodes/token` —— 签发一次性接入 Token

```json
// 请求 —— Token 在签发时就绑定这台机器的身份
{ "node_id": "node-sg-01", "city": "新加坡", "vendor": "V.PS", "line": "CMIN2", "public_ip": "203.0.113.9" }
// data
{
  "token": "ec_1f9a…（仅此一次可见）",
  "expires_at": "2026-08-21T11:12:07+08:00",
  "install_cmd": "curl -fsSL https://ec.internal/install.sh | sudo bash -s -- --token ec_1f9a… --master ec.internal:9000"
}
```

Token **30 分钟 TTL、单次使用**，用后即失效。节点凭它完成首连并换取隧道客户端证书，
此后全部走 mTLS——见 [ADR-0009](adr/0009-internal-pki-two-cas.md)。
`token` 只在这一次响应里出现，任何后续接口都不回显。

### `POST /nodes/:id/push` —— 把当前基线重推给单个节点

`data`：`{ "deploy_id": 82, "cfg_version": "cfg-2f9a1c" }`，进度走 WS。
对已下线节点返回 `code: 2001`。

这个端点是 [ADR-0005](adr/0005-retry-only-transport-failures.md) 的兜底：Caddy 拒绝的
配置不自动重试，环境类临时故障由人在这里手动恢复。

### `POST /nodes/:id/dns` —— 解析开关

```json
// 请求
{ "enabled": false }
// data
{ "id": "node-hk-01", "dns_enabled": false, "weights_rebalanced": true }
```

关闭后该节点退出解析，其余节点的权重在各线路内**重新归一化**。写审计。

### `POST /nodes/:id/probe` —— 探活

```json
// data —— 成功
{ "reachable": true,  "rtt_ms": 38, "caddy_admin": true,  "cfg_version": "cfg-2f9a1c" }
// data —— 失败（HTTP 200 + code 3002）
null
```

`caddy_admin` 是节点本机 `127.0.0.1:2019` 的可达性，与隧道可达性分开报——
隧道通而 Admin 不通，说明 Caddy 挂了而 Agent 还活着，这两种故障的处置完全不同。

### `POST /nodes/:id/drain` —— 下线

```json
// 请求 —— 必须显式确认，防止误点
{ "confirm": true }
// data —— 三步的执行结果
{ "steps": [
  { "step": "dns_removed",   "ok": true },
  { "step": "conns_drained", "ok": true, "detail": "等待 12400 连接结束，耗时 8.2s" },
  { "step": "tunnel_closed", "ok": true }
] }
```

---

## 5. 措辞：审计动作与事件文案照术语表

`audit_logs.action` 与 `event.msg` 由后端产生、在前端页面上**原样显示**，所以它们的措辞
是契约的一部分，不是实现细节。

一律用 [CONTEXT.md](../CONTEXT.md) 的术语表：**「下发」**，不是推送 / 发布 / 部署。

| 动作 | `action` 取值 |
|---|---|
| 下发配置 | `下发配置` |
| 回滚到某版本 | `回滚配置` |
| 新建 / 修改 / 删除路由 | `新建路由` / `修改路由` / `删除路由` |
| 修改访问规则 | `修改访问规则` |
| 修改全局策略 | `修改全局策略` |
| 调整解析权重 | `调整解析权重` |
| 节点解析开关 | `暂停解析` / `恢复解析` |
| 节点下线 | `下线节点` |
| 签发接入 Token | `签发接入Token` |
| 证书续期 | `续期证书` |
| 修改系统设置 / 告警 | `修改系统设置` / `修改告警设置` |
| 发送告警测试 | `发送告警测试` |
| 登录 / 登出 | `登录` / `登出` |

---

## 6. 配置资源

三类资源共用一套草稿机制（§6.4），`res_key` 格式：
`route:<domain>` / `rule:<id>` / `global:<id>`。

### 6.1 反代路由

`GET /routes` → `data.items[]`；`PUT /routes/:domain`；`DELETE /routes/:domain`。

```json
{
  "domain": "api.example.com",
  "upstream": "10.8.0.12:8080",
  "block_mode": "abort",
  "mtls": true,
  "compress": true,
  "body_max": "5MB",
  "whitelist": ["203.0.113.7", "10.8.0.0/24"],
  "version": 7
}
```

- `block_mode`：`abort`（静默断连，默认）| `403` | `404`。选 `403` 会暴露服务存在，
  前端应给出这条提示。术语用**处置方式**。
- `mtls` 是**回源 mTLS**：边缘节点回源时出示 `edge-mtls` 客户端证书，由源站校验。
  **不是**「要求访问者出示证书」——两者方向相反，见
  [ADR-0008](adr/0008-route-mtls-is-upstream-client-cert.md)。UI 文案不要单说「mTLS」。
- `body_max` 在 API 上是**人类可读字符串**（`"5MB"`）。真实 Caddy 的 `max_size` 要
  int64 字节数，这个转换由后端渲染器做——前端不要自己转，也不要把这个字符串当成
  可下发的值（这正是 [ADR-0007](adr/0007-workbench-preview-is-a-representation.md)
  里举的那个例子）。
- `version` 为 `0` 表示**尚未下发到任何节点**，右栏应整块显示为新增。

`POST /routes` 是新建向导，额外校验：域名格式、`upstream` 形如 `host:port`、重名
（重名返回 `code: 1004`）。创建后前端跳工作台并选中 `route:<domain>`。

`DELETE /routes/:domain` **联动**把该域名从所有 `access_rules.apply_to` 里摘掉，
响应带上受影响的规则供前端提示：

```json
{ "deleted": "api.example.com", "unbound_rules": ["office-wl", "svc-key-1"] }
```

### 6.2 访问规则

`GET /rules` → `data.items[]`；`PUT /rules/:id`。

```json
{ "id": "office-wl", "name": "办公网白名单", "type": "ip_whitelist",
  "enabled": true, "apply_to": ["api.example.com"], "version": 3,
  "spec": { "ips": ["203.0.113.7", "10.8.0.0/24"] } }
```

`type` 决定 `spec` 的形状，三选一：

```json
"ip_whitelist"   → { "ips": ["203.0.113.7", "10.8.0.0/24"] }
"service_secret" → { "header": "X-Service-Key", "algo": "hmac-sha256",
                     "ttl_s": 300, "replay_protection": true }
"jwt_bearer"     → { "iss": "https://idp.internal/", "aud": "edge",
                     "jwks_url": "https://idp.internal/.well-known/jwks.json", "skew_s": 60 }
```

**`apply_to` 为空数组的规则不生效**——那是半成品状态，不是「对所有域名生效」。
前端应把它显示为未绑定，不要显示为全局生效。

后两种类型的验签**不由 Caddy 做**：Caddy 用 `forward_auth` 委托给 Agent 在回环上的
校验端点，由 Agent 用 Go 真正验签，并把声明透传给源站（见
[ADR-0003](adr/0003-edge-auth-via-agent-forward-auth.md)）。这对前端不可见，但决定了
`spec` 里能出现哪些字段——不要照 caddy-jwt 插件的字段名设计表单，我们不装那个插件。

### 6.3 全局策略

`GET /policies/:id` / `PUT /policies/:id`，`id` 取 `tls` | `log` 两个。

```json
{ "id": "tls", "name": "TLS 策略", "version": 4, "spec": { } }
```

字段清单由前端从高保真设计稿的 `wbFieldsFor` 抄出，后端照它渲染。

**`global:tls`**

| field | 类型 | 取值 | seed |
|---|---|---|---|
| `ca` | enum | `letsencrypt` \| `zerossl` | `letsencrypt` |
| `email` | string | ACME 账户邮箱 | `ops@example.com` |
| `key_type` | enum | `p256` \| `p384` \| `rsa2048` | `p256` |
| `min_version` | enum | `1.2` \| `1.3` | `1.2` |
| `http3` | bool | 开启需放行 443/udp | `true` |
| `hsts` | bool | | `true` |
| `hsts_max_age` | int | 秒，仅在 `hsts` 开启时有意义 | `63072000` |
| `ocsp` | bool | OCSP Must-Staple | `false` |

> `ca` / `email` / `key_type` 是**主控**签发证书时用的参数（certmagic 跑 DNS-01），
> **不下发给节点**——节点跑官方 Caddy，不自己申请证书（ADR-0001）。设计稿里那句
> 「Caddy 全生命周期自动申请与续期」是旧说法，前端已改文案。
> `min_version` / `http3` / `hsts` / `ocsp` 才是真正渲染进节点配置的。

**`global:log`**

| field | 类型 | 取值 | seed |
|---|---|---|---|
| `format` | enum | `json` \| `console` | `json` |
| `level` | enum | `DEBUG` \| `INFO` \| `WARN` \| `ERROR` | `INFO` |
| `roll_size` | int | MB | `50` |
| `roll_keep` | int | 保留文件数 | `5` |
| `strip_headers` | bool | 移除 `Server` / `X-Powered-By` | `true` |
| `rate_limit` | bool | 按来源 IP 限流 | `true` |
| `rate_rps` | int | **条件字段**，见下 | `200` |
| `rate_burst` | int | **条件字段**，见下 | `400` |

> `rate_rps` / `rate_burst` 只在 `rate_limit = true` 时出现在表单里，因此
> `rate_limit = false` 时这两个键**可能根本不存在**。渲染器不要假定它们一定在，
> 也不要在关闭限流时给它们填默认值再渲染——那会让 diff 里凭空多出两行。

### 6.4 草稿

**草稿是叠加在基线之上的 Partial**，`effective = merge(live, draft)`。草稿**全局可见**，
任何人都能看到别人正在改什么。

```
GET    /drafts          → { "items": { "route:api.example.com": { "upstream": "10.8.0.13:8080" },
                                        "rule:office-wl":        { "spec": { "ips": [...] } } },
                            "updated": { "route:api.example.com": { "by": "abiu", "at": "…" } } }
PUT    /drafts/:key     ← 整个 Partial（不是单字段增量），后写覆盖
DELETE /drafts          → 放弃全部草稿
```

- **字段值改回与线上一致时，前端必须从 Partial 里删掉该键**，不要留一个等值的键——
  否则 `changeCount` 和资源树上的蓝点会虚报。多行文本按去空行规范化后比较。
- Partial 为空对象时后端**删除**该草稿行，等价于「这个资源没有未下发改动」。
- 并发是**后写覆盖**（单人系统 + ops-bot，不做乐观锁）。`updated.by/at` 回给前端，
  用于在别人刚改过时给一个提示。

---

## 7. 下发

流水线：草稿 → Go 层校验 → 确认弹层（权威 diff + 目标节点）→ 广播 → 逐节点热重载并回报
→ 全部落定后确立新基线。见 [ADR-0004](adr/0004-no-master-side-caddy-validate.md)、
[ADR-0005](adr/0005-retry-only-transport-failures.md)。

### 7.1 `POST /deploys/preview` —— 权威渲染与预校验

后端开发文档 §4 漏了这个端点。它同时是 **dry-run**：Go 层校验在这里就跑，
所以确认弹层能在广播之前把校验失败一并暴露。

```json
// 请求
{ "res_keys": ["route:api.example.com", "rule:office-wl"] }
// data
{
  "before": "{\n  \"apps\": {\n    \"http\": …",
  "after":  "{\n  \"apps\": {\n    \"http\": …",
  "baseline": "cfg-2f9a1c",
  "targets": [ { "id": "node-hk-01", "status": "ok" }, { "id": "node-us-01", "status": "down" } ],
  "validation": { "ok": true, "errors": [] }
}
```

- `before` / `after` 都是**后端渲染的字节全文**，前端用自己的 LCS 算 diff。
  权威性来自「两份都是后端渲染的」，不来自谁算的 diff。
- `validation.errors` 与 §0.3 的 `1002` 同构（`res_key` / `field` / `reason`），
  这样工作台能把错误落到具体输入框。`ok: false` 时前端禁用下发按钮。
  注意：**校验失败在这里返回 `code: 0`**——预览成功地告诉了你「校验没过」，
  这不是请求失败。只有 `POST /deploys` 才用 `1002` 拒绝。
- `targets` 是本次会广播到的节点及其当前状态，供弹层显示「下发到 N 个节点」。
  预览**不要求**有在线节点——它是 dry-run，`targets` 为空数组是合法结果。
- `baseline` 是 `before` 所代表的那一版，即当前基线。

  > **本端点不返回 `cfg_version`。**（早先的契约里有，那是错的，已删。）
  > 新版本号是在 `POST /deploys` 那一刻生成的，预览时给出一个只会与实际下发不符的
  > 号码，正是我们一直在拦的那类「界面给出兑现不了的承诺」。弹层要显示版本递增时，
  > 写「基线 cfg-2f9a1c → 新版本（下发时生成）」，不要编一个号出来。
- **`before` / `after` 都不包含 `apps/tls`**（内联证书段）。私钥不进浏览器，
  且证书不是草稿资源。弹层底部必须标明「证书段由主控自动附加，不在此 diff 中」——
  见 [ADR-0007](adr/0007-workbench-preview-is-a-representation.md) 的补充。

### 7.2 `POST /deploys` —— 校验并下发

```json
// 请求 —— 只带本次勾选的草稿
{ "res_keys": ["route:api.example.com"] }
// data
{ "deploy_id": 82, "cfg_version": "cfg-9b31e7", "targets": ["node-hk-01", "node-us-01"] }
```

校验不过返回 `code: 1002` + 结构化 `errors`，**不触达任何节点**。
成功后进度全部走 WS `deploy_progress`（§2）。未勾选的草稿仍然是草稿。

### 7.3 `GET /deploys` —— 下发记录

cursor 分页（§0.5）。

```json
// data.items[]
{
  "id": 82, "cfg_version": "cfg-9b31e7",
  "operator": "abiu",
  "res_keys": ["route:api.example.com"],
  "ok_count": 5, "fail_count": 1,
  "is_baseline": true,
  "created_at": "2026-08-21T10:42:07+08:00"
}
```

`is_baseline` 为 `true` 的那一条是当前基线，**不可回滚**（前端应禁用该行的回滚按钮）。

### 7.4 `GET /deploys/:id` —— 单次详情

列表项的全部字段，另加逐节点结果。WS 断线时前端每 2s 轮询它降级。

```json
{
  "…列表项字段…": null,
  "targets": ["node-hk-01","node-us-01","node-tw-01","node-jp-01","node-kr-01","node-de-01"],
  "target_count": 6,
  "phase": "running",
  "results": [
    { "node": "node-hk-01", "state": "ok",   "detail": "31ms",              "retrying": false },
    { "node": "node-tw-01", "state": "fail", "detail": "deadline exceeded", "retrying": true  }
  ]
}
```

`phase`：`running` | `done`。`results[]` 的字段与 WS `deploy_progress` 帧一一对应，
所以前端的 `PushProgress` 组件两条数据源可以共用一套渲染。

**`phase` 的判据是「`results` 覆盖了全部 `target_count` 个节点，且没有一条
`retrying: true`」——不是「有节点回报过」，也不是「回报数 == 目标数」。**

> **`6/6` 不等于结束了。** 重试中的节点已经回报过一次失败，但它那一行还会再变。
> 把它算成终态会让确认弹层提前落定，而用户以为下发已经收尾。
> （这条是前端 agent 在 mock 上撞出来的：让失败节点恒为 `retrying: true`，
> 弹层就永远不落定——反过来说明落定条件必须同时看这两个量。）

**结果是逐节点到达即写入**，不是攒到全部结束再写。攒到最后会让这个端点在整个
下发过程中什么都返回不了，而它正是 WS 断线时的降级路径（§2）——那恰恰是用户
最需要被告知的时刻。

**`targets` 给出的是「是哪几个节点」，不只是几个。** 前端**不要**用 `results`
整体替换已有的行——进行中轮询拿到的是部分结果，整体替换会把还没回报的节点整行
抹掉，于是「还有谁没回来」这个信息消失，而那正是降级时最需要看见的。
正确做法是以 `targets` 为骨架、按 node id 把 `results` 合并进去。

> 用户在下发进行中**刷新页面**时，前端手上没有 `POST /deploys` 那次响应里的
> 目标列表。只有 `targets` 落库并从这里返回，那几行「待下发」才画得出来。
> `target_count` 是 `len(targets)`，库里只存前者——两份记同一件事迟早会不一致。

### 7.5 `POST /deploys/:cfg_version/rollback` —— 回滚

**回滚不直接下发**。它读该版本的快照与当前基线逐资源比对，把差异**写回草稿**，
由人在工作台确认 diff 后走同一条流水线。回滚同样过校验、同样留审计。

```json
// data —— 写回了哪些草稿，前端据此跳工作台
{ "res_keys": ["route:api.example.com", "rule:office-wl"] }
```

---

## 8. DNS 调度

### `GET /dns/weights` / `PUT /dns/weights`

按线路分组。线路码固定五个：`ct`（电信）`cu`（联通）`cm`（移动）`tw`（台湾）`ov`（境外）。

```json
// data
{ "lines": [
  { "code": "ct", "name": "电信", "entries": [
      { "node": "node-hk-01", "weight": 60, "share": 60.0, "dns_enabled": true,  "status": "ok" },
      { "node": "node-us-01", "weight": 40, "share": 0.0,  "dns_enabled": false, "status": "down" }
  ] }
] }
```

- `weight` 是**配置值**，`share` 是**实际占比**。两者不同：`dns_enabled: false` 的节点
  （手动暂停或心跳超时自动摘除）`share` 为 `0`，其权重在该线路内的其余节点间**重新归一化**。
  前端的占比条画 `share`，输入框绑 `weight`。
- `PUT` 后**立即**调 DNS 服务商，失败返回 `code: 3001` 且不落库。写审计。

---

## 9. 证书

证书由**主控**集中签发（certmagic 跑 **DNS-01**）并经隧道内联下发，边缘节点不持有
DNS 凭据、不自行申请——见 [ADR-0001](adr/0001-master-issues-certificates.md)、
[ADR-0010](adr/0010-cert-distribution.md)。

> 设计稿 seed 里 `challenge` 写着 `HTTP-01` 的地方都是错的：域名按权重只解析到部分节点，
> 轮换外的节点无法完成 HTTP-01 校验，而节点恰恰需要在**进入轮换之前**就持有证书。

### `GET /certs`

```json
// data.items[]
{
  "domain": "api.example.com",
  "issuer": "Let's Encrypt",
  "challenge": "dns-01",
  "auto_renew": true,
  "not_after": "2026-10-19T08:00:00+08:00",
  "days_left": 59,
  "expected_nodes": 6,
  "loaded_nodes": 5,
  "missing_nodes": ["node-tw-01"]
}
```

**两列真相**，这是本端点最要紧的地方：

- `expected_nodes` 是**主控账面**——主控签发了它，应当覆盖这么多节点。
- `loaded_nodes` / `missing_nodes` 是**节点回执**——Agent 上报的
  [证书清单](../CONTEXT.md)里真正加载了这张证书的节点。

`loaded_nodes < expected_nodes` 意味着**下发到了但没生效**。这类故障在「节点自管证书」
的模型里根本看不见，是这套设计换来的主要能力，UI 上值得显式呈现（`N / M 个节点`，
不足时转 warning 并可展开列出 `missing_nodes`）。

`days_left` 三档由前端着色，阈值前端定；后端只给天数。

### `POST /certs/:domain/renew` / `POST /certs/renew-check`

单张续期 / 批量到期检查。**都是主控自己去 ACME 续，再随下一次下发把新证书内联带下去**，
不是让节点去续。异步：立即返回，结果经 WS `event` 帧回报。

```json
// data
{ "domain": "api.example.com", "accepted": true }
```

---

## 10. 审计

### `GET /audit`

cursor 分页（§0.5），可选 `?operator=abiu`。倒序。

```json
// data.items[]
{
  "id": 1837, "at": "2026-08-21T10:42:07+08:00",
  "operator": "abiu", "action": "下发配置",
  "target": "cfg-9b31e7", "src_ip": "10.8.0.2",
  "result": "partial", "detail": "5 成功 / 1 失败"
}
```

`result`：`ok` | `fail` | `partial`。`action` 的取值见 §5。
失败的**登录**尝试（`action: "登录"`, `result: "fail"`）在审计页单独提示。

---

## 11. 系统设置与告警

### `GET /settings` / `PUT /settings`

```json
{
  "master_endpoint": "ec.internal:9000",
  "heartbeat_interval_s": 3,
  "offline_threshold_count": 3,
  "auto_drop_dns": true,
  "dns_provider": { "kind": "cloudflare", "credential_mode": "api_token", "configured": true },
  "ops_bot_token_configured": true
}
```

- `master_endpoint` **必须是域名不是 IP**，后端校验，违反返回 `code: 1001`。
- 「节点最长 N 秒后被摘除」= `heartbeat_interval_s × offline_threshold_count`，
  由前端算出来实时显示（这是设置页的联动提示，不需要后端给）。
- **凭证只写入不回显**。`dns_provider` 里永远没有明文，只有 `configured: true/false`
  与 `credential_mode`（`api_token` | `global_key`，两者字段不同，前端表单据此切换）。
  `PUT` 时不带凭证字段 = 保持不变；带了就是替换。`ops_bot_token_configured` 同理。

### `GET /alerts` / `PUT /alerts`

```json
{
  "notify_level": "warn",
  "webhook": { "url_configured": true },
  "lark":    { "webhook_configured": true, "at_all_on_crit": true }
}
```

`notify_level`：`all`（全部）| `warn`（异常及以上）| `crit`（仅严重）。**渠道共用**这一个级别。

### `POST /alerts/test`

发一张 Lark 测试卡片。写审计（`action: "发送告警测试"`）。

```json
// 请求
{ "channel": "lark" }
// data
{ "sent": true, "detail": "卡片已投递" }
```

下游失败返回 `code: 3001`，`msg` 带上服务商的原文错误——这是排查 webhook 配错的唯一线索。

---

## 附：本文与设计文档的差异一览

给两边 agent 对账用。设计文档冻在 v1.0，以下以本文为准：

| 处 | 设计文档 | 本文 |
|---|---|---|
| 后端 §4 | 无 preview 端点 | 新增 `POST /deploys/preview`（§7.1） |
| 后端 §4 | 无 session 查询端点 | 新增 `GET /auth/session`（§1） |
| 后端 §3 | 证书不建表，从节点清单聚合 | 证书建表；清单降级为回执（§9） |
| PRD §4 | Caddy 全生命周期自动管理证书 | 主控集中签发 + 内联下发（ADR-0001/0010） |
| PRD §7 | 控制台走 mTLS | 内网 + 会话 + 审计，mTLS 默认关（ADR-0013） |
| 后端 §6 | 主控跑 `caddy validate` | Go 层校验（ADR-0004，§7.1/§7.2） |
| 后端 §6 | 失败一律重试 5 次 | 只重试传输层失败（ADR-0005，§2 `retrying`） |
| 后端 §8 | 前置 Caddy 反代 + 单套内部 CA | 不前置 Caddy；两套独立 CA（ADR-0013 / ADR-0009） |
| 前端 §6 | `hb_ms` | `hb_age_ms` + `last_hb_at`（§2/§4） |
| 前端 §5.1 | 右栏做权威 diff | 右栏是可读表示，权威 diff 在弹层（ADR-0007） |
