# 节点部署

一条命令把一台干净的 Linux 机器变成边缘节点。

```bash
EDGE_ENROLL_TOKEN=<面板签发的 Token> ./edge-node.sh install \
    --node-id node-hk-01 \
    --master master.example.com:9000 \
    --agent-binary ./edge-agent-linux-amd64
```

支持 Debian / Ubuntu 与 RHEL 系（Rocky / AlmaLinux）。装的是**官方 Caddy**
（apt / dnf 官方源），不自建二进制——DNS-01 与验签都不在节点上做
（[ADR-0001](../docs/adr/0001-cert-issuance.md) / [ADR-0003](../docs/adr/0003-access-rules.md)），
因此不需要任何 Caddy 插件。

其余子命令：

```bash
./edge-node.sh verify                 # 自检：真查监听地址、沙箱、自动重启
./edge-node.sh uninstall [--keep-caddy]
```

## 已验证到什么程度

**这个脚本没有在真实 Linux 上跑过。** 开发机是 macOS，没有容器或虚拟机工具。
下面把「验过的」和「没验过的」分开列，因为这两者的可信度差得很远。

### 验过的（`./edge-node_test.sh`，63 条断言）

跑在开发机上，验的是脚本的**决策逻辑**——这是脚本里真正会出错的地方：

| 覆盖 | 用什么验的 |
| --- | --- |
| 发行版探测 | 6 份真实 `/etc/os-release`（Debian 12、Ubuntu 22.04、Rocky 9、Alma 9、Alpine、Arch） |
| 监听地址判定 | 6 份真实 `ss -ltnH` 输出，含 `0.0.0.0` / 通配 `*` / 内网 IP / `[::1]` / 完全没监听 |
| systemd 单元生成 | 断言凭据不在 `ExecStart`、`Restart=always`、13 项沙箱指令、`ReadWritePaths` |
| 防火墙规则 | ufw / firewalld / nftables 三套命令序列 |
| 幂等 | 真写临时目录、真比对内容，重复执行必须报告「无变化」 |
| 文件权限 | 凭据文件真落盘，`find -perm 600` |

另外 `shellcheck -s bash` 对两个文件都干净。做了 22 次变异验证，全部被测试抓住。

开发时 shellcheck 抓出过一个真 bug：`$id_like：` 里的全角冒号被 bash 当成了
变量名的一部分，导致「不支持的发行版」这条分支必崩。

### 没验过的

- **装包**：`apt-get` / `dnf` 的实际执行，官方源的 GPG key 与 sources.list 是否还是这个地址
- **systemd**：单元文件写出来了，但 systemd 认不认、沙箱选项在目标发行版的
  systemd 版本上是否都支持（老版本会因为不认识的指令拒绝启动整个单元）
- **防火墙**：命令生成了，但没在真机上执行过
- **接入**：`edge-agent enroll` 在真机上的表现
- **`verify` 子命令本身**：它调用 `systemctl show`、`stat -c`、`pgrep`，
  这些在 macOS 上跑不了

### 真机验收怎么做

拿一台干净的 Debian 12 或 Rocky 9：

```bash
# 1. 装 + 接入
EDGE_ENROLL_TOKEN=<token> ./edge-node.sh install \
    --node-id node-test-01 --master <主控>:9000 --agent-binary ./edge-agent

# 2. 幂等：再跑一遍，不该重启任何服务
systemctl show -p ActiveEnterTimestamp caddy      # 记下时间
EDGE_ENROLL_TOKEN=<同一个> ./edge-node.sh install ...同样的参数
systemctl show -p ActiveEnterTimestamp caddy      # 时间不该变

# 3. 加固逐条真查
ss -ltnp | grep -E ':(2019|2021)'                 # 只能是 127.0.0.1 / [::1]
curl -sS --max-time 3 http://<对外IP>:2019/config/ # 必须连不上
systemctl show -p Restart --value edge-agent      # always
systemctl show -p NoNewPrivileges --value edge-agent caddy
ps auxww | grep -i token                          # 什么都不该有

# 4. 自动重启
systemctl kill -s KILL edge-agent && sleep 5
systemctl is-active edge-agent                    # active

# 5. 卸载
./edge-node.sh uninstall
systemctl list-units 'edge-agent*' 'caddy*'       # 什么都不该剩
ls /etc/edge-agent                                # 不存在
```

## 已知缺口

**`--agent-binary` 是必填的，主控目前不分发 `edge-agent` 二进制。**

「单条命令完成安装」这条验收项因此**没有完全达成**：得先把二进制放到目标机器上，
或者提供一个可下载的 URL。

没有顺手加一个下载端点，是因为它需要自己的设计：未鉴权下载一个要以 root 运行的
二进制是个明显的问题面，而带鉴权又要解决「接入前还没有凭据」的先有鸡先有蛋。
从 URL 安装时脚本**强制要求** `--agent-sha256`，没有校验和就拒装——
网上下下来的二进制要以 root 跑，没校验等于把这台机器交给任何能劫持这条链路的人。

## 一并修好的两个装配漏洞

写这个脚本时发现的，两个都是「单测和 e2e 全绿、生产上不工作」：

1. **校验端点从没启动过。** `cmd/agent` 没设过 `VerifyAddr`，而 `Config` 的约定是
   「为空则不起」——访问规则在生产上完全不工作。参数解析已抽到
   `internal/agent.ParseArgs` 并加了测试，默认值现在有人守着。
2. **主控渲染的 `forward_auth` 指向虚空。** `render.DefaultOptions()` 没带
   `VerifyAddr`。现在渲染器会在「有受保护域名却没有校验端点地址」时**拒绝渲染**——
   这道线在唯一权威那里守住，而不是指望每个调用方都记得填。
