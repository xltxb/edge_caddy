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
# Agent 跑在这个专用系统用户下，**不是 root**。理由见 agent_unit。
readonly AGENT_USER=edge-agent
readonly AGENT_ENV=/etc/edge-agent.env
readonly AGENT_UNIT=/etc/systemd/system/edge-agent.service
readonly CADDY_DROPIN_DIR=/etc/systemd/system/caddy.service.d
readonly CADDY_LOG_DIR=/var/log/caddy
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

# agent_user_plan 给出建专用系统用户的命令。
#
# 形状跟 caddy_install_plan 一样（吐命令、调用方 eval），理由也一样：
# **认不出的家族直接失败，不去猜。** 猜错的后果是用户没建成、Agent 起不来，
# 而 systemd 报的是 `217/USER` —— 那个错误码不会有人联想到发行版探测。
#
# **每条都先判存在**：重装和升级都会再跑一次 do_install，而 useradd 撞上
# 已存在的用户是非零退出，那会让整个安装在这一步断掉。
agent_user_plan() {
  local family="$1"
  case "$family" in
    debian|rhel|arch)
      # --system 拿的是系统 UID 段，不进 /etc/login.defs 的普通用户范围。
      # --no-create-home 是因为它的状态目录是 StateDirectory= 管的
      # （/var/lib/edge-agent），家目录只会多一个没人用的空目录。
      echo "id -u ${AGENT_USER} >/dev/null 2>&1 || useradd --system --no-create-home --shell /usr/sbin/nologin ${AGENT_USER}"
      ;;
    alpine)
      # busybox 的 adduser 没有长选项：-S 系统用户、-D 不设密码、-H 不建家目录。
      echo "id -u ${AGENT_USER} >/dev/null 2>&1 || adduser -S -D -H -s /sbin/nologin ${AGENT_USER}"
      ;;
    *)
      echo "认不出发行版家族 [$family]，不知道怎么建系统用户" >&2
      return 1
      ;;
  esac
}

# agent_unit 生成 edge-agent 的 systemd 单元。
#
# **heredoc 不加引号**，因为下面要展开 ${AGENT_USER}——用户名在脚本里
# 只有一份真相（那个 readonly），单元文件、chown、verify 都取自它。
# 单元内容里没有 `$` 也没有反引号，所以展开是安全的。
agent_unit() {
  cat <<UNIT
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

# **不是 root。**
#
# 盘过 Agent 的全部特权操作，一项都不需要：写文件只落在 StateDir
#（agent.go 的 certPath 与 StateDir、geoip.go 的 mmdb），校验端点监听
# 127.0.0.1:2020 是非特权端口，出站连主控与访问 Caddy Admin 都不要特权，
# 排空走 Caddy 的 metrics（drain.go 的 countConns）而不是 /proc。
#
# 回源证书路径虽然是主控下发的（PushConfig.upstream_cert_path），看起来
# 像个任意文件写入口，但它的默认值就在 StateDir 里（config.go 的
# EC_UPSTREAM_CERT），而下面的 ReadWritePaths 早已把它锁死在那儿了。
User=${AGENT_USER}
Group=${AGENT_USER}
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

# **空的 capability 集，两行都要。**
#
# 非 root 进程本来就没有 capability，所以这两行**现在**是冗余的。
# 它们防的是以后：哪天有人为了图省事把 User= 改回 root，
# 这两行让那件事不再等于「顺手拿回全部特权」。
CapabilityBoundingSet=
AmbientCapabilities=

# 以下这些在 root 身份下大多形同虚设，降权之后才真正成立。
PrivateDevices=yes
ProtectClock=yes
ProtectHostname=yes
ProtectKernelLogs=yes
ProtectProc=invisible
ProcSubset=pid
RestrictSUIDSGID=yes
RestrictNamespaces=yes
RestrictRealtime=yes
SystemCallArchitectures=native
SystemCallFilter=@system-service
UMask=0077

# **MemoryDenyWriteExecute 是故意没有的，而上面那些留着——凭的是
# 「出问题时什么时候暴露」，不是「有没有风险」。**
#
# ProcSubset=pid 万一挡了 Go runtime 要读的东西，进程**起不来**：
# harden 三十秒等不到隧道就把单元换回去，install 装完 verify 立刻报红。
# 这条路上有网。
#
# MemoryDenyWriteExecute 不一样：它的故障是进程先正常跑起来，之后在某次
# GC 或某个 cgo 调用上崩。那三十秒是绿的，网接不住 —— 而 Agent 挂掉那一刻
# 受保护域名整体 502（ADR-0003 的 fail-closed）。
#
# 起不来的风险有回滚兜着，跑着跑着崩没有。所以留前者、去后者。

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

# preflight_master 在**动这台机器之前**先确认它连得上主控的隧道端口。
#
# 少了这一步，装机会一路成功——装 Caddy、写 systemd 单元、起 Agent——
# 然后节点**永远不出现在控制台里**，而这台机器上没有任何东西说得出为什么。
# 人会去查 Token、查指纹、查防火墙，而问题可能只是那个域名被 CDN 代理了。
#
# **这不是启发式判断，是真前提**：连不上隧道端口，这次安装无论如何不可能工作。
# 所以它拒绝继续，而不是警告一句然后接着装。
preflight_master() {
  local addr="$1" host port rest

  case "$addr" in
    wss://*|ws://*)
      # 隧道走 HTTP 面：主机名后面可能带端口，也可能带路径。
      # **不能用 ${addr%:*} 那一套**——`wss://cdn.example.com` 会被切成
      # host="wss"，然后拿着 "wss" 去连，报的是「域名解析失败」，
      # 而人会去查 DNS，那儿没有问题。
      rest="${addr#*://}"
      rest="${rest%%/*}"
      case "$rest" in
        *:*) host="${rest%:*}"; port="${rest##*:}" ;;
        *)   host="$rest"
             case "$addr" in wss://*) port=443 ;; *) port=80 ;; esac ;;
      esac
      ;;
    *://*)
      die "--master 的协议只能是 wss://（或本地调试用 ws://），实际是 ${addr%%://*}://"
      ;;
    *)
      host="${addr%:*}"
      port="${addr##*:}"
      [ "${host}" != "$addr" ] || die "--master 要写成 host:port（如 ec.example.com:9000），或 wss://host（隧道走 443）"
      ;;
  esac

  log "先确认连得上主控：${host}:${port}"
  if command -v nc >/dev/null 2>&1; then
    nc -z -w 5 "${host}" "${port}" 2>/dev/null && { log "  可连"; return 0; }
  elif command -v timeout >/dev/null 2>&1; then
    timeout 5 bash -c "exec 3<>/dev/tcp/${host}/${port}" 2>/dev/null && { log "  可连"; return 0; }
  else
    log "  跳过（没有 nc，也没有 timeout）—— 连不上的话装完节点不会出现在控制台里"
    return 0
  fi

  # **变量一律写 ${...}**：`$port。` 里那个中文句号会被 bash 当成变量名的一部分
  # （多字节字节 > 0x7F），报的是 `port?: unbound variable` —— 而这条报错出现在
  # 一段本身就在处理「连不上」的代码里，很容易被读成网络问题。
  die "连不上 ${host}:${port} —— 这次安装无论如何不会工作，所以在动这台机器之前停下。

可能的原因，按撞见的频率排：

  1) **那个域名被 CDN 代理了**（Cloudflare 橙云等）。CDN 只转发 80/443 那几个端口，
     9000 不在里面 —— 节点连的是 CDN，不是你的主控。
     主控的 EC_ADVERTISE 要指向一个**直连源站**的域名（Cloudflare 里是灰云 / DNS only），
     跟控制台那个域名可以不是同一个。
  2) 主控那台机器的防火墙 / 安全组没放行 ${port}。
  3) 主控的 EC_GRPC_ADDR 只监听了回环（应当是 0.0.0.0:${port}）。

在主控那台机器上验：  ss -lntp | grep ${port}
从这台机器上验：      nc -vz ${host} ${port}"
}

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
  preflight_master "$master"

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

  log "建专用系统用户 $AGENT_USER"
  agent_user_plan "$family" | while IFS= read -r cmd; do
    [ -z "$cmd" ] && continue
    log "  $cmd"
    eval "$cmd"
  done

  install -d -m 0700 "$AGENT_HOME"
  # **存量节点的状态目录是 root:root 0700**（Agent 以前跑在 root 下），
  # 里面躺着隧道客户端证书私钥。StateDirectory= 对一个**已存在**的目录
  # 会不会重设属主，我不打算靠记忆断言——显式改一次，代价是零。
  chown -R "$AGENT_USER":"$AGENT_USER" "$AGENT_HOME"

  log "写入配置与单元"
  agent_env "$master" "$node_id" "$token" "$ca_pin" > "$AGENT_ENV"
  # **0640 root:edge-agent，不是 0600 root:root。**
  #
  # 里面有一次性 Token，所以其他人仍然不可读；但降权之后 Agent 得读得到它。
  # 留成 0600 的话进程起不来，报的是「缺少 EC_ENROLL_TOKEN」——
  # 看起来像 Token 没写进去，人会去查接入流程，而那儿没有问题。
  chown root:"$AGENT_USER" "$AGENT_ENV"
  chmod 0640 "$AGENT_ENV"
  agent_unit > "$AGENT_UNIT"
  install -d -m 0755 "$CADDY_DROPIN_DIR"
  caddy_admin_dropin > "$CADDY_DROPIN_DIR/admin-loopback.conf"

  # 日志目录必须在这里建：主控下发的配置把日志写到 $CADDY_LOG_DIR
  # （file writer，roll_size/roll_keep 轮转），而 Caddy 以 caddy 用户跑，
  # 自己在 /var/log 底下建目录会被拒——那时的症状是**整份配置被拒绝、
  # 下发失败**，跟「少建了一个目录」毫无表面关联。
  install -d -o caddy -g caddy -m 0755 "$CADDY_LOG_DIR"

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
  ss_out="$(mktemp)"
  # **单引号会让 $ss_out 留到退出那一刻才求值，而那时它早出了作用域。**
  #
  # 它是 local 的，函数一返回就没了；EXIT trap 在脚本退出时才跑，
  # 于是 set -u 报 `ss_out: unbound variable` —— 而这句话出现在
  # **六条检查全部 ✓ 之后**，退出码也跟着变成非零。
  #
  # 后果不只是难看：任何按 verify 的退出码判断的地方，
  # 都会把一台完全健康的节点当成失败。
  #
  # 双引号在这里是承重的：路径在**设置 trap 的这一刻**就展开进去了。
  trap "rm -f '$ss_out'" EXIT
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

  # 查的是「caddy 用户写得进去」，不是「目录存在」——root 建了目录但没
  # chown 的话，目录在、日志照样一个字节都写不出来，而且下一次下发会被
  # file writer 打开失败整份拒绝。
  if sudo -u caddy test -w "$CADDY_LOG_DIR" 2>/dev/null; then
    printf '  ✓ 日志目录 %s 可被 caddy 用户写入\n' "$CADDY_LOG_DIR"
  else
    printf '  ✘ 日志目录 %s 不存在或 caddy 用户写不进去 —— 下发会整份被拒\n' "$CADDY_LOG_DIR"
    rc=1
  fi

  # **0640 而不是 0600**：Agent 降权之后要读得到它（组是 edge-agent）。
  # 判据放宽到「其他人不可读」，因为 0600 与 0640 都满足那一条，
  # 而真正要防的是最后那一位不为 0。
  local mode
  mode="$(stat -c '%a' "$AGENT_ENV" 2>/dev/null || stat -f '%Lp' "$AGENT_ENV" 2>/dev/null || echo '?')"
  case "$mode" in
    600|640)
      printf '  ✓ %s 权限 %s（其他人不可读）\n' "$AGENT_ENV" "$mode"
      ;;
    *)
      printf '  ✘ %s 权限是 %s，里面有接入 Token\n' "$AGENT_ENV" "$mode"
      rc=1
      ;;
  esac

  # **Agent 到底跑在谁身上。**
  #
  # 单元文件写着 User=edge-agent 不等于进程真的降权了：单元可能是旧的
  #（升级时忘了 daemon-reload），也可能有人手动改回去过。所以查的是
  # 运行中的进程，不是那份文件。
  local runas
  runas="$(systemctl show -p User --value edge-agent 2>/dev/null || echo '?')"
  if [ "$runas" = "$AGENT_USER" ]; then
    printf '  ✓ Agent 跑在 %s 下，不是 root\n' "$AGENT_USER"
  else
    printf '  ✘ Agent 跑在 [%s] 下 —— 期望 %s。单元可能是旧的（试 daemon-reload）\n' \
      "$runas" "$AGENT_USER"
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

# do_update 换掉 edge-agent 二进制并重启。
#
# **不碰配置、不碰状态目录、不碰 Caddy。** 更新只该换那一个文件 ——
# 顺手「重新写一遍配置」的更新流程，会在某次改动之后悄悄覆盖掉人手工调过的东西。
#
# 接入 Token 是一次性的，装好之后 /var/lib/edge-agent 里已经是隧道证书，
# 所以更新**不需要也不该**要 --token。要 Token 的更新流程等于每次都重新接入，
# 而那会在主控那边留下一台「换了身份」的机器。
do_update() {
  local agent_src="" 
  while [ $# -gt 0 ]; do
    case "$1" in
      --agent-bin) agent_src="${2:-}"; shift 2 ;;
      *) die "update 只认 --agent-bin，不认 $1" ;;
    esac
  done
  [ -n "$agent_src" ] || die "要 --agent-bin <新二进制的路径>"
  [ -f "$agent_src" ] || die "$agent_src 不存在"
  [ -x "$AGENT_BIN" ] || die "$AGENT_BIN 不在 —— 这台机器还没装过，用 install"

  # **新旧不是同一个文件才继续。** 拿同一个文件当「新版」是最常见的一种
  # 「更新了但什么也没变」——而它之后的一切看起来都正常。
  if cmp -s "$agent_src" "$AGENT_BIN"; then
    log "新二进制与当前的完全相同，什么也没做"
    return 0
  fi

  local backup="${AGENT_BIN}.prev"
  log "备份当前二进制到 $backup"
  cp -p "$AGENT_BIN" "$backup"

  log "换上新的"
  install -m 0755 "$agent_src" "$AGENT_BIN"

  log "重启 edge-agent"
  systemctl restart edge-agent

  if wait_tunnel_up; then
    log "更新完成。旧的留在 $backup —— 确认没问题之后可以删掉。"
    return 0
  fi

  # **自动回滚。** 一台连不上主控的节点是收不到任何配置的，
  # 包括「把它换回去」那条 —— 所以这一步不能等人来做。
  log "30 秒内没有看到隧道连上，回滚到旧版本"
  install -m 0755 "$backup" "$AGENT_BIN"
  systemctl restart edge-agent
  die "新版本没能连上主控，已回滚。看 journalctl -u edge-agent -n 50"
}

# wait_tunnel_up 等隧道真的连上，最多 30 秒。连上返回 0，超时返回 1。
#
# **起来了不等于连上了。** 进程活着而隧道连不上时，节点在控制台上是离线的
# ——而那正是改动最容易出的那种问题（版本对不上、证书路径变了、
# 降权之后读不到 EnvironmentFile）。所以等的是「隧道通了」，不是「进程还在」。
#
# update 与 harden 共用这一份：两者都在改一台**正在服务**的机器，
# 判据分成两份的话，迟早有一份会停在「进程还在」上。
wait_tunnel_up() {
  local i=0
  while [ $i -lt 30 ]; do
    if systemctl is-active --quiet edge-agent && \
       journalctl -u edge-agent --since "-30s" 2>/dev/null | grep -q "接入完成\|隧道已重连\|已连接主控"; then
      return 0
    fi
    sleep 1
    i=$((i + 1))
  done
  return 1
}

# do_harden 把一台**已经在跑**的节点降权到专用系统用户。
#
# # 为什么需要一个单独的子命令
#
# install 要 --token，而 Token 是一次性的、这台机器早就用掉了；
# update 只换二进制、不碰单元文件。于是存量节点没有任何路径能拿到降权后的
# 单元 —— **降权这件事对它们会是死的**，而没有任何地方会说出来。
#
# 它是幂等的：建用户先判存在，chown 与重写单元重复做没有副作用。
# 已经降过权的机器上再跑一次，代价只是一次重启。
do_harden() {
  need_root
  [ -x "$AGENT_BIN" ] || die "$AGENT_BIN 不在 —— 这台机器还没装过，用 install"
  [ -f "$AGENT_UNIT" ] || die "$AGENT_UNIT 不在 —— 这台机器不是本脚本装的，不动它"

  local family
  family="$(os_family /etc/os-release)" || die "认不出这个发行版，不知道怎么建系统用户"

  log "建专用系统用户 $AGENT_USER"
  agent_user_plan "$family" | while IFS= read -r cmd; do
    [ -z "$cmd" ] && continue
    log "  $cmd"
    eval "$cmd"
  done

  log "把状态目录与配置文件交给 $AGENT_USER"
  # 里面躺着隧道客户端证书私钥，存量节点上它是 root:root 0700。
  chown -R "$AGENT_USER":"$AGENT_USER" "$AGENT_HOME"
  # 0640 而不是 0600：降权之后 Agent 得读得到它，而其他人仍然不可读。
  chown root:"$AGENT_USER" "$AGENT_ENV"
  chmod 0640 "$AGENT_ENV"

  # **先备份单元。** 降权起不来那一刻，受保护域名整体 502
  #（ADR-0003 的 fail-closed）—— 这时候没有时间去手写一份单元回去。
  local backup="${AGENT_UNIT}.prev"
  log "备份当前单元到 $backup"
  cp -p "$AGENT_UNIT" "$backup"

  log "写入降权后的单元并重启"
  agent_unit > "$AGENT_UNIT"
  systemctl daemon-reload
  systemctl restart edge-agent

  if wait_tunnel_up; then
    log "降权完成。旧单元留在 $backup —— 确认没问题之后可以删掉。"
    log "跑 '$0 verify' 查一遍。"
    return 0
  fi

  # **自动回滚，理由与 update 那处相同**：一台连不上主控的节点收不到
  # 任何配置，包括「把它换回去」那条。
  log "30 秒内没有看到隧道连上，把单元换回去"
  install -m 0644 "$backup" "$AGENT_UNIT"
  systemctl daemon-reload
  systemctl restart edge-agent
  die "降权之后没能连上主控，已回滚到原来的单元。
看 journalctl -u edge-agent -n 50 —— 最可能的两种：
  1) $AGENT_ENV 读不到（权限或属主没改成 root:$AGENT_USER 0640）
  2) $AGENT_HOME 里的隧道证书还是 root 的（chown -R 没跑到）"
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
  $0 install --master <wss://host 或 host:port> --node-id <id> --token <一次性> --ca-pin <sha256>
             [--agent-bin <路径>] [--http3]
  $0 update --agent-bin <新二进制的路径>
  $0 harden
  $0 verify
  $0 uninstall

--ca-pin 是主控隧道 CA 证书的 SHA-256，控制台「添加节点」时一并给出。
**它不能省也不能改**：接入首连时本机还没有 CA，指纹是确认对面就是你的主控的
唯一依据（ADR-0009）。

update **只换二进制**，不碰配置、不碰状态目录、不碰 Caddy，也不需要 Token
（这台机器已经有隧道证书了）。换完等隧道连上；30 秒内没连上就**自动回滚**
——一台连不上主控的节点收不到任何配置，包括「把它换回去」那条。

harden 把一台**已经在跑**的节点降权到专用系统用户 edge-agent（早期版本装的
节点跑在 root 下）。它不需要 Token，可以重复跑。**会重启一次 Agent**——
受保护域名在那几秒里整体 502（ADR-0003 的 fail-closed），所以多节点要一台一台来。
降权之后连不上主控会自动把单元换回去。

新装的节点不需要 harden：install 直接就是降权后的单元。

本脚本**不下载** edge-agent 二进制。用 --agent-bin 指向本机已有的文件。
USAGE
}

main() {
  case "${1:-}" in
    install)   shift; do_install "$@" ;;
    update)    shift; do_update "$@" ;;
    harden)    shift; do_harden ;;
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
