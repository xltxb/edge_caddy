# 部署

两个可执行体，两种装法。

## 边缘节点

### 先把两个文件送上去

命令里的 `./edge-node.sh` 和 `--agent-bin ./edge-agent` **都是相对路径**，
它们都假定文件已经在当前目录。**谁也不负责把它们送上去。**

```bash
# 从开发机推过去，或者用你惯用的任何方式
scp deploy/edge-node.sh edge-agent root@node-hk-01:/root/
```

> 这一段先前只写了二进制那一半。脚本自己那一半没人写——**因为写文档的人
> 手上就有它**，于是「它怎么上去的」这个问题从来没出现过。
>
> 这是欠条的一个变体：不是「承诺了将来」，是**「假定了当下」**。
> 假定的东西对写的人成立，所以他不会想到要说。

### 装

**控制台「添加节点」直接给出这条命令**（`install_cmd` 字段），复制粘贴即可：

```bash
sudo ./edge-node.sh install \
  --master ec.internal:9000 \
  --node-id node-hk-01 \
  --token ec_1f9a… \
  --ca-pin 9e8f22a3430a2f859aee5b47… \
  --agent-bin ./edge-agent
sudo ./edge-node.sh verify   # ← 控制台的 verify_cmd 字段，别跳过
```

`install_cmd` 是**一条要执行的命令**，不是「两个值的来源」——Token 与 CA 指纹
在响应里另有 `token` 与 `ca_pin` 两个字段，要单独取值就取那两个。

**`verify` 不是可选的收尾。** 它查的正是 Caddy Admin 有没有暴露在回环之外，
而证书私钥以 `load_pem` 内联在运行配置里（[ADR-0010](../docs/adr/0010-cert-distribution.md)）
——能读 Admin 就能读到它们。脚本为「没在监听」和「监听错地方」专门分了两个
返回值（前者是 Caddy 没起来，后者是私钥暴露），**而没人跑它的话，那个区分
一次也用不上**。控制台因此把它作为 `verify_cmd` 一并给出。

> 控制台发的是**这条脚本命令**，不是裸的 `edge-agent …`。
> 裸命令跑得起来，但跑起来是前台进程、没有 systemd 单元、**没有 `Restart=always`**
> ——而受保护域名的 fail-closed 依赖 Agent 存活（ADR-0003）。
> 发一条绕过它的命令，等于让人有机会省掉这个脚本存在的理由。

### `--ca-pin` 不能省也不能改

接入首连时这台机器上还没有隧道 CA，**指纹是确认对面就是你的主控的唯一依据**。
少了它，接入会退化成 TOFU：中间人在那一刻冒充主控，就能把一次性 Token 骗走
（[ADR-0009](../docs/adr/0009-internal-pki-two-cas.md)）。

保护来自这条命令本身，不来自这个脚本——**手动接入时同样要带上它**。

### 这个脚本不下载 edge-agent

`--agent-bin` 指向一个已经在本机的文件。分发二进制需要一个可信来源，
而这套系统还没有那个东西——假装有会比没有更糟。

### 装完之后节点上有什么

| 东西 | 位置 | 为什么是这样 |
|---|---|---|
| Agent 二进制 | `/usr/local/bin/edge-agent` | |
| 凭据 | `/etc/edge-agent.env`（0600） | **绝不进 ExecStart**：命令行参数出现在 `ps` 输出里，本机任何用户都看得到 |
| 隧道身份 | `/var/lib/edge-agent/tunnel.*`（0700） | 接入时主控签发，此后连接全走 mTLS |
| 回源证书 | `/var/lib/edge-agent/edge-mtls.*` | 主控每次下发时续，叶子 24 小时（ADR-0009） |
| Caddy Admin | 127.0.0.1:2019（drop-in 钉死） | 证书私钥以 `load_pem` 内联在运行配置里（ADR-0010），能读 Admin 就能读到它们 |
| 校验端点 | 127.0.0.1:2020 | JWT / 服务密钥在这里验签（ADR-0003） |

### 防火墙

脚本**不替你改防火墙**，只打印该放行什么：

- `80/tcp`、`443/tcp`
- `443/udp` —— **只在开了 HTTP/3 时**。无条件开一个 UDP 端口是白送攻击面；
  而漏了它的症状很隐蔽：HTTP/3 握不上会静默回落到 TCP，用户只觉得「有点慢」。
- **2019 与 2020 一个都不放行。**

### 两条承重的约束

**`Restart=always` 不是锦上添花。** 受保护域名的 fail-closed 依赖 Agent 存活
（[ADR-0003](../docs/adr/0003-edge-auth-via-agent-forward-auth.md)）——它挂掉那一刻，
那些域名整体 502。这是正确的安全姿态，代价就是 Agent 成了硬依赖。

**单元里没有 `Requires=caddy.service`。** Caddy 挂了 Agent 应当继续活着并把这件事
报上去。`Requires` 会让它跟着一起停，于是主控看到的是「节点离线」——
而真相是「节点活着但 Caddy 挂了」。这两种故障的处置完全不同。

## 主控

主控没有安装脚本。它需要的东西少，而每一项都要人做决定：

```bash
createdb edge_controller
export EC_DATABASE_URL="postgres://localhost:5432/edge_controller?sslmode=disable"
export EC_SECRET_KEY="…至少 32 字节…"      # DNS/Lark 凭据与两套 CA 的根私钥都用它加密
export EC_ADVERTISE="ec.internal:9000"      # ← 必须是域名，填 IP 主控不启动，见下
export EC_HTTP_ADDR="127.0.0.1:8080"        # ← 见下

./master -migrate
./master --create-user 'abiu:…'
./master -ca-pin                            # 打印 CA 指纹，添加节点时要用
./master
```

### `EC_ADVERTISE` 必须是域名，没有默认值

**这是一个会让现有部署起不来的改动。** 原先它默认 `127.0.0.1:9000` 且不校验；
现在没有默认值，填 IP 直接拒绝启动（#24）。升级前先把它设成域名。

为什么值得付这个代价：代价出现在**主控换地址那一天**。

| | 用 IP | 用域名 |
|---|---|---|
| 改主控 | 重签证书 + 重启 | 重签证书 + 重启 |
| 改**每台节点** | `EnvironmentFile` 里的 `--master` 挨台改 | 不用动 |
| 改完之前 | 节点全部连不上 | 无感 |

贵的是「挨台改」，不是重签——内部 PKI 重签一次 `SignServer` 就行。
而服务端证书 TTL 是**十年**，不会自动轮换：这笔账要么不付，要么那天一次性全付。

> **这跟「证书能不能验过」无关。** 首次接入根本不看 SAN——Agent 走
> `InsecureSkipVerify` + `--ca-pin` 指纹校验。这条限制纯粹是运维可达性。

**没有默认值是刻意的。** 任何默认值在生产上都是错的（没人的主控真叫那个名字），
而一个能启动的错误默认值比起不来更危险：它会让人以为配好了，
直到第一台节点连不上。与 `EC_SECRET_KEY` 同一条。

本地开发用 `localhost:9000`——它是主机名不是 IP，能过校验。

### `EC_HTTP_ADDR` 绑错地方不会有任何东西报警

控制台的准入是「只绑内网 + 会话 Cookie + 全写审计」
（[ADR-0013](../docs/adr/0013-console-access-is-network-plus-session.md)）。
「只绑内网」是**部署形态**，不是代码里的检查——绑成 `0.0.0.0` 代码不会拒绝，
也不会有任何日志提示。

远程访问走 SSH 隧道或 WireGuard。mTLS 是留给「控制台要挪出内网」那天的开关
（`EC_MTLS=1`），**翻开它之前先确认回环上仍有一个入口**，否则弄丢客户端证书
会把唯一的运维人员锁在系统外面。

### 主控不装 Caddy

[ADR-0004](../docs/adr/0004-no-master-side-caddy-validate.md)：下发前的校验是渲染器
自己的 Go 层检查，不跑 `caddy validate`。装一个只用来反代的 Caddy 也会把这个性质
吃掉一半——机器上仍然多了一个要跟版本、要打 CVE 的 Caddy，排查时「主控上的 Caddy」
和「节点上的 Caddy」会被混为一谈。

## 测试

```bash
bash deploy/edge-node_test.sh
```

它跑在开发机上（macOS 也行），验的是**决策逻辑**：发行版探测、监听地址判定、
单元与防火墙规则的生成。这些是脚本里真正会写错、又不需要 Linux 的部分。

**「装上去能起来」验不了**，那需要一台真机。这里不假装验过——一份「全部通过」
而其中几条其实没验的报告，比没有这份报告更糟：它会让人停止怀疑。

同样地，`edge-node.sh verify` 也**只报它真的查过的**。隧道通不通、证书有没有被
Caddy 真的加载，这两件事它查不到，会明说，并指向控制台上看得到的地方
（节点在线状态、证书页的「N / M 个节点」）。

## 一个已知的偶发失败

全量并行跑 `go test ./...` 时见过一次：

```
Caddy 拒绝了带访问规则的配置: 连接 Caddy Admin: Post ".../config/apps/http": EOF
```

admin socket 在响应之前被关掉了。**根因未知。**

到目前为止见过**三次**。前两次都没抓到现场：第一次的证据只有那行错误，
第二次连是哪条测试都没记下来（我当时的统计脚本只数了失败个数）。
**统计方式本身也会丢证据**——这一点比那个 flake 本身更值得记。

### 2026-08-21：第三次，第一次有现场

`scripts/gotest.py`（那个「永远打印失败细节」的脚本）**第一次全量跑就抓到了它**：

```
FAIL  internal/agent  TestSchemaErrorIsRejectedConsistently
    caddy_test.go:211: 基线配置应当被接受: 连接 Caddy Admin: Post ".../config/apps/http": EOF
    caddytest.go:152: caddy 进程直到测试结束仍然活着 —— 失败不是因为它死了
```

**根因仍然未知**，但排除了两条，而且发现一处诊断代码从未生效：

**假说一（已证伪）：keep-alive 连接被复用而对端已关闭。** 这本来能完美解释
「只有 POST 失败」——Go 的 Transport 对幂等请求会自动重试，POST 不会。
用 `httptrace` 实测：Caddy Admin 上**每一次请求都是新连接**（`reused=false`），
根本没有复用。

**假说二（已证伪）：失败时 caddy 日志为空，说明它还没走完启动。**
实测发现日志**在成功时也是空的** —— 启动配置里写着 `"level":"ERROR"`，
而启动与重载信息都是 INFO 级。

**而这意味着那条「失败时打印 caddy 日志」的诊断从来没有输出过任何东西**
——那是上一轮**专门为这个 flake 加的**。写了一个诊断而没验证它诊断得出东西，
跟写一句注释而没验证它说的是真的是同一个错。
**一个恒为空的诊断比没有诊断更糟：它让人以为「那边没什么可说的」。**

日志级别已改成 INFO，验过失败时确实能看到 Caddy 的启动与重载记录。
复现率约 3%（三十余轮见过一次），**低到无法用跑循环证明任何修法**——
所以没有改动产品代码，只改了「下次能看见多少」。

没有加重试来「修」它：重试会把这个信号永久掩埋，而
[ADR-0005](../docs/adr/0005-retry-only-transport-failures.md) 的整套分类恰恰依赖
「连不上」与「被拒绝」的区分——在测试装置里模糊掉它，等于把那条 ADR 想守的东西
先在自己家里拆了。

`internal/caddytest` 改成在失败时报告 **Caddy 进程是不是已经自己退出**，
那是当时最缺的一个事实。下次复现至少知道该往哪儿看。

（并行时同时会有二十来个 Caddy 实例。如果它变频繁，`go test -p 2` 能压低并发，
但那是绕开而不是解决。）
