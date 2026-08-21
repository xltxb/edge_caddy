#!/usr/bin/env bash
#
# 把一台机器变成边缘节点：装官方 Caddy、装 edge-agent、写 systemd 单元、
# 用一次性 Token 完成接入。
#
# # 这个脚本承载了几条不是风格问题的约束
#
#   1. **节点跑 apt/dnf 装的官方 Caddy**，不自建二进制。ADR-0001 与 ADR-0003
#      都建立在这条上：官方包没有 DNS provider（所以证书由主控签发），
#      也没有 JWT/HMAC 模块（所以鉴权走 forward_auth 委托给 Agent）。
#      这里装的必须是官方包。
#
#   2. **Agent 的 Restart=always 是承重的**，不是锦上添花。受保护域名的
#      fail-closed 依赖 Agent 存活（ADR-0003）——它挂掉那一刻，那些域名
#      整体 502。这是正确的安全姿态，代价就是 Agent 成了硬依赖。
#
#   3. **凭据只进 EnvironmentFile，绝不进 ExecStart。** 命令行参数出现在
#      ps 输出里，本机任何用户都看得到。
#
#   4. **Caddy Admin 只监听回环。** 证书私钥以 load_pem 内联在运行配置里
#      （ADR-0010），能读 Admin 就能读到它们。ADR-0010 说过「部署脚本会用
#      防火墙再兜一层」——这里兑现那句话，并且**装完真的去查一遍**。
#
# # 这个脚本没做什么
#
#   它不下载 edge-agent 二进制。--agent-bin 指向一个已经在本机的文件，
#   或者预先放在 /usr/local/bin/edge-agent。分发二进制需要一个可信的来源，
#   而这套系统还没有那个东西——假装有会比没有更糟。
#
set -euo pipefail

readonly AGENT_BIN=/usr/local/bin/edge-agent
readonly AGENT_HOME=/var/lib/edge-agent
readonly AGENT_ENV=/etc/edge-agent.env
readonly AGENT_UNIT=/etc/systemd/system/edge-agent.service
readonly CADDY_DROPIN_DIR=/etc/systemd/system/caddy.service.d
readonly CADDY_ADMIN_PORT=2019
readonly VERIFY_PORT=2020

# ── 纯函数：吃输入、吐结果、不碰系统。测试只测这些。 ──

# os_family 从 os-release 文件判断发行版家族。
#
# 先看 ID 再看 ID_LIKE：Rocky 的 ID 是 rocky，认不出来，但 ID_LIKE 里有 rhel。
# 只看 ID 会让每个 RHEL 衍生版都要单独列一遍，而它们的包管理是一样的。
os_family() {
  local file="$1" id id_like
  id="$(os_release_field "$file" ID)"
  id_like="$(os_release_field "$file" ID_LIKE)"

  case "$id" in
    debian|ubuntu) echo debian; return 0 ;;
    rhel|centos|fedora|rocky|almalinux) echo rhel; return 0 ;;
    alpine) echo alpine; return 0 ;;
    arch) echo arch; return 0 ;;
  esac
  case " $id_like " in
    *" debian "*|*" ubuntu "*) echo debian; return 0 ;;
    *" rhel "*|*" fedora "*|*" centos "*) echo rhel; return 0 ;;
    *" arch "*) echo arch; return 0 ;;
  esac
  echo unknown
  return 1
}

os_release_field() {
  local file="$1" key="$2" val
  val="$(grep -E "^${key}=" "$file" 2>/dev/null | head -1 | cut -d= -f2-)" || true
  val="${val%\"}"; val="${val#\"}"
  printf '%s' "$val"
}

# caddy_install_plan 给出安装官方 Caddy 的命令。
#
# 只列有官方源的家族。**认不出来的家族直接失败**，不去猜一个 curl | bash
# ——猜错的后果是装上一个来路不明的二进制，而那正是 ADR-0001 拒绝自建二进制
# 想避免的东西。
caddy_install_plan() {
  case "$1" in
    debian)
      cat <<'PLAN'
apt-get install -y debian-keyring debian-archive-keyring apt-transport-https curl
curl -1sLf https://dl.cloudsmith.io/public/caddy/stable/gpg.key | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
curl -1sLf https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt | tee /etc/apt/sources.list.d/caddy-stable.list
apt-get update
apt-get install -y caddy
PLAN
      ;;
    rhel)
      cat <<'PLAN'
dnf install -y 'dnf-command(copr)'
dnf copr enable -y @caddy/caddy
dnf install -y caddy
PLAN
      ;;
    alpine)
      echo "apk add --no-cache caddy caddy-openrc"
      ;;
    arch)
      echo "pacman -S --noconfirm caddy"
      ;;
    *)
      echo "未知的发行版家族：$1" >&2
      return 1
      ;;
  esac
}

# port_is_loopback_only 判断某个端口是不是只监听在回环上。
#
# 吃 `ss -ltnp` 的输出而不是自己去跑 ss：这样它能被夹具喂，
# 而「监听地址判定」恰恰是这个脚本里最容易写错、又最要紧的一处。
port_is_loopback_only() {
  local src="$1" port="$2" addrs
  addrs="$(awk -v p=":${port}" '
    $1 == "LISTEN" {
      a = $4
      if (substr(a, length(a) - length(p) + 1) == p) print a
    }' "$src")"

  if [ -z "$addrs" ]; then
    echo "端口 ${port} 上没有任何进程在监听" >&2
    return 2   # 与「监听了但暴露」区分开：没在听和听错地方要分别处置
  fi

  local a host bad=0
  while IFS= read -r a; do
    host="${a%:*}"; host="${host#[}"; host="${host%]}"
    case "$host" in
      127.*|::1) ;;
      *) echo "端口 ${port} 监听在 ${a}，不是回环地址" >&2; bad=1 ;;
    esac
  done <<<"$addrs"
  return "$bad"
}

# agent_unit 生成 edge-agent 的 systemd 单元。
agent_unit() {
  cat <<'UNIT'
[Unit]
Description=Edge Controller Agent
After=network-online.target caddy.service
Wants=network-online.target
# 不是 Requires=caddy.service：Caddy 挂了 Agent 应当继续活着并把这件事报上去。
# Requires 会让它跟着一起停，于是主控看到的是「节点离线」——而真相是
# 「节点活着但 Caddy 挂了」。这两种故障的处置完全不同。

[Service]
Type=simple
# 凭据只在这里，**绝不进 ExecStart**：命令行参数出现在 ps 输出里，
# 本机任何用户都看得到。
EnvironmentFile=/etc/edge-agent.env
ExecStart=/usr/local/bin/edge-agent

# **Restart=always 是承重的。** 受保护域名的 fail-closed 依赖 Agent 存活
# （ADR-0003）：它挂掉那一刻，那些域名整体 502。
Restart=always
RestartSec=2

User=root
StateDirectory=edge-agent
StateDirectoryMode=0700

# 节点被打穿之后，这是唯一还在的那道墙。
NoNewPrivileges=yes
PrivateTmp=yes
ProtectSystem=strict
ProtectHome=yes
ReadWritePaths=/var/lib/edge-agent
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
LockPersonality=yes

[Install]
WantedBy=multi-user.target
UNIT
}

# agent_env 生成 EnvironmentFile。
#
# **Token 与 CA 指纹都在这里。** 指纹不是秘密（它是公开证书的哈希），
# 但把它和 Token 放在一起能保证接入那一刻两者都在——
# 少了指纹的接入会退化成 TOFU，而那正是 --ca-pin 要防的（ADR-0009）。
agent_env() {
  local master="$1" node_id="$2" token="$3" ca_pin="$4"
  cat <<ENV
EC_MASTER_ADDR=${master}
EC_NODE_ID=${node_id}
EC_ENROLL_TOKEN=${token}
EC_CA_PIN=${ca_pin}
EC_STATE_DIR=${AGENT_HOME}
EC_CADDY_ADMIN=http://127.0.0.1:${CADDY_ADMIN_PORT}
EC_VERIFY_LISTEN=127.0.0.1:${VERIFY_PORT}
ENV
}

# caddy_admin_dropin 把 Caddy Admin 钉在回环上。
#
# 官方包默认就是 localhost:2019，但那是**默认值**——一份被人改过的
# Caddyfile 可以把它挪到 0.0.0.0。证书私钥以 load_pem 内联在运行配置里
# （ADR-0010），能读 Admin 就能读到它们，所以这里显式钉死并在装完查一遍。
caddy_admin_dropin() {
  cat <<'UNIT'
[Service]
Environment=CADDY_ADMIN=127.0.0.1:2019
UNIT
}

# firewall_plan 给出要放行的端口。
#
# **443/udp 只在开了 HTTP/3 时才放行。** 无条件开一个 UDP 端口是白送一个
# 攻击面；而漏了它的症状很隐蔽——HTTP/3 握不上会静默回落到 TCP，
# 用户只觉得「有点慢」，没有任何报错。
firewall_plan() {
  local family="$1" http3="$2"
  echo "80/tcp"
  echo "443/tcp"
  [ "$http3" = "yes" ] && echo "443/udp"
  # **2019 与 2020 一个都不放行。** Caddy Admin 与 Agent 校验端点都只在
  # 回环上，放行它们等于把私钥和验签端点交出去。
  case "$family" in
    debian) echo "# ufw allow <port>" ;;
    rhel)   echo "# firewall-cmd --permanent --add-port=<port>" ;;
    *)      echo "# 请手动放行以上端口" ;;
  esac
}

# ── 以下会碰真系统 ──

log()  { printf '\033[1;36m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m警告:\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31m错误:\033[0m %s\n' "$*" >&2; exit 1; }

need_root() { [ "$(id -u)" = 0 ] || die "需要 root（用 sudo 跑）"; }

do_install() {
  local master="" node_id="" token="" ca_pin="" agent_src="" http3=no
  while [ $# -gt 0 ]; do
    case "$1" in
      --master)    master="$2"; shift 2 ;;
      --node-id)   node_id="$2"; shift 2 ;;
      --token)     token="$2"; shift 2 ;;
      --ca-pin)    ca_pin="$2"; shift 2 ;;
      --agent-bin) agent_src="$2"; shift 2 ;;
      --http3)     http3=yes; shift ;;
      *) die "未知参数 $1" ;;
    esac
  done
  [ -n "$master" ]  || die "--master 必填"
  [ -n "$node_id" ] || die "--node-id 必填"
  [ -n "$token" ]   || die "--token 必填"
  # **指纹必填，没有默认值。**
  # 缺了它接入会退化成 TOFU：中间人在首连那一刻冒充主控，就能把一次性 Token
  # 骗走（ADR-0009）。让它可选等于让人有机会省掉一道真实的保护。
  [ -n "$ca_pin" ]  || die "--ca-pin 必填 —— 没有它，接入首连无法确认对面就是你的主控"

  need_root
  local family
  family="$(os_family /etc/os-release)" || die "认不出这个发行版，请手动安装 Caddy 后再跑本脚本"
  log "发行版家族：$family"

  log "安装官方 Caddy"
  caddy_install_plan "$family" | while IFS= read -r cmd; do
    [ -z "$cmd" ] && continue
    log "  $cmd"
    eval "$cmd"
  done

  log "安装 edge-agent"
  if [ -n "$agent_src" ]; then
    install -m 0755 "$agent_src" "$AGENT_BIN"
  fi
  # 这个脚本**不下载**二进制。分发需要一个可信来源，而这套系统还没有那个东西
  # ——假装有会比没有更糟。
  [ -x "$AGENT_BIN" ] || die "$AGENT_BIN 不存在。用 --agent-bin 指向本机的二进制，或先自行放置。"

  install -d -m 0700 "$AGENT_HOME"

  log "写入配置与单元"
  agent_env "$master" "$node_id" "$token" "$ca_pin" > "$AGENT_ENV"
  chmod 0600 "$AGENT_ENV"   # 里面有一次性 Token
  agent_unit > "$AGENT_UNIT"
  install -d -m 0755 "$CADDY_DROPIN_DIR"
  caddy_admin_dropin > "$CADDY_DROPIN_DIR/admin-loopback.conf"

  systemctl daemon-reload
  systemctl enable --now caddy
  systemctl enable --now edge-agent

  log "防火墙要放行（本脚本不替你改防火墙）："
  firewall_plan "$family" "$http3" | sed 's/^/    /'

  log "装完了。跑 '$0 verify' 查一遍。"
}

# do_verify 查装完之后的状态。
#
# **每一条都真的去查，查不了的明说查不了。** 一份「全部 ✓」而其中几条其实
# 没查过的报告，比没有这份报告更糟——它会让人停止怀疑。
do_verify() {
  local rc=0 ss_out
  ss_out="$(mktemp)"; trap 'rm -f "$ss_out"' EXIT
  if command -v ss >/dev/null 2>&1; then
    ss -ltn > "$ss_out" 2>/dev/null || true
  else
    warn "没有 ss 命令，监听地址这几项**没有查**"
    : > "$ss_out"
  fi

  printf '\n'
  check_unit edge-agent || rc=1
  check_unit caddy || rc=1

  if [ -s "$ss_out" ]; then
    if port_is_loopback_only "$ss_out" "$CADDY_ADMIN_PORT" 2>/dev/null; then
      printf '  ✓ Caddy Admin 只监听回环\n'
    else
      case $? in
        2) printf '  ✘ Caddy Admin (%s) 没在监听 —— Caddy 可能没起来\n' "$CADDY_ADMIN_PORT" ;;
        *) printf '  ✘ Caddy Admin (%s) 暴露在回环之外 —— 证书私钥内联在运行配置里，能读 Admin 就能读到它们\n' "$CADDY_ADMIN_PORT" ;;
      esac
      rc=1
    fi
    if port_is_loopback_only "$ss_out" "$VERIFY_PORT" 2>/dev/null; then
      printf '  ✓ Agent 校验端点只监听回环\n'
    else
      printf '  ✘ Agent 校验端点 (%s) 不在回环上或没在监听\n' "$VERIFY_PORT"
      rc=1
    fi
  fi

  if [ -f "$AGENT_HOME/tunnel.crt" ]; then
    printf '  ✓ 已取得隧道证书（接入完成）\n'
  else
    printf '  ✘ 还没有隧道证书 —— 接入没完成，看 journalctl -u edge-agent\n'
    rc=1
  fi

  local mode
  mode="$(stat -c '%a' "$AGENT_ENV" 2>/dev/null || stat -f '%Lp' "$AGENT_ENV" 2>/dev/null || echo '?')"
  if [ "$mode" = "600" ]; then
    printf '  ✓ %s 权限 600\n' "$AGENT_ENV"
  else
    printf '  ✘ %s 权限是 %s，里面有接入 Token\n' "$AGENT_ENV" "$mode"
    rc=1
  fi

  printf '\n  下面这些**没有**查：Agent 与主控之间的隧道是否真的通、\n'
  printf '  证书是否真的被 Caddy 加载。那两件事在控制台上看得到\n'
  printf '  （节点在线状态、证书页的「N / M 个节点」）。\n\n'
  return "$rc"
}

check_unit() {
  if systemctl is-active --quiet "$1"; then
    printf '  ✓ %s 在跑\n' "$1"
    return 0
  fi
  printf '  ✘ %s 没在跑 —— journalctl -u %s\n' "$1" "$1"
  return 1
}

do_uninstall() {
  need_root
  systemctl disable --now edge-agent 2>/dev/null || true
  rm -f "$AGENT_UNIT" "$AGENT_ENV" "$CADDY_DROPIN_DIR/admin-loopback.conf"
  systemctl daemon-reload
  # **不删 $AGENT_HOME**：里面有隧道证书。删掉就必须重新走接入流程，
  # 而卸载脚本多半是在排障时跑的——那时候把身份一起弄丢会让处境更糟。
  warn "保留了 $AGENT_HOME（含隧道证书）。确实要清就手动 rm -rf。"
  log "已卸载 edge-agent。Caddy 没有动。"
}

usage() {
  cat <<USAGE
用法：
  $0 install --master <host:port> --node-id <id> --token <一次性> --ca-pin <sha256>
             [--agent-bin <路径>] [--http3]
  $0 verify
  $0 uninstall

--ca-pin 是主控隧道 CA 证书的 SHA-256，控制台「添加节点」时一并给出。
**它不能省也不能改**：接入首连时本机还没有 CA，指纹是确认对面就是你的主控的
唯一依据（ADR-0009）。

本脚本**不下载** edge-agent 二进制。用 --agent-bin 指向本机已有的文件。
USAGE
}

main() {
  case "${1:-}" in
    install)   shift; do_install "$@" ;;
    verify)    shift; do_verify ;;
    uninstall) shift; do_uninstall ;;
    ""|-h|--help) usage ;;
    *) usage; exit 1 ;;
  esac
}

# 被 source 时不执行 main，好让测试能调里面的纯函数。
if [ "${BASH_SOURCE[0]}" = "${0}" ]; then
  main "$@"
fi
