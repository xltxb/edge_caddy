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

### 0.2.1 未知字段一律拒绝

**请求体里出现契约没有的字段，返回 `1001` 并点名那个字段。**

静默忽略的话，一个写错的 key 会得到 `code: 0`——**请求成功了，
而什么也没存进去**。前端 agent 真撞上过：发了顶层 `dns_credential`
而不是 `dns_provider.credential`，返回成功、界面提示「设置已保存」，
而 `configured` 一直是 `false`。

**这是「没生效」那一族里最坏的一种：成功的假象。**
报错会让人再试，假象让人走开——他会去查别的地方，
因为「保存那一步明明成功了」。

### 0.3 `code` 取值

| code | 含义 | 典型场景 |
|---|---|---|
| `0` | 成功 | |
| `1001` | 参数格式错 | 域名不合法、`upstream` 不是 `host:port` |
| `1002` | **校验失败**（带结构化 `errors`） | Go 层渲染前校验不通过 |
| `1003` | 资源不存在 | `PUT /routes/nope.com` |
| `1004` | 资源冲突 | 新建路由重名 |
| `2001` | 状态冲突 | 对离线节点推配置；对已下线节点开解析或签 Token |
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

  **重试是异步的**：`POST /deploys` 在首轮结束时就返回（PRD 要求 6 节点
  ≤10s 完成反馈），掉队的节点在后台补推，最多 5 次、指数退避封顶 30 秒。
  期间这一行会反复在 `run` 与 `fail(retrying:true)` 之间变化，最后落到
  `ok` 或 `fail(retrying:false)`。次数用尽时 `detail` 会带上「已重试 N 次」。

  > 两种情况下重试会被**放弃**，这一行直接转终态并说明原因：
  > 一次新的下发开始了（旧配置不能盖到已经拿到新版本的节点上——那是把节点
  > 推回过去），或主控重启了（内存里的队列没了，留着 `retrying:true`
  > 会让 `phase` 永远停在 `running`）。

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
    "nodes_online":  3,   "nodes_warn": 2,   "nodes_down": 1,   "nodes_total": 6,
    "conns_total":   48200,
    "conns_delta_pct": 12.4,
    "conns_delta_reason": null,
    "origin_rate":   8.7,
    "drift_nodes":   1
  },
  "events": [ { "id": 4127, "at": "…", "node": "node-us-01", "kind": "crit", "msg": "…" } ]
}
```

- `dns_sync` 是**最近一次**把解析安排推给服务商的结果，`GET /nodes` 上也有一份。

  > 它与 `lines` 里的 `share` 是两件事：`share` 是**我们打算**怎么分，
  > `dns_sync` 说的是**服务商那边真的这样了没有**。
  >
  > 这个字段是**常驻**的，因为界面上「已退出解析」那类徽标也是常驻的——
  > 而 `POST /nodes/:id/dns` 响应里的 `dns_synced` 只出现一次就消失了。
  > 没有它的话，一次失败的同步会留下一个**一直撒谎到下次有人再点开关为止**
  > 的徽标。常驻的说法需要常驻的真相来源。
  >
  > **从来没同步过时 `at` 是 `null`**，不是某个零值时间（§0.4）。
  > 一个格式正确但意思是假的时间会渲染成 `00:00:00`，读起来像「凌晨同步过一次」
  > ——**这比一片空白危险**：空白会让人去查，一个像模像样的时间不会。

- `baseline` 是**当前基线**的版本号，顶栏常驻显示。放在顶层而不是 `kpi` 里——
  它不是一个指标，是全局上下文。

  **前端反推不出来，必须由后端给。** 从各节点上报的 `cfg_version` 取众数看着可行，
  但那在「一次下发只到了少数节点」时会指向旧版：6 个节点里 2 个是新版、4 个落后，
  众数给出旧版，于是 `drift_nodes` 会算成 2 而真相是 4——**方向正好反了**。
  基线的定义是「最近一次成功下发确立的那一版」（CONTEXT.md），只有主控知道。
  `drift_nodes` 也以它为准，两者同源才不会互相打架。
- **`nodes_online` 只算 `status == "ok"`，不含 `warn`。** 四个数字满足
  `online + warn + down == total`，由后端同一条语句产出。

  > `warn` 是「连着但不健康」（负载超过阈值）。把它算进「在线」，KPI 会在一台
  > CPU 81%、内存快满的机器上仍然显示绿色——而巡检时最该被看见的恰恰是那台。
  > 而且账要算得平：`在线 5 · 异常 2 · 离线 1` 里那 2 台既被算进在线、又被单独
  > 点名，读的人两种理解都对不上另一半。
  >
  > 前端**不要自己从节点列表推导这几个数**：两边分别推导迟早会算不平，
  > 而一处口径错会在界面上冒出来两次（侧栏角标与 KPI），那比单个错数字
  > 更让人怀疑整个系统。阈值在系统设置里（`warn_cpu_pct` / `warn_mem_pct`，
  > 默认 80 / 90）。

- `conns_delta_pct` 是**较昨日同时段**的连接数变化百分比，可为负，**可为 `null`**。
  数据来自 `traffic_samples`：每分钟一行全局聚合，保留 7 天。
  这是唯一一处需要落库的时序数据；节点级的 `cpu_series` 仍然只在内存里（§4）。

  **`null` 时 `conns_delta_reason` 说明是哪一种**，有数字时它是 `null`。
  前端一律按空态处理，不要显示 0%。

  | `conns_delta_reason` | 含义 | 对人的意思 |
  |---|---|---|
  | `insufficient_history` | 库里最早的样本还不到 24 小时前 | **再等等就有** |
  | `no_sample` | 有更早的样本，但昨天那一分钟没有 | 主控那时停着，或那一分钟被跳过；明天同一时刻可能有 |
  | `zero_baseline` | 昨天那一分钟的连接数是 0 | 不是故障，是昨天那会儿真没连接 |

  **一个不带原因的 `null` 逼着界面在三种情况里挑一句话说，而挑错的那两次
  会让人白等。** 「历史不足」说的是「再等等」，而真相可能是「主控昨天那会儿
  停着」——等到明天也不会变。后端当场就知道是哪一种，那就说出来。

  `no_sample` 时**不会往前找最近的样本来凑**——往前找会悄悄改变「同时段」的
  含义，而那个含义正是这个数字的全部意义。

  `zero_baseline` 时百分比在数学上没有定义：昨天 0、今天 100 确实是「从无到有」，
  但它不是一个百分比。硬算会得到 `+Inf` 或者一个靠平滑凑出来的假数字，
  **而两者都比「暂无同比」更难被质疑——一个具体的百分比自带可信度。**

  > **采样会主动跳过它不信任的那一分钟。**
  >
  > 主控刚启动的几分钟内不采样：那时 health 的内存是空的，节点要重连
  > （Agent 退避封顶 30 秒）再发一次心跳才会重新有数。这期间采到的点偏低，
  > 而 **24 小时后它会成为同比的分母，产生一个假的巨大涨幅**——那时人早忘了
  > 昨天重启过。一个 `+340%` 比一个 `null` 危险得多：null 会让人去查，
  > 一个具体的百分比不会。
  >
  > 同理，任何一分钟里只要有未下线的节点没报数，那一分钟就不记。
  > 宁可缺样本，不要假样本：缺的那一分钟只影响一个点，
  > 假的那一分钟会在 24 小时后再骗一次。
- `origin_rate` 是**回源率**百分比：**到达 upstream 的请求 ÷ 边缘收到的总请求**。
  越低越好，前端按「高于阈值转 warning」着色。**一个请求都还没有时是 `null`**——
  0 会被读成「一个请求都没回源」，那是个很好的数字，而真相是「还没有数据」。

  数据来自各节点 Caddy 的 `/metrics`：`caddy_http_requests_total{handler="reverse_proxy"}`
  是到达 upstream 的，其余 handler 的是被访问规则拦下或由静态响应处理掉的。

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
  "drained_at": null,
  "dns_reason": "manual", "dns_actor": "abiu", "dns_changed_at": "2026-08-22T14:30:00+08:00",
  "agent_version": "v0.2.0 (d3da612)",
  "routes": 7, "rules": 3,
  "created_at": "2026-08-01T09:00:00+08:00"
}
```

- `status`：`ok` | `warn` | `down`。
- **`drained_at` 与 `status` 是两个正交的事实，别合成一个徽标。**
  `status: down` 是**观察**——主控连续几个周期没收到心跳，判定它已经不在了。
  `drained_at` 是**意图**——人明确让它退出服务（[ADR-0014](adr/0014-drain-is-intent-not-status.md)）。

  一台节点可以「已下线且在线」（刚按下下线，隧道还没断干净），
  也可以「未下线但离线」（它自己挂了）。前者要显示成「我关的」，后者是故障。
  合成一个徽标会让运维在半夜分不清该不该起床。

  `drained_at` 非 `null` 时该节点：不参与解析、不进下发目标、不接受接入、
  **也不报离线告警**。

- **`agent_version` 是那台机器上跑的 Agent 版本**，接入时上报，每次接入覆写。
  空串表示还没接入过。

  灰度部署时人最先问的就是「我推上去的那一版到底上没上」。
  它是 `git describe + commit` 的形式（二十来个字符），
  **建议放在展开后的详情里而不是行上**——行上那几个数字是变化的，
  版本号是静止的，混在一起会让人扫不动。

- **`dns_reason` / `dns_actor` / `dns_changed_at` 说的是「最近一次解析开关是谁改的」。**

  | `dns_reason` | 谁改的 | 界面该引导人做什么 |
  |---|---|---|
  | `manual` | 人在控制台点的，`dns_actor` 是他的账号 | 他心里有数，想开就开回来 |
  | `auto_offline` | 心跳超时，**系统自动摘的**，`dns_actor` 是 `null` | **先去修那台机器** |
  | `drained` | 人把这台节点下线了 | 要用它先「重新上线」 |

  三者此前在数据里长得一模一样（只有 `dns_enabled` 一个布尔），
  于是界面只能说「未参与解析」四个字——**而这三种的处置完全不同**。

  从没人动过时三样都是 `null` / 空串：**那与「manual 而操作人不详」是两回事**。

  `dns_actor` 在系统自动摘除时是 `null`，**不是 `"system"`**——
  一个叫 system 的操作人会在界面上冒出一个不存在的账号，而人会去问那是谁。

  `dns_changed_at` 永远是真实时刻或 `null`，不会是零值时间（§0.4）。
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

`level`：`debug` | `info` | `warn` | `error`，**小写**。

> **§6.3 全局日志策略里也有一个 `level`，那个是大写**（`DEBUG` | `INFO` | …）。
> 同名不同规范，而两处相隔几百行。
>
> 这不是笔误：这一个是**一条日志实际的级别**，那一个是**要不要记这条日志的阈值**
> ——一个是事实，一个是配置。大小写的差异是从各自的来源带来的
> （Go 的 slog 给大写常量名，而节点日志走契约的小写取值）。
>
> 前端 agent 已经因为这个混淆动错过一次文件：他扫 `level: '(\w+)'` 报出一个
> 大写的 `INFO`，把它当成节点日志的取值改成了小写——而那是日志策略的。
> **一个正则扫出来的名字，不带它属于哪个概念。**

**是 Agent 自己的运行日志，不是 Caddy 的 access log。**

人在这一栏问的是「这台机器上发生了什么」——配置应用、证书加载、校验端点报错，
那些都是 Agent 写的。access log 属于另一个问题（流量分析），
而且一台扛流量的边缘节点的 access log 会把隧道和主控数据库压垮。

每个节点保留 500 条，查询上限 200（`?limit=`）。要追溯更久远的事，
看审计日志与事件流——那两样是主控自己写的，不会随节点消失。

> 这个端点曾经在契约里躺了很久而**从来没有注册过**，格式完整、看不出异样。
> 当时 `TestAllContractEndpointsAreRegistered` 没抓到它，因为那张端点清单是
> 手工维护的——**一份用来防止人忘记的清单，自己被忘了**。
>
> 现在契约全文里提到的每个端点都必须在某张清单里，而标了「（未实现）」的
> 会被另一条断言盯着：它必须**确实没有注册**（§0 的「不给桩」原则本身
> 现在也被测着）。

### `POST /nodes/token` —— 签发一次性接入 Token

```json
// 请求 —— Token 在签发时就绑定这台机器的身份
{ "node_id": "node-sg-01", "city": "新加坡", "vendor": "V.PS", "line": "CMIN2", "public_ip": "203.0.113.9" }
// data
{
  "token": "ec_1f9a…（仅此一次可见）",
  "expires_at": "2026-08-21T11:12:07+08:00",
  "ca_pin": "9e8f22a3430a2f859aee5b47…（隧道 CA 证书的 SHA-256）",
  "install_cmd": "sudo ./edge-node.sh install --master ec.internal:9000 --node-id node-sg-01 --token ec_1f9a… --ca-pin 9e8f22a3… --agent-bin ./edge-agent",
  "verify_cmd": "sudo ./edge-node.sh verify",
  "prerequisites": [
    "当前目录下有 edge-node.sh（本仓库 deploy/ 目录）",
    "当前目录下有 edge-agent 二进制（脚本不负责下载）"
  ]
}
```

Token **30 分钟 TTL、单次使用**，用后即失效。节点凭它完成首连并换取隧道客户端证书，
此后全部走 mTLS——见 [ADR-0009](adr/0009-internal-pki-two-cas.md)。
`token` 只在这一次响应里出现，任何后续接口都不回显。

**`--ca-pin` 不能改也不能省。** ADR-0009 没写死首连时 Agent 拿什么验主控；
纯 TOFU 会让中间人在那一刻冒充主控把一次性 Token 骗走。这串指纹是那个洞的
唯一堵法，**保护来自这条命令本身，不来自任何安装脚本**——界面上说明它时
别把功劳记在一个不存在的东西上，否则嫌它太长而删掉的人正好落进它要防的事里。

**`install_cmd` 给的是部署脚本，不是裸的 `edge-agent` 命令。**

> 裸命令跑得起来，但跑起来是前台进程、没有 systemd 单元、**没有 `Restart=always`**
> ——而受保护域名的 fail-closed 依赖 Agent 存活（ADR-0003）：它挂掉那一刻那些
> 域名整体 502，没有任何东西把它拉回来。发一条绕过它的命令，等于让人有机会
> 省掉部署脚本存在的理由。这与「`--ca-pin` 必填、不给默认值」是同一条判据。

**`--agent-bin` 留在命令里并给占位**，而不是省掉走默认值：脚本**不负责下载**
二进制，写在命令里比藏在文档里更难被跳过。

**`prerequisites` 点名了两个文件，不是一个。** `./edge-node.sh` 也是相对路径，
它和 `edge-agent` 是同一类东西：命令里指着它，而谁也不负责送它上去。
先前只说了二进制那一半——因为写文档的人手上就有脚本，于是「它怎么上去的」
这个问题从没出现过。**这是欠条的一个变体：不是「承诺了将来」，是「假定了当下」。**

**`verify_cmd` 与 `install_cmd` 一起给，界面必须一并呈现。** 照「复制命令」
按钮做的人不会自己想到还要跑一次 verify，而 verify 查的正是 Caddy Admin
有没有暴露在回环之外——私钥以 `load_pem` 内联在运行配置里（ADR-0010），
能读 Admin 就能读到它们。**一道没有人会执行的检查，等于不存在。**
到期时间以响应里的 `expires_at` 为准，不要在界面上写死「30 分钟」——
写死的数字变错了不会有任何报错，只会有人照着它算。

### `POST /nodes/:id/push` —— 把当前基线重推给单个节点

```json
// data
{ "cfg_version": "cfg-2f9a1c", "detail": "31ms" }
```

**重推推的是当前基线那一版，不产生新版本，也不写下发记录。** 把一台掉队的机器
带上来，不该在下发记录里多出一次谁也没发起过的下发。推完那台机器的
`cfg_version` 就等于基线，配置漂移随之消失。对**离线**节点返回 `code: 2001`。

> 这句话原先写的是「已下线节点」。那时术语表里还没有「下线」这个词，
> 两个意思共用了一个说法。现在「离线」是观察、「下线」是意图
> （[CONTEXT.md](../CONTEXT.md)），这里说的是前者——节点连不上，推不过去。
> 已下线的节点必然也离线，所以行为上没变，变的是这句话终于说的是它的意思。

这个端点是 [ADR-0005](adr/0005-retry-only-transport-failures.md) 的兜底：Caddy 拒绝的
配置不自动重试，环境类临时故障由人在这里手动恢复。

### `POST /nodes/:id/dns` —— 解析开关

```json
// 请求
{ "enabled": false }
// data
{ "id": "node-hk-01", "dns_enabled": false,
  "dns_synced": true, "detail": "解析安排已同步到服务商" }
```

关闭后该节点退出解析，其余节点的权重在各线路内**重新归一化**。写审计——
动作名跟着方向变（`暂停解析` / `恢复解析`），记成同一个动作会把一半信息丢掉。

**`dns_synced` 说的是「服务商那边真的变了没有」，界面必须呈现它。**
没配服务商时它是 `false`，`detail` 会说清「解析未变动」——不说的话，
人点完开关会以为流量已经不走那台机器了，而它照旧在解析里。

> 这里**没有** `weights_rebalanced`。契约早先写过那个字段，实现里从来没有过；
> 前端镜像契约的类型因此保留了一个恒为 `undefined` 的字段，那句「其余节点权重
> 已重新归一化」从此再没显示过——没有报错，纯粹的静默。
>
> 归一化确实发生了（它在主控算，见 §8），只是不由这个响应报告。

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
  { "step": "dns_removed",   "ok": true,  "detail": "解析安排已同步到服务商" },
  { "step": "conns_drained", "ok": true,  "detail": "已建立的连接都已结束；解析缓存未过期前仍可能有新连接进来" },
  { "step": "tunnel_closed", "ok": true,  "detail": "隧道已断开，此后拒绝该节点重连" }
] }
```

**每一步的 `ok` 说的是「这件事真的发生了」，不是「我这边的记录改成功了」。**

`dns_removed` 曾经拿「标志位写库成功」当判据，于是没配服务商时也报 `ok: true`
——解析记录一个字节没变，而运维看到「已停止解析」就去关机器。现在它反映的是
**服务商那边真的变了**；没配服务商时报 `ok: false`，detail 说「尚未配置 DNS
服务商，解析未变动」。

**`conns_drained` 的 detail 带着这句话的边界，界面上要原样显示。**
解析摘掉了，但 DNS 有 TTL，缓存在各级递归里，一段时间内仍会有新连接进来。
「已排空」指的是**回报那一刻**的连接数，不是「再也没有请求」——
不说的话人会据此认为可以关机了。

没排干净时 detail 带**还剩多少条**：

```json
{ "step": "conns_drained", "ok": false, "detail": "等了 30s 仍有 214 条连接未结束；关机会掐断它们" }
```

回一个布尔答不了人接下来要做的那个决定（现在能不能关机）：
是还剩 2 条可以直接关，还是还剩 8000 条得再等。

第一步没能摘掉解析时，这一步**跳过**而不是失败——解析还指着这台机器，
新连接源源不断，排空没有意义。detail 说清是跳过，否则人会去查节点。

**`tunnel_closed` 包含「此后拒绝该节点重连」，不只是断开这一次。**
Agent 断了就重连，只断开是个假动作：三秒后隧道又开了，节点照旧接下发、
照旧参与解析。所以下线在主控侧落一个持久标记（`drained_at`），
节点不在线时这一步**仍然报 `ok: true`**——要紧的是那个标记，而它落成了。

下线之后该节点会被拒绝接入（Agent 侧收到 `PermissionDenied`），
也拿不到新的接入 Token（`POST /nodes/token` 报 `2001`）——
否则「重装一台机器」就绕过了下线。

### `POST /nodes/:id/rejoin` —— 重新上线

```json
// 请求 —— 无 body
// data
{
  "id": "node-hk-01",
  "drained_at": null,
  "dns_enabled": false,
  "detail": "已允许重新接入；解析仍是关闭的，确认配置无误后再打开"
}
```

**解析不自动打开。** 一台机器能接入不等于它该马上分流量：它刚回来，
配置可能还是旧的。解析由人另外点（`POST /nodes/:id/dns`），
或者由下一次成功下发带起来。

界面上这一步要跟「下线」放在一起——一个只能按一次的下线按钮，
误点的代价不对称。

给一台**已下线**的节点开关解析**两个方向都拒**（`2001`）：

- 开：解析会指向一台主控明确拒绝它接入的机器。
- 关：它本来就不在解析里。而且关这一下会把 `dns_reason` 从 `drained`
  改写成 `manual`，于是**节点页说「已下线」而 DNS 页说「人手动关的」**
  ——两句单独看都对，只有并排才看得出对不上账。

> 关的方向此前是允许的，理由是「那个方向不会把流量送到一台连不上的机器上」。
> 那个理由至今成立，它只是**漏了上面第二条**。

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
| 导入证书 | `导入证书` |
| 删除访问规则 | `删除访问规则` |
| 修改全局策略 | `修改全局策略` |
| 调整解析权重 | `调整解析权重` |
| 节点解析开关 | `暂停解析` / `恢复解析` |
| 节点下线 | `下线节点` |
| 节点重新上线 | `重新上线` |

### 接入被拒会写事件（`kind: "warn"`）

人在一台新机器上跑完安装脚本，回到控制台等它上线。**如果接入被拒，
此前控制台上一片安静**——没有报错、没有提示、节点列表里不会多一行，
唯一的线索在那台机器的 `journalctl` 里，而他人在控制台前面。

| 事件 `msg` | 什么时候 | `node` |
|---|---|---|
| `接入被拒：Token 无效` | 这张 Token 认不出来 | `null` |
| `接入被拒：Token 已过期（签发后 30 分钟内有效）` | 签发超过 30 分钟 | 自称的 node_id |
| `接入被拒：Token 已被使用过` | 一张 Token 用第二次 | 自称的 node_id |
| `接入被拒：该节点已被下线，先在控制台「重新上线」再接入` | 被下线的机器在敲门 | 真实 node_id |
| `接入被拒：没有客户端证书也没有接入 Token` | 两样都没带 | `null` |

**`kind` 是 `warn`，判据是「这个状态会不会自己好起来」。** 不会自愈的最低 `warn`，
会自愈的才可以 `info`（心跳抖动、单次探活失败属于后者）。接入被拒不会自愈——
要么人去改 Token，要么人去重新上线，要么去关掉那台机器的 Agent。
`crit` 也不对：它不影响正在服务的流量。

**最后一行那种最安静**：一台被下线的机器，Agent 的 `Restart=always` 保证它
每隔几十秒敲一次门，而此前控制台完全看不到它在敲。那一条**认得出是哪台机器**
（凭 mTLS 证书的 CN），所以挂在那个节点上，人能点进去。

**前三行里 `node` 为 `null` 的那些是刻意的**：凭 Token 接入被拒时，
那台机器**还不是一个节点**——把事件挂到一个不存在的 node_id 上，
会让事件流里出现一行点不开的节点名。
| 签发接入 Token | `签发接入Token` |
| 证书续期 | `续期证书` |
| 修改系统设置 / 告警 | `修改系统设置` / `修改告警设置` |
| 发送告警测试 | `发送告警测试` |
| 登录 / 登出 | `登录` / `登出` |

---

## 6. 配置资源

三类资源共用一套草稿机制（§6.4），`res_key` 格式：
`route:<domain>` / `rule:<id>` / `global:<id>`。

### 6.0 两条编辑路径，控制台只走其中一条

改一个资源有两条路：

1. **草稿路**：`PUT /drafts/:key` → `POST /deploys/preview` → `POST /deploys`。
   **控制台一律走这条**（唯一例外见下）。
2. **直改路**：`PUT /routes/:domain`、`DELETE /routes/:domain`、`PUT /policies/:id`。

> 控制台的唯一例外是 `PUT /rules/:id`，且只为**共享密钥**：草稿是全局可见的
> （`GET /drafts` 列出所有人的草稿），凭证不能进去。

**直改路目前没有任何界面客户端。这不是遗漏，别当死代码删掉。**
它是 `EC_OPS_BOT_TOKEN` 那个免登录调用者的入口——批量脚本要的正是「直接改一条」。

而它**没有绕过下发流水线**：这两条路改的都是主控这边的**期望配置**，
节点在 `POST /deploys` 之前拿不到任何东西。审计照写、`deploys/preview` 照样出 diff、
按 `cfg_version` 回滚照样管用。直改路少掉的只有**草稿那一层**——
也就是「改了但还没打算发」这个中间状态，脚本本来就不需要它。

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
- **`version` 只在一次成功的下发里 +1**（那一次勾选到的资源），改草稿不动它。
  它回答的是「这条资源被下发过几次」，不是「它被编辑过几次」——
  所以草稿改了十遍再下发一次，version 只 +1。三类资源（路由 / 规则 / 策略）
  都是这个规则。

`POST /routes` 是新建向导，额外校验：域名格式、`upstream` 形如 `host:port`、重名
（重名返回 `code: 1004`）。创建后前端跳工作台并选中 `route:<domain>`。

**删除会连它的草稿一起清掉**（`route:<domain>` / `rule:<id>`，见 §6.4）。

留一份指向已删资源的草稿，会让「有几处未下发改动」这个数字算上一个
**再也下发不出去**的东西——而那个数字是人决定「现在要不要推」的依据。

> 这条行为一直是这样，但直到 2026-08-21 才写进契约。它是实现删除时
> 顺手做对的配套决定，而**正因为不是主线功能，写契约的时候没想到它**。
> 前端那边已经在依赖它，并且为它单独写了一条前提检查——
> 一个前端在依赖、而契约没写的行为，说明契约还不是完整的接缝。

`DELETE /routes/:domain` **联动**把该域名从所有 `access_rules.apply_to` 里摘掉，
响应带上受影响的规则供前端提示：

```json
{ "deleted": "api.example.com", "unbound_rules": ["office-wl", "svc-key-1"] }
```

### 6.2 访问规则

`GET /rules` → `data.items[]`；`PUT /rules/:id`；`DELETE /rules/:id` → `{ "deleted": "<id>" }`。

> **删除是「让它不在」，停用是「让它不生效」，两者不能互相替代。**
> 一条 id 打错的规则，停用和解绑域名都处理不了它——它会一直躺在列表里。
>
> 界面上要有删除入口。**既没有删除按钮、也没说不能删的话，人会找一圈然后
> 以为是自己没找到**——一个「没有」表现成了「我没找到」。
>
> 删掉一条**已下发**的规则，节点上那条直到下一次下发前仍在生效。
> 这个窗口是 fail-closed 方向（还在拦），比删路由那个窗口（还在服务）安全，
> 而后者我们早就允许了。
>
> 删不存在的规则报 `1003`，不假装成功。
>
> **删除会连它的草稿（`rule:<id>`）一起清掉**，与删路由同一条理由（见上）。
> 而**规则本身不会因为删路由而消失**——删路由摘的是绑定，不是规则：
> 「让它不生效」和「让它不在」是两件事。

```json
{ "id": "office-wl", "name": "办公网白名单", "type": "ip_whitelist",
  "enabled": true, "apply_to": ["api.example.com"], "version": 3,
  "spec": { "ips": ["203.0.113.7", "10.8.0.0/24"] } }
```

`type` 决定 `spec` 的形状，三选一：

```json
"ip_whitelist"   → { "ips": ["203.0.113.7", "10.8.0.0/24"] }
"service_secret" → { "header": "X-Service-Key", "algo": "hmac-sha256",
                     "ttl_s": 300, "replay_protection": true,
                     "secret_configured": true }
"jwt_bearer"     → { "iss": "https://idp.internal/", "aud": "edge",
                     "jwks_url": "https://idp.internal/.well-known/jwks.json", "skew_s": 60 }
```

> **共享密钥不在 `spec` 里。** `PUT /rules/:id` 的请求体上有一个**顶层** `secret`
> 字段，只写入不回显——`spec` 会被 `GET /rules` 原样返回，密钥放进去就等于回显了
> （PRD §7）。读接口只给 `spec.secret_configured` 布尔。
>
> **空串表示保持不变**，与 DNS 凭据、Lark webhook 一致：前端不回显它，
> 提交时也带不出原值来。界面按「已配置 / 更换」处理。
>
> ```json
> // PUT /rules/svc-key-1
> { "name": "…", "type": "service_secret", "enabled": true,
>   "apply_to": ["api.example.com"],
>   "spec": { "header": "X-Service-Key", "algo": "hmac-sha256", "ttl_s": 300 },
>   "secret": "只在设置时出现，留空即不改" }
> ```

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

> `ca` / `email` / `key_type` 是**主控**签发证书时用的参数（DNS-01），
> **不下发给节点**——节点跑官方 Caddy，不自己申请证书（ADR-0001）。设计稿里那句
> 「Caddy 全生命周期自动申请与续期」是旧说法，前端已改文案。
>
> 真正渲染进节点配置的是：`min_version` → `tls_connection_policies.protocol_min`；
> `http3` → server 的 `protocols`（开它还需要部署脚本放行 443/udp）；
> `hsts` → 响应头，**只在 TLS 那台 server 上发**（明文响应里发 HSTS 浏览器会忽略，
> 而它会让人以为已经生效了）。
>
> `ocsp`（Must-Staple）是**签发时**写进 CSR 的属性，不是服务端设置——它属于
> 主控的签发参数，节点侧无从体现。

**`global:log`**

| field | 类型 | 取值 | seed |
|---|---|---|---|
| `format` | enum | `json` \| `console` | `json` |
| `level` | enum | `DEBUG` \| `INFO` \| `WARN` \| `ERROR`（**大写**，见 §4 的说明） | `INFO` |
| `roll_size` | int | MB | `50` |
| `roll_keep` | int | 保留文件数 | `5` |
| `strip_headers` | bool | 移除 `Server` / `X-Powered-By` | `true` |
| `rate_limit` | bool | 按来源 IP 限流 —— **做不到，见下** | `false` |
| `rate_rps` | int | **条件字段**，见下 | 不适用 |
| `rate_burst` | int | **条件字段**，见下 | 不适用 |

> `rate_rps` / `rate_burst` 只在 `rate_limit = true` 时出现在表单里，因此
> `rate_limit = false` 时这两个键**可能根本不存在**。渲染器不要假定它们一定在，
> 也不要在关闭限流时给它们填默认值再渲染——那会让 diff 里凭空多出两行。
>
> ⚠️ **`rate_limit = true` 会让下发被拒绝（`code: 1002`）。**
>
> 这张表里这三行原先记的 seed 是 `true` / `200` / `400`——那抄自设计稿，
> 而实现的默认是 `false`。**文档记录的默认值是一个下发必被拒的值**，
> 比单纯的不一致更坏：照着文档配一遍，得到的是一个推不动的系统。已改。
>
> 用真二进制核实过：**官方 Caddy 2.11.4 的 132 个标准模块里一个限流模块都没有**
> （`caddy-ratelimit` 是插件）。一个开着却没有效果的限流开关，比一个明说
> 「做不到」的报错危险得多——与回源 mTLS 当初的处理一致。
>
> 这是同一个模式的第三次：设计稿假设了一个服务商/组件没有的能力。
> 前两次是「回源率靠缓存」（官方 Caddy 没有缓存模块）与「Cloudflare 分线路权重」
> （DNS 记录没有线路与权重概念）。要真做限流就得自建 Caddy 二进制，
> 而那会推翻 ADR-0001 与 ADR-0003 共同的前提。
>
> 界面应当把这个开关置灰并说明原因，而不是让人打开再被拒。

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
- **两者都可能是 `null`，而且必须按 `null` 处理，不会是空串**（§0.4）：
  - `after: null` = 校验没过，主控没有渲染出可下发的配置。
  - `before: null` = 当前基线自己渲染不出来（例如某条路由的 `mtls` 还开着，
    渲染器目前拒绝它）。

  > **不要把 `null` 喂给 diff。** 空串在这里是一个合法的配置内容（一份空配置），
  > 用它代替「没有」会让 diff 把整份配置渲染成删除——一个人填错了一个 IP，
  > 界面却告诉他「这次下发会删光所有配置」。那比不显示 diff 糟糕得多，
  > 而且出现在他最紧张的那一刻。
  >
  > `after` 为 `null` 时不要渲染 diff，写一行「校验未通过，主控没有渲染出
  > 可下发的配置」；`before` 为 `null` 时整份显示为新增，并说明基线渲染不出来。
- `validation.errors` 与 §0.3 的 `1002` 同构（`res_key` / `field` / `reason`），
  这样工作台能把错误落到具体输入框。`ok: false` 时前端禁用下发按钮。
  注意：**校验失败在这里返回 `code: 0`**——预览成功地告诉了你「校验没过」，
  这不是请求失败。只有 `POST /deploys` 才用 `1002` 拒绝。
- **`errors` 里有一类不是「字段配错了」，而是「这个资源不存在」**：
  `field: "res_key"`，出现在勾选了一份**底下没有 live 资源**的草稿时。
  草稿是在已有资源上的改动（Partial），没有底子合并不出东西。
  它排在渲染问题**前面**——「这条资源根本不存在」是「这条资源哪里配错了」的前提。

  > 原先这种草稿被**静默跳过**：预览 `ok: true` 什么也不提、下发返回成功、
  > 而随后 §7.2 那三件事里的第一件把草稿删掉了。
  > **人写了一条新规则，预览说没问题，下发说成功，然后什么都没有、草稿也没了。**
  >
  > 现在它挡在 §7.2 之前，所以草稿活得下来——这是拒绝它的**主要理由**，
  > 不是副作用。要新建资源，先把资源本身建出来（`POST /routes`、
  > `PUT /rules/:id`），再改它的草稿。
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
成功后进度全部走 WS `deploy_progress`（§2）。

**一次成功的下发对被勾选的资源做了三件事**，前端刷新时都会看到：

1. **草稿被删掉**，它的内容合入了 live —— 「有几处未下发改动」相应减少。
   未勾选的草稿仍然是草稿。
2. **`version` +1**（§6）。
3. **基线变成新的 `cfg_version`**，各节点的 `cfg_version` 跟着更新，
   `drift` 相应清零（§4）。

第 1 条曾经是个真 bug：草稿被清掉了、节点也跑上了新配置，而**源码里的
live 行没更新**——于是下一次下发又会把旧值推回去。当时的测试没抓到它，
因为它们只断言了「节点服务新值」和「草稿没了」，两条在 bug 存在时都成立。

### 7.3 `GET /deploys` —— 下发记录

cursor 分页（§0.5）。

```json
// data.items[]
{
  "id": 82, "cfg_version": "cfg-9b31e7",
  "operator": "abiu",
  "res_keys": ["route:api.example.com"],
  "ok_count": 5, "fail_count": 1,
  "targets": ["node-hk-01", "node-us-01", "node-sg-01"],
  "is_baseline": true,
  "created_at": "2026-08-21T10:42:07+08:00"
}
```

`is_baseline` 为 `true` 的那一条是当前基线，**不可回滚**（前端应禁用该行的回滚按钮）。

**`targets` 在列表里有用，别当成详情字段漏出来的。** 它给出这次下发的**目标总数**，
而 `ok_count + fail_count` 是**已回报数**——两者不等就说明这次下发还在进行中。
没有它就判断不出「结束了没有」。

**列表没有 `target_count`，详情有。** 那是个不一致，来源是列表直接序列化了
仓储结构体而详情是手工拼的。列表里要总数用 `targets.length`——
两个投影只该有一处，而 `target_count` 留在详情里的理由是契约 §7.4 列了它
（见那一节）。

> `targets` 此前不在这张表里，是前端 agent 拿他的 mock 与真主控比形状时发现的
> ——他的 `check:shapes` 以真主控为基准，那一处报的是「真主控有而我没有」。
>
> **这类缺口两边的测试都发现不了**：我这边验的是「后端按契约行事」，
> 他那边验的是「前端与他想象的后端接得上」，而**契约本身对不对，
> 只有把两边真产物摆在一起才看得见**。

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

> 注意这一段路径参数是**版本号**（`cfg-8b03e7`），而 §7.4 的
> `GET /deploys/:id` 是**数字编号**。两者形状不同不是笔误——后端文档 §4
> 就是这么定的，前者按内容寻址、后者按记录寻址。

```json
// data
{
  "res_keys": ["route:api.example.com", "rule:office-wl"],
  "skipped": [
    { "res_key": "route:new.example.com",
      "reason": "这条路由是那次下发之后才新建的，回滚不会删除它" }
  ]
}
```

- `res_keys` 是写回了草稿的资源，前端据此跳工作台。
- **`skipped` 是回滚覆盖不到的资源，必须显示出来。** 草稿是叠加在 live 行上的
  Partial，那一行不存在就无处可叠；而回滚承诺「只写草稿、不动线上」，为了恢复
  一条已删除的路由去直接写 live 就破坏了这个承诺。两种情形：那次下发之后被删的
  （回滚不会建回来），和那次下发之后才新建的（回滚不会删掉）。

  > 静默跳过不可接受：人点了「回滚到 cfg-8b03e7」、界面说成功了，而某条路由
  > 其实没回去。

- 只写回**有差异的字段**，不是整个资源——整个写回会让工作台上每个字段都亮成
  改动过，diff 就失去了指出「哪儿变了」的作用。`version` 由系统维护，不写入。
- **快照不含共享密钥**，因此回滚不恢复旧密钥。密钥只写入不回显（PRD §7），
  它本来就不是用户在 diff 里看到的东西。
- 回到**当前基线**返回 `code: 2001`（回到自己是空操作，返回成功会让人以为
  发生了什么，而工作台上一处改动都不会出现）。前端应把当前基线那行的回滚按钮禁用。

---

## 8. DNS 调度

### `GET /dns/weights` / `PUT /dns/weights`

按线路分组。线路码固定五个：`ct`（电信）`cu`（联通）`cm`（移动）`tw`（台湾）`ov`（境外）。

```json
// data
{
  "domain": "cdn.example.com",
  "dns_sync": { "ok": false, "at": "2026-08-21T10:42:07+08:00",
                "detail": "尚未配置 DNS 服务商" },
  // 从来没同步过时：{ "ok": false, "at": null, "detail": "尚未向 DNS 服务商同步过" }
  "lines": [
    { "code": "ct", "name": "电信", "entries": [
        { "node": "node-hk-01", "weight": 60, "share": 60.0, "dns_enabled": true,  "status": "ok" },
        { "node": "node-us-01", "weight": 40, "share": 0.0,  "dns_enabled": false, "status": "down" }
    ] }
  ],
  "capabilities": {
    "kind": "cloudflare",
    "lines": [
      { "code": "cn", "name": "中国（电信 / 联通 / 移动合并）", "covers": ["ct","cu","cm"] },
      { "code": "tw", "name": "台湾", "covers": ["tw"] },
      { "code": "ov", "name": "境外", "covers": ["ov"] }
    ],
    "weights": true,
    "notes": "Cloudflare 的 DNS 记录没有权重与线路概念……电信 / 联通 / 移动无法区分"
  }
}
```

- `weight` 是**配置值**，`share` 是**实际占比**。两者不同：`dns_enabled: false` 的节点
  （手动暂停或心跳超时自动摘除）`share` 为 `0`，其权重在该线路内的其余节点间**重新归一化**。
  前端的占比条画 `share`，输入框绑 `weight`。
- `PUT` 后**立即**调 DNS 服务商，失败返回 `code: 3001` 且**不落库**。写审计。

  > 顺序是**先推后存**。反过来的话，库里会留下一份服务商上并不存在的安排，
  > 而界面照常显示它——那是最糟的一种不一致，因为看起来一切正常。
  >
  > 尚未配置服务商时**仍然保存**（权重是本地的意图），但 `capabilities.notes`
  > 会说明「不会推到任何地方」。

- **五条线路始终齐全**，即使某条一个节点都没配。前端按线路分组渲染，
  缺一条会让那一组凭空消失，而不是显示成「这条线还没配」。
- **每条线路的 `entries` 列的是「候选」，不是「已配置的」。**
  候选 = 配过权重的 ∪ 还没下线的节点。所以一台刚接入、一行权重都没配过的节点，
  会在五条线路上各出现一次，`weight: 0`、`share: 0`——**那正是给它配权重的入口**。

  > 原先只列「配过权重的」，于是形成一个闭环：节点只有在 `dns_weights` 里有行时
  > 才出现在这一页上，而写那张表的唯一入口就是这个 `PUT`——界面上只能改
  > 「已经在列表里的节点」。**新接入的节点永远进不了解析**，而页面看起来完全正常。

  > **不要试图从 `node.line` 推出线路码。** `node.line`（`CN2 GIA`、`CMIN2`）是
  > **机房卖的中转线路**，说的是这台机器怎么出网；`line_code`（`ct/cu/cm/tw/ov`）是
  > **访问者来自哪个运营商**。一台 CN2 GIA 的机器同时服务电信、联通、移动的访问者
  > 是常态，两者之间没有函数关系。「哪台机器接哪条线的流量」是一个**调度决定**，
  > 只能由人来下——产品要做的是让这个决定做得出来。

- **已下线的节点（`drained_at` 非空）不进候选**，给一台已经退出的机器配权重
  是没有意义的动作。但它**如果配过权重就仍然出现**——那份配置是人写下的意图，
  不该因为一次下线就从页面上消失（重新上线之后还要用）。
- 整条线路的节点全部离线时，全部 `share` 为 `0`（不做除法）。一次机房故障
  就会到这个状态，除零或 NaN 会让这个页面在最需要看的时候崩掉。
- **`warn` 的节点仍然参与解析。** 它是「连着但不健康」，自动摘掉会把负载全压到
  其余节点上，很可能连锁。要摘由人决定。

### `capabilities` —— 这家服务商实际能做到什么

**两家服务商的能力并不对等，界面必须如实呈现。**

| | 分线路（电信/联通/移动…） | 权重 |
|---|---|---|
| **DNSPod** | 原生支持，是它的核心功能 | 支持（付费套餐） |
| **Cloudflare** | **DNS 记录没有这个概念** | **DNS 记录没有 weight 字段** |

Cloudflare 的加权调度经 **Load Balancing**（独立付费产品）实现，而它的地理维度是
**国家/大洲，不是中国的 ISP 线路**。因此 `ct` / `cu` / `cm` 会被塌缩成「中国」，
`capabilities.lines` 里只有 `["cn","tw","ov"]`。

**三条线权重不同时，`PUT` 会以 `code: 1001` 拒绝并说明原因**，而不是取个平均值——
给出一个用户没要过的配置比拒绝糟糕得多，尤其它还不会被发现。

**每个分组自带 `covers`——它盖住了 §8 的哪几条线路。** 这张映射是服务商的知识
（「Cloudflare 的中国这一组盖住电信/联通/移动」只有适配器知道），因此由后端给出：
放在前端意味着同一份知识存在两处，加第三家服务商时会静默渲染错。

前端应据 `covers` 把被同一组盖住的线路**合并成一个输入框**，而不是置灰——
置灰只是把拒绝提前，合并让「三条线权重不同」这个会被拒绝的状态在界面上
根本无法被表达。`covers` 长度为 1 的分组正常渲染。`notes` 直接呈给用户。

五条线路必然被完整覆盖：漏掉的那条在界面上会凭空消失，而它在契约里是存在的。

---

## 9. 证书

证书由**主控**集中签发（certmagic 跑 **DNS-01**）并经隧道内联下发，边缘节点不持有
DNS 凭据、不自行申请——见 [ADR-0001](adr/0001-master-issues-certificates.md)、
[ADR-0010](adr/0010-cert-distribution.md)。

> 设计稿 seed 里 `challenge` 写着 `HTTP-01` 的地方都是错的：域名按权重只解析到部分节点，
> 轮换外的节点无法完成 HTTP-01 校验，而节点恰恰需要在**进入轮换之前**就持有证书。

### `PUT /certs/:domain` —— 从外部证书平台导入

主控获取证书的**第二条路**，与 ACME 并行。节点那一侧完全不变：
仍然是主控集中持有、经隧道内联下发（ADR-0001、ADR-0010），
节点照旧不持有 DNS 凭据、不自行申请。

```json
// 请求
{
  "cert_pem": "-----BEGIN CERTIFICATE-----\n…（叶子 + 中间证书）",
  "key_pem":  "-----BEGIN PRIVATE KEY-----\n…"
}
// data —— **不回显私钥**
{
  "domain": "api.example.com",
  "issuer": "外部证书平台 CA",
  "not_after": "2026-11-21T08:00:00+08:00",
  "days_left": 90,
  "domains": ["api.example.com", "*.api.example.com"],
  "auto_renew": false,
  "warnings": [],
  "detail": "已导入并下发到各节点"
}
```

**成功即已下发。** 存进库而不推到节点，界面会显示「已导入」而节点上还是旧的
——所以这个端点在下发失败时返回 `3001`，`msg` 会说清「证书已存下，但下发失败」。

**`auto_renew` 恒为 `false`，这不是可选项。** 主控续不了一张不是它签的证书
（ACME 需要那个域名的 DNS 控制权与账户绑定）。而留成 `true` 的后果更重：
到期前 30 天续期扫描会挑中它，**主控用 ACME 重签一张覆盖掉导入的那张**
——而那不会有任何提示，人只会在某天发现签发者变了。

`challenge` 回 `"imported"`，它回答的是「这张证书是怎么来的」。

#### 四条硬校验，全部在存之前

它们的共同点是**失败都不会当场显形**：证书存进库、下发到节点、
界面显示「已导入」，而站点是坏的。

| 拒绝的情形 | 不拦住会怎样 |
|---|---|
| 私钥配不上证书 | 两边单独看都是合法 PEM，而 TLS 握手失败 |
| 证书不覆盖那个域名 | **证书本身完全有效**，浏览器报名称不符，而人会去查 DNS、查 Caddy |
| 已经过期 | 界面照样显示「已导入」 |
| 还没生效（`NotBefore` 在未来） | 多半是机器时钟不对，而那件事值得当场知道 |

全部报 `1002`（带 `errors`），**不是 `1001`**：`1001` 是「格式错」，
人拿到它会去检查 JSON 有没有写错，而问题在 PEM 里面。
域名不符那条的 `reason` 里会带上**它实际覆盖的域名**——人多半是传错了文件。

通配符按 RFC 6125 匹配：`*.example.com` 覆盖 `api.example.com`，
**不覆盖** `a.b.example.com`。

#### `warnings` 是提示，不是拒绝

`data.warnings` 里的东西**不该拒绝，但人应当知道**。拒绝一张能用的证书，
代价是人在别处凑合（比如把证书直接 scp 到节点上，绕开整套下发）。

- **只有叶子、没有中间证书** —— 部分客户端（老 Android、某些 Java 运行时）
  会握手失败，而另一些完全正常。**「有的人打得开有的人打不开」是最难查的一类。**
- **快到期了** —— 导入的证书主控不会自动续期，到期前要再导一次。

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
  "warn_cpu_pct": 80,
  "warn_mem_pct": 90,
  "dns_provider": {
    "kind": "cloudflare",          // dnspod | cloudflare | ""（未配置）
    "domain": "example.com",       // 根域名
    "sub": "cdn",                  // 子域前缀，@ 表示根
    "credential_mode": "api_token",// 仅 cloudflare：api_token | global_key
    "configured": true             // 凭证在不在，永远没有明文
  },
  "master_endpoint_readonly": true,
  "ops_bot_token_configured": true
}
```

**`PUT` 时 `dns_provider` 的字段**（两家不同，界面按 `kind` 切换）：

| 字段 | dnspod | cloudflare |
|---|---|---|
| `kind` | ✓ | ✓ |
| `domain` / `sub` | ✓ | ✓ |
| `credential` | `ID,Token` | API Token 或 Global Key |
| `credential_mode` | — | `api_token` \| `global_key` |
| `zone_id` | — | ✓ |
| `email` | — | 仅 `global_key` |
| `account_id` | — | 可选 |
| `clear` | ✓ | ✓ |（独立动作，见下）

**每个字段独立判断：不给 = 不动，给了 = 设成那个值。**
所以 `{"dns_provider":{}}` **什么也不改**（不是清空——这一行此前写反了，
是前端 agent 在真主控上试出来的）。

**`credential` 空串 = 不改动**（凭证不回显，前端带不出原值）。
于是逐字段清空会留下一个**能到达的矛盾状态**：库里有凭证、而没有服务商。
那不只是显示别扭——**一份再也用不到、也删不掉的凭证仍然是一把有效的 API Token**。

清掉整份配置（含凭证）用一个**独立的动作**：

```json
{ "dns_provider": { "clear": true } }
```

`clear` **不能与其他字段同时给**，同时给返回 `1002`：
`{"clear":true,"kind":"dnspod"}` 有两种合理读法（先清再设 / 清掉一切），
而挑一种执行等于替人做了他没做的决定。

> 这张表此前不在契约里——示例只有 `kind` / `credential_mode` / `configured`，
> 而主控实际还回 `domain` / `sub`、`PUT` 还接受 `zone_id` / `email` / `account_id`。
> 前端 agent 是去读 `internal/api/settings.go` 才拿到准确的 key 的。
> **一份要靠读实现才能用的契约，在那一段上不成立。**

- `warn_cpu_pct` / `warn_mem_pct` 决定一台节点什么时候进 `warn`（默认 80 / 90）。
  没有阈值的话 `warn` 永远不会被写入，「异常 N 个」那个桶就恒为 0——
  **一个永远是零的计数比没有这个计数更糟**，它会让人以为「系统看过了，没问题」。
- **`master_endpoint` 是只读的**（`master_endpoint_readonly: true`），
  `PUT` 带它一律返回 `1002`。

  它的值来自启动配置 `EC_ADVERTISE`，**运行时改不了**：这个地址进了主控
  服务端证书的 SAN，而证书是启动时签的——改设置改不了证书，
  那时节点会连上一个证书里没有它的地址，握手直接失败。

  「必须是域名不是 IP」那条规则没有消失，它在**启动时**校验（填 IP 主控拒绝启动）。

  > 此前 `PUT` 会把新值存进库，而**没有任何东西读那一列**（拼安装命令用的是
  > `EC_ADVERTISE`）。人改完看到「已保存」，节点的连接地址一个字没变。
  > 而 `GET` 回的是库里那一列——灰度上它一直是空的，于是设置页显示空白，
  > 而主控明明知道自己公布的是什么。
- 「节点最长 N 秒后被摘除」= `heartbeat_interval_s × offline_threshold_count`，
  由前端算出来实时显示（这是设置页的联动提示，不需要后端给）。
- **凭证只写入不回显**。`dns_provider` 里永远没有明文，只有 `configured: true/false`
  与 `credential_mode`（`api_token` | `global_key`，两者字段不同，前端表单据此切换）。
  `PUT` 时不带凭证字段 = 保持不变；带了就是替换。
- **`ops_bot_token` 不是这个端点能改的东西。** 它只从环境变量 `EC_OPS_BOT_TOKEN` 读，
  在主控启动时装进鉴权中间件；`GET /settings` 的 `ops_bot_token_configured` 是**只读回显**，
  `PUT` 里发 `ops_bot_token` 会被严格绑定当场拒掉。

  > 这一条原先写成「`ops_bot_token_configured` 同理」，跟凭证那一句并列——
  > 前端照着它做了个输入框，而后端从来没有这个字段。它是免登录调用主控的凭证，
  > 让一个已登录会话去铸一把长期钥匙，跟改 CA、改监听地址是同一类事，
  > 属于部署面，不属于控制台。

### `GET /alerts`

```json
{
  "notify_level": "warn",
  "webhook": { "url_configured": true },
  "lark":    { "webhook_configured": true, "at_all_on_crit": true }
}
```

`notify_level`：`all`（全部）| `warn`（异常及以上）| `crit`（仅严重）。**渠道共用**这一个级别。

### `PUT /alerts`

**请求体是平的，和 `GET` 的形状不一样**，四个字段都可省，省掉 = 保持不变：

```json
{
  "notify_level": "crit",
  "webhook_url": "https://...",      // 空串 = 保持不变
  "lark_webhook": "https://...",     // 空串 = 保持不变
  "at_all_on_crit": true
}
```

`data` 是 `null`——这个端点不回显保存后的状态，要新状态就重新 `GET`。

> **为什么不做成和 `GET` 一样的形状。** `GET` 里只有 `url_configured: true/false`，
> 没有地方放 webhook 地址（凭证只写入不回显，PRD §7），所以 `PUT` 的字段集**必然**
> 和 `GET` 不同。那么两种写法：一种是形状明显不同（平的），一种是形状看着一样、
> 里面的字段名不一样（`webhook.url` vs `webhook.url_configured`）。
>
> 这一段原先把两个端点并成一个代码块，读起来就是「PUT 发 GET 那个形状」。
> 前端照着做了，于是 `at_all_on_crit` 被包在 `lark` 里发过来，后端的结构体里
> 它在顶层——`encoding/json` 静默丢掉，返回 `code: 0`，界面显示「已保存」，
> 而「严重时 @所有人」这个开关**从来没存进去过**。
>
> **看着一样而实际不一样的形状，比明显不一样的形状更容易骗到人。** 现在是平的，
> 并且这个端点改用严格绑定：发错形状会当场报出哪个字段不认识，而不是默默吞掉。

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
