# 部署

两个可执行体，两种装法。

## 边缘节点

**控制台「添加节点」直接给出这条命令**（`install_cmd` 字段），复制粘贴即可：

```bash
sudo ./edge-node.sh install \
  --master ec.internal:9000 \
  --node-id node-hk-01 \
  --token ec_1f9a… \
  --ca-pin 9e8f22a3430a2f859aee5b47… \
  --agent-bin ./edge-agent
sudo ./edge-node.sh verify
```

`install_cmd` 是**一条要执行的命令**，不是「两个值的来源」——Token 与 CA 指纹
在响应里另有 `token` 与 `ca_pin` 两个字段，要单独取值就取那两个。

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
export EC_ADVERTISE="ec.internal:9000"      # 进服务端证书 SAN，也拼进安装命令
export EC_HTTP_ADDR="127.0.0.1:8080"        # ← 见下

./master -migrate
./master --create-user 'abiu:…'
./master -ca-pin                            # 打印 CA 指纹，添加节点时要用
./master
```

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

到目前为止见过两次、累计二十余轮未能复现，两次都没抓到现场：
第一次的证据只有那行错误，第二次连是哪条测试都没记下来（我当时的统计脚本
只数了失败个数）。**统计方式本身也会丢证据**——这一点比那个 flake 本身
更值得记。

没有加重试来「修」它：重试会把这个信号永久掩埋，而
[ADR-0005](../docs/adr/0005-retry-only-transport-failures.md) 的整套分类恰恰依赖
「连不上」与「被拒绝」的区分——在测试装置里模糊掉它，等于把那条 ADR 想守的东西
先在自己家里拆了。

`internal/caddytest` 改成在失败时报告 **Caddy 进程是不是已经自己退出**，
那是当时最缺的一个事实。下次复现至少知道该往哪儿看。

（并行时同时会有二十来个 Caddy 实例。如果它变频繁，`go test -p 2` 能压低并发，
但那是绕开而不是解决。）
