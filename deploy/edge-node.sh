#!/usr/bin/env bash
#
# edge-node.sh —— 把一台干净的 Linux 机器变成边缘节点。
#
#   安装并接入：  EDGE_ENROLL_TOKEN=<token> ./edge-node.sh install \
#                   --node-id node-hk-01 --master master.example.com:9000
#   卸载：        ./edge-node.sh uninstall
#   自检：        ./edge-node.sh verify
#
# 幂等：重复执行不产生副作用，可以放心重跑。
#
# 装的是**官方 Caddy**（apt/dnf 官方源），不自建二进制——DNS-01 与验签都不在
# 节点上做（ADR-0001 / ADR-0003），因此不需要任何 Caddy 插件。
#
# 接入 Token 只走环境变量与 EnvironmentFile，**绝不进 ExecStart**：命令行参数
# 会出现在 ps 输出里，任何本机用户都看得到。
#
# 本文件被 deploy/edge-node_test.sh source 进去测其中的纯函数，因此顶层不做
# 任何有副作用的事，main 只在直接执行时调用。
set -euo pipefail

EDGE_AGENT_BIN="${EDGE_AGENT_BIN:-/usr/local/bin/edge-agent}"
EDGE_STATE_DIR="${EDGE_STATE_DIR:-/etc/edge-agent}"
EDGE_ENV_FILE="${EDGE_ENV_FILE:-/etc/edge-agent/agent.env}"
SYSTEMD_DIR="${SYSTEMD_DIR:-/etc/systemd/system}"
CADDY_ADMIN_PORT="${CADDY_ADMIN_PORT:-2019}"
VERIFY_PORT="${VERIFY_PORT:-2021}"

# ── 发行版探测 ──

# os_release_field 取出 os-release 里某个键的值，去掉可能的引号。
os_release_field() {
  local file="$1" key="$2"
  awk -F= -v k="$key" '
    $1 == k {
      v = substr($0, index($0, "=") + 1)
      gsub(/^"|"$/, "", v)
      print v
      exit
    }
  ' "$file"
}

# detect_family 判断发行版家族，只认 debian 与 rhel。
#
# 认错了会用错的包管理器跑到一半失败，留下半装状态：Caddy 装了、Agent 没装、
# 防火墙规则加了一半。所以不认识的当场拒绝，不猜。
#
# 用 ID_LIKE 而不是穷举 ID：Ubuntu 的衍生版有几十个，穷举必然漏。
detect_family() {
  local file="${1:-/etc/os-release}"
  if [ ! -r "$file" ]; then
    echo "读不到 ${file}，无法判断发行版" >&2
    return 1
  fi

  local id id_like
  # 解析而不是 source。os-release 按规范是可以 source 的，但这个脚本以 root
  # 跑，source 等于执行文件里的任意内容——解析拿到的是同样的值，代价是零。
  id="$(os_release_field "$file" ID)"
  id_like="$(os_release_field "$file" ID_LIKE)"

  case " $id $id_like " in
    *" debian "*|*" ubuntu "*)
      printf 'debian'
      return 0
      ;;
    *" rhel "*|*" centos "*|*" fedora "*|*" rocky "*|*" almalinux "*)
      printf 'rhel'
      return 0
      ;;
  esac

  case "$id" in
    alpine)
      echo "不支持 Alpine：整套加固建立在 systemd 上（沙箱、自动重启），而 Alpine 用 OpenRC" >&2
      ;;
    *)
      echo "不支持的发行版 ID=${id} ID_LIKE=${id_like}：官方 Caddy 只提供 apt/dnf 源" >&2
      ;;
  esac
  return 1
}

# ── 防火墙 ──

# firewall_plan 打印某个后端要执行的规则命令，一行一条。
#
# 防火墙是**第二道防线**：第一道是 Caddy Admin 只绑回环。两道都要有——
# 配置改错了还有防火墙兜着，防火墙被刷掉了还有绑定地址兜着。
#
# 生成命令而不是直接执行，是为了让规则本身可测、可打印给人看：
# 「这条命令会做什么」比「脚本刚才做了什么」容易审。
firewall_plan() {
  local backend="$1"
  case "$backend" in
    ufw)
      # SSH 必须先放行。把它关在门外等于把自己锁在门外，而这台机器
      # 可能在别的大洲——ufw enable 会立刻切断当前连接。
      cat <<'PLAN'
ufw allow 22/tcp
ufw allow 80/tcp
ufw allow 443/tcp
PLAN
      # Admin 与校验端点都只在回环上，本不该被外部触达；显式 deny 是为了
      # 万一哪天绑定地址被改错，还有这一层拦着
      printf 'ufw deny %s/tcp\n' "$CADDY_ADMIN_PORT"
      printf 'ufw deny %s/tcp\n' "$VERIFY_PORT"
      echo 'ufw --force enable'
      ;;
    firewalld)
      # firewalld 默认区域拒绝未放行的端口，因此不需要显式 deny；
      # 要保证的是别顺手把 Admin 端口放行了
      cat <<'PLAN'
firewall-cmd --permanent --add-service=ssh
firewall-cmd --permanent --add-service=http
firewall-cmd --permanent --add-service=https
firewall-cmd --reload
PLAN
      ;;
    nftables)
      # 回环必须放行：Agent 要连本机 Caddy Admin，拦了它下发会全部失败，
      # 而现象是「所有节点下发超时」——很容易被当成网络问题查半天
      cat <<'PLAN'
table inet edge {
  chain input {
    type filter hook input priority filter; policy drop;
    ct state established,related accept
    iif "lo" accept
    tcp dport 22 accept
    tcp dport { 80, 443 } accept
    ip protocol icmp accept
    icmpv6 type { echo-request, nd-neighbor-solicit, nd-neighbor-advert, nd-router-advert } accept
  }
}
PLAN
      ;;
    *)
      echo "不认识的防火墙后端 ${backend}：只支持 ufw / firewalld / nftables" >&2
      return 1
      ;;
  esac
}

# detect_firewall 挑一个本机可用的后端。
detect_firewall() {
  if command -v ufw >/dev/null 2>&1; then
    printf 'ufw'
  elif command -v firewall-cmd >/dev/null 2>&1; then
    printf 'firewalld'
  elif command -v nft >/dev/null 2>&1; then
    printf 'nftables'
  else
    echo "本机没有 ufw / firewall-cmd / nft，跳过防火墙配置" >&2
    return 1
  fi
}

# ── 监听地址判定 ──

# port_is_loopback_only 判断某端口是否只在回环上监听。
#
# 输入是 `ss -ltnH` 的输出（第一个参数给文件时读文件，便于测试喂真实样本）。
#
# 三种结果都算失败，理由各不相同：
#   - 绑在对外地址上：Admin 没有鉴权，能连上就等于拥有这台节点
#   - 绑在通配 * / 0.0.0.0 上：同上，只是更彻底
#   - 根本没在监听：Admin 没起来的话下发会全部失败；报「通过」会让人以为正常
#
# 端口用**精确匹配**：子串匹配会让 2019 命中 20190，而那是另一个进程。
port_is_loopback_only() {
  local src="$1" port="$2"
  local lines
  lines="$(awk -v p=":${port}" '
    $1 == "LISTEN" {
      addr = $4
      if (substr(addr, length(addr) - length(p) + 1) == p) print addr
    }
  ' "$src")"

  if [ -z "$lines" ]; then
    echo "端口 ${port} 上没有任何进程在监听" >&2
    return 1
  fi

  local addr host bad=0
  while IFS= read -r addr; do
    host="${addr%:*}"
    host="${host#[}"
    host="${host%]}"
    case "$host" in
      127.*|::1) ;;
      *)
        echo "端口 ${port} 监听在 ${addr}，不是回环地址" >&2
        bad=1
        ;;
    esac
  done <<<"$lines"
  return "$bad"
}

# ── systemd 单元生成 ──

# agent_unit 生成 edge-agent 的 systemd 单元。
#
# 三件事是承重的，不是风格问题：
#
#   1. 凭据只在 EnvironmentFile 里，**绝不进 ExecStart**——命令行参数出现在
#      ps 输出里，任何本机用户都看得到。
#   2. Restart=always：受保护域名的 fail-closed 依赖 Agent 存活（ADR-0003），
#      它挂掉那一刻鉴权就没了。
#   3. 沙箱：节点被打穿之后，这是唯一还在的那道墙。
agent_unit() {
  local node_id="$1" master="$2"
  cat <<UNIT
[Unit]
Description=Edge Controller Agent
Documentation=https://github.com/xltxb/edge_caddy
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
# 凭据走 EnvironmentFile 而不是 ExecStart：命令行参数会出现在 ps 输出里
EnvironmentFile=-${EDGE_ENV_FILE}
ExecStart=${EDGE_AGENT_BIN} run \\
  --node-id ${node_id} \\
  --master ${master} \\
  --state-dir ${EDGE_STATE_DIR}/pki \\
  --caddy-admin http://127.0.0.1:${CADDY_ADMIN_PORT} \\
  --verify-addr 127.0.0.1:${VERIFY_PORT}

# 受保护域名的 fail-closed 依赖 Agent 存活，挂了必须马上回来
Restart=always
RestartSec=3s
# 起不来时别把 CPU 打满，但也别放弃重试：StartLimitIntervalSec=0 表示不设上限
StartLimitIntervalSec=0

# ── 沙箱 ──
NoNewPrivileges=yes
PrivateTmp=yes
PrivateDevices=yes
ProtectSystem=strict
ProtectHome=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectKernelLogs=yes
ProtectControlGroups=yes
ProtectClock=yes
ProtectHostname=yes
ProtectProc=invisible
RestrictNamespaces=yes
RestrictRealtime=yes
RestrictSUIDSGID=yes
LockPersonality=yes
MemoryDenyWriteExecute=yes
SystemCallArchitectures=native
SystemCallFilter=@system-service
SystemCallErrorNumber=EPERM
# 只需要 IPv4/IPv6 与本机 Unix 套接字
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
# ProtectSystem=strict 之下整个文件系统只读；证书目录必须显式放开，
# 否则接入时写证书失败，而报错是 EROFS——看着像磁盘坏了
ReadWritePaths=${EDGE_STATE_DIR}

[Install]
WantedBy=multi-user.target
UNIT
}

# caddy_override 生成官方 caddy.service 的**覆盖**片段。
#
# 用 drop-in 而不是改官方单元：官方包升级会覆盖它自己的单元文件，改在那里的
# 加固会在某次 apt upgrade 之后无声消失。
#
# 覆盖里不出现 ExecStart：配置由主控经 Admin API 下发（ADR-0004），
# 节点上的 Caddyfile 是什么样与我们无关。
caddy_override() {
  cat <<UNIT
[Service]
# Caddy 要绑 80/443，只保留这一项能力，其余全部丢掉
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE

NoNewPrivileges=yes
PrivateTmp=yes
PrivateDevices=yes
ProtectHome=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectKernelLogs=yes
ProtectControlGroups=yes
ProtectClock=yes
ProtectHostname=yes
ProtectProc=invisible
RestrictNamespaces=yes
RestrictRealtime=yes
RestrictSUIDSGID=yes
LockPersonality=yes
SystemCallArchitectures=native
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
UNIT
}

# agent_env_file 生成接入凭据文件的内容。
#
# 落盘时权限收到 0600（见 install_agent）。它是这台机器上唯一一处出现 Token
# 的地方——单元文件、命令行、日志里都不该有。
agent_env_file() {
  local token="$1"
  cat <<ENV
# 由 edge-node.sh 生成。**不要**把它加进版本库或备份到别处。
# Token 是一次性的：用过即失效，接入成功后这个文件就没有价值了。
EDGE_ENROLL_TOKEN=${token}
ENV
}

# ── 安装 ──

log()  { printf '\033[1;36m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m警告:\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31m错误:\033[0m %s\n' "$*" >&2; exit 1; }

need_root() {
  [ "$(id -u)" -eq 0 ] || die "需要 root 权限（装包、写 systemd 单元、改防火墙）"
}

# write_if_changed 只在内容有变化时写文件，并回报是否写过。
#
# 幂等的关键：无脑重写会让每次执行都触发 daemon-reload 与重启，而重启 Caddy
# 意味着断掉所有正在进行的连接——「重跑一遍确认一下」不该有这种代价。
write_if_changed() {
  local path="$1" mode="$2" content="$3"
  if [ -f "$path" ] && [ "$(cat "$path")" = "$content" ]; then
    chmod "$mode" "$path"
    return 1
  fi
  mkdir -p "$(dirname "$path")"
  printf '%s' "$content" >"$path"
  chmod "$mode" "$path"
  return 0
}

# write_units 写入两个单元文件，**任一有变化就返回 0**（有变化），
# 全都没变返回 1。
#
# 返回值决定要不要 daemon-reload 与重启。无脑重写会让每次执行都重启 Caddy，
# 断掉所有正在进行的连接——「重跑一遍确认一下」不该有这种代价。
write_units() {
  local node_id="$1" master="$2" changed=1
  if write_if_changed "${SYSTEMD_DIR}/edge-agent.service" 0644 \
      "$(agent_unit "$node_id" "$master")"; then
    changed=0
  fi
  if write_if_changed "${SYSTEMD_DIR}/caddy.service.d/10-edge-hardening.conf" 0644 \
      "$(caddy_override)"; then
    changed=0
  fi
  return "$changed"
}

install_caddy() {
  local family="$1"
  if command -v caddy >/dev/null 2>&1; then
    log "Caddy 已安装，跳过"
    return 0
  fi
  # 装官方包，不自建二进制：DNS-01 与验签都不在节点上做
  # （ADR-0001 / ADR-0003），因此不需要任何 Caddy 插件。
  case "$family" in
    debian)
      log "从官方 apt 源安装 Caddy"
      apt-get update -qq
      apt-get install -y -qq debian-keyring debian-archive-keyring apt-transport-https curl gpg
      curl -fsSL 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' \
        | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
      curl -fsSL 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' \
        >/etc/apt/sources.list.d/caddy-stable.list
      apt-get update -qq
      apt-get install -y -qq caddy
      ;;
    rhel)
      log "从官方 dnf 源安装 Caddy"
      dnf install -y -q 'dnf-command(copr)'
      dnf copr enable -y @caddy/caddy
      dnf install -y -q caddy
      ;;
  esac
}

install_agent_binary() {
  local src="$1" sha="$2"
  case "$src" in
    http://*|https://*)
      # 网上下下来的二进制要以 root 跑，没有校验和就装等于把这台机器交给
      # 任何能劫持这条链路的人。宁可拒装。
      [ -n "$sha" ] || die "从 URL 安装必须同时提供 --agent-sha256"
      log "下载 edge-agent"
      local tmp
      tmp="$(mktemp)"
      curl -fsSL "$src" -o "$tmp"
      local got
      got="$(sha256sum "$tmp" | awk '{print $1}')"
      if [ "$got" != "$sha" ]; then
        rm -f "$tmp"
        die "edge-agent 校验和不符：期望 ${sha}，实际 ${got}"
      fi
      install -m 0755 "$tmp" "$EDGE_AGENT_BIN"
      rm -f "$tmp"
      ;;
    *)
      [ -f "$src" ] || die "找不到 edge-agent 二进制：${src}"
      install -m 0755 "$src" "$EDGE_AGENT_BIN"
      ;;
  esac
}

do_install() {
  local node_id="" master="" agent_src="" agent_sha="" ca_path=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --node-id)        node_id="$2"; shift 2 ;;
      --master)         master="$2"; shift 2 ;;
      --agent-binary)   agent_src="$2"; shift 2 ;;
      --agent-sha256)   agent_sha="$2"; shift 2 ;;
      --master-ca)      ca_path="$2"; shift 2 ;;
      *) die "未知参数 $1" ;;
    esac
  done
  [ -n "$node_id" ] || die "必须指定 --node-id"
  [ -n "$master" ]  || die "必须指定 --master（host:port）"
  [ -n "$agent_src" ] || die "必须指定 --agent-binary（路径或 URL）；主控目前不分发二进制"
  [ -n "${EDGE_ENROLL_TOKEN:-}" ] || die "必须通过环境变量提供 EDGE_ENROLL_TOKEN（不要用命令行参数，ps 里看得到）"

  need_root
  local family
  family="$(detect_family)" || exit 1
  log "发行版家族：${family}"

  install_caddy "$family"
  install_agent_binary "$agent_src" "$agent_sha"

  mkdir -p "${EDGE_STATE_DIR}/pki"
  chmod 0700 "$EDGE_STATE_DIR" "${EDGE_STATE_DIR}/pki"

  # 凭据文件 0600：它是这台机器上唯一一处出现 Token 的地方
  write_if_changed "$EDGE_ENV_FILE" 0600 "$(agent_env_file "$EDGE_ENROLL_TOKEN")" || true

  local changed=0
  if write_units "$node_id" "$master"; then
    changed=1
    systemctl daemon-reload
  fi

  # 接入只做一次：Token 是一次性的，重跑时它已经失效了。
  # 判据是证书在不在，而不是「脚本跑没跑过」——后者在重装系统后会骗人。
  if [ -f "${EDGE_STATE_DIR}/pki/tunnel.crt" ]; then
    log "已接入过（证书已存在），跳过接入"
  else
    log "接入主控"
    local ca_args=()
    [ -n "$ca_path" ] && ca_args=(--master-ca "$ca_path")
    EDGE_ENROLL_TOKEN="$EDGE_ENROLL_TOKEN" "$EDGE_AGENT_BIN" enroll \
      --node-id "$node_id" --master "$master" \
      --state-dir "${EDGE_STATE_DIR}/pki" "${ca_args[@]}" \
      || die "接入失败。Token 是一次性的，重试前请到面板重新签发一个"
  fi

  # 接入成功后凭据就没价值了，留着只是多一处泄漏面
  rm -f "$EDGE_ENV_FILE"

  log "启用服务"
  systemctl enable --now caddy
  systemctl enable --now edge-agent
  if [ "$changed" -eq 1 ]; then
    systemctl try-restart caddy edge-agent
  fi

  apply_firewall
  echo
  do_verify
}

apply_firewall() {
  local backend
  if ! backend="$(detect_firewall)"; then
    warn "跳过防火墙配置。Caddy Admin 仍然只绑回环，但少了第二道防线"
    return 0
  fi
  log "配置防火墙（${backend}）"
  case "$backend" in
    nftables)
      local conf=/etc/nftables.d/edge.nft
      mkdir -p /etc/nftables.d
      firewall_plan nftables >"$conf"
      nft -f "$conf"
      ;;
    *)
      firewall_plan "$backend" | while IFS= read -r cmd; do
        [ -n "$cmd" ] || continue
        # shellcheck disable=SC2086 # 规则命令是本脚本自己生成的，需要按词拆分
        eval $cmd
      done
      ;;
  esac
}

# ── 自检 ──

VERIFY_FAILED=0
check() { # 说明 命令...
  local desc="$1"; shift
  if "$@" >/dev/null 2>&1; then
    printf '  \033[32m✓\033[0m %s\n' "$desc"
  else
    printf '  \033[31m✘\033[0m %s\n' "$desc"
    VERIFY_FAILED=1
  fi
}

do_verify() {
  log "自检"
  local ss_out
  ss_out="$(mktemp)"
  ss -ltnH >"$ss_out" 2>/dev/null || warn "没有 ss 命令，监听地址无法验证"

  check "caddy 正在运行"       systemctl is-active --quiet caddy
  check "edge-agent 正在运行"  systemctl is-active --quiet edge-agent
  check "edge-agent 开机自启"  systemctl is-enabled --quiet edge-agent

  # 真查监听地址，而不是相信配置文件里写了什么：配置写对了但没重载、
  # 或者被别的配置覆盖，都会让文件与现实脱节
  check "Caddy Admin 只在回环上（${CADDY_ADMIN_PORT}）" \
    port_is_loopback_only "$ss_out" "$CADDY_ADMIN_PORT"
  check "校验端点只在回环上（${VERIFY_PORT}）" \
    port_is_loopback_only "$ss_out" "$VERIFY_PORT"
  rm -f "$ss_out"

  check "凭据文件未残留" test ! -e "$EDGE_ENV_FILE"
  check "证书目录不对外可读" test "$(stat -c '%a' "${EDGE_STATE_DIR}/pki" 2>/dev/null)" = "700"
  # 接入凭据不得出现在进程列表里
  check "Token 不在进程列表里" test -z "$(pgrep -af 'EDGE_ENROLL_TOKEN|enroll.*--token' || true)"

  # Restart=always 是承重的：受保护域名的 fail-closed 依赖 Agent 存活
  check "Agent 配了自动重启" \
    sh -c "systemctl show -p Restart --value edge-agent | grep -qx always"
  check "Agent 沙箱已生效" \
    sh -c "systemctl show -p NoNewPrivileges --value edge-agent | grep -qx yes"
  check "Caddy 沙箱已生效" \
    sh -c "systemctl show -p NoNewPrivileges --value caddy | grep -qx yes"

  if [ "$VERIFY_FAILED" -eq 0 ]; then
    log "自检通过"
  else
    die "自检未通过，见上面标红的项"
  fi
}

# ── 卸载 ──

do_uninstall() {
  local keep_caddy=0
  while [ $# -gt 0 ]; do
    case "$1" in
      --keep-caddy) keep_caddy=1; shift ;;
      *) die "未知参数 $1" ;;
    esac
  done
  need_root

  log "停止并禁用 edge-agent"
  systemctl disable --now edge-agent 2>/dev/null || true
  rm -f "${SYSTEMD_DIR}/edge-agent.service"
  rm -f "${SYSTEMD_DIR}/caddy.service.d/10-edge-hardening.conf"
  rmdir "${SYSTEMD_DIR}/caddy.service.d" 2>/dev/null || true
  systemctl daemon-reload

  rm -f "$EDGE_AGENT_BIN" "$EDGE_ENV_FILE"
  # 证书与私钥一并删掉：留着等于在一台已经不属于集群的机器上留一把钥匙
  rm -rf "$EDGE_STATE_DIR"

  if [ "$keep_caddy" -eq 0 ]; then
    log "卸载 Caddy"
    systemctl disable --now caddy 2>/dev/null || true
    if command -v apt-get >/dev/null 2>&1; then
      apt-get purge -y -qq caddy || true
      rm -f /etc/apt/sources.list.d/caddy-stable.list
      rm -f /usr/share/keyrings/caddy-stable-archive-keyring.gpg
    elif command -v dnf >/dev/null 2>&1; then
      dnf remove -y -q caddy || true
    fi
  else
    log "保留 Caddy（--keep-caddy）"
  fi

  # 防火墙规则**不自动回滚**：这台机器上的 80/443 放行未必只是给我们用的，
  # 顺手关掉可能把别的服务一起断了。要清理请自己看一眼再动手。
  warn "防火墙规则未回滚。若这台机器不再对外提供服务，请自行收回 80/443"
  log "卸载完成"
}

usage() {
  cat <<'USAGE'
用法:
  EDGE_ENROLL_TOKEN=<token> edge-node.sh install \
      --node-id <节点ID> --master <host:port> \
      --agent-binary <路径或URL> [--agent-sha256 <校验和>] [--master-ca <路径>]

  edge-node.sh verify              自检：真查监听地址、沙箱、自动重启
  edge-node.sh uninstall [--keep-caddy]

接入 Token 只能走环境变量。命令行参数会出现在 ps 输出里，任何本机用户都看得到。
USAGE
}

# ── 入口 ──

main() {
  local cmd="${1:-}"
  [ $# -gt 0 ] && shift
  case "$cmd" in
    install)   do_install "$@" ;;
    verify)    need_root; do_verify ;;
    uninstall) do_uninstall "$@" ;;
    ""|-h|--help|help) usage ;;
    *) usage; die "未知子命令 ${cmd}" ;;
  esac
}

# 只在直接执行时跑 main；被 source 时只定义函数，供测试使用。
if [ "${BASH_SOURCE[0]}" = "${0}" ]; then
  main "$@"
fi
