#!/usr/bin/env bash
#
# 部署脚本的测试。
#
# 它跑在开发机上（macOS 也行），只验**决策逻辑**：发行版探测、监听地址判定、
# 单元文件与防火墙规则的生成。这些是脚本里真正会写错、又不需要 Linux 的部分。
#
# **「装上去能起来」这件事这里验不了**，需要一台真机。不假装验过——
# 一份「全部通过」而其中几条其实没验的报告，比没有这份报告更糟。
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./edge-node.sh
source "$HERE/edge-node.sh"
# edge-node.sh 顶上有 set -e（脚本自己需要）。测试要故意走失败路径，
# 因此 source 之后立刻关掉——否则第一条「本该失败」的断言会把整个测试中断。
set +e

PASS=0; FAIL=0
ok()   { PASS=$((PASS+1)); printf '  ✓ %s\n' "$1"; }
bad()  { FAIL=$((FAIL+1)); printf '  ✘ %s\n' "$1"; [ $# -gt 1 ] && printf '      %s\n' "$2"; return 0; }
eq()   { [ "$1" = "$2" ] && ok "$3" || bad "$3" "期望 [$1]，实际 [$2]"; }
has()  { case "$1" in *"$2"*) ok "$3" ;; *) bad "$3" "找不到 [$2]" ;; esac; }
hasnt(){ case "$1" in *"$2"*) bad "$3" "不该出现 [$2]" ;; *) ok "$3" ;; esac; }
# no_directive 断言某条 systemd 指令**没有**出现。
#
# 不能用 hasnt：单元文件里有一句解释「为什么不用 Requires=caddy.service」的注释，
# 而整段搜字符串分不出「指令」和「关于该指令的注释」——那会把说明看成违规。
# 只匹配行首（允许前导空白）的指令。
no_directive(){ 
  if printf '%s\n' "$1" | grep -qE "^[[:space:]]*$2"; then
    bad "$3" "单元里出现了指令 [$2]"
  else
    ok "$3"
  fi
}

fixture() { printf '%s/testdata/%s' "$HERE" "$1"; }

printf '\n发行版探测\n'
eq debian "$(os_family "$(fixture os-release.debian12)")" "Debian 12"
eq debian "$(os_family "$(fixture os-release.ubuntu2204)")" "Ubuntu 22.04"
eq alpine "$(os_family "$(fixture os-release.alpine)")" "Alpine"
eq arch   "$(os_family "$(fixture os-release.arch)")" "Arch"
# Rocky / Alma 的 ID 认不出来，靠 ID_LIKE 里的 rhel。只看 ID 就要把每个
# RHEL 衍生版单独列一遍，而它们的包管理是一样的。
eq rhel   "$(os_family "$(fixture os-release.rocky9)")" "Rocky 9（走 ID_LIKE）"
eq rhel   "$(os_family "$(fixture os-release.alma9)")" "AlmaLinux 9（走 ID_LIKE）"

printf '\n认不出的发行版要失败，不能猜\n'
tmp="$(mktemp)"; printf 'ID=plan9\n' > "$tmp"
out="$(os_family "$tmp")"; rc=$?
eq unknown "$out" "认不出时返回 unknown"
[ "$rc" -ne 0 ] && ok "并且以非零退出" || bad "认不出时应当非零退出"
caddy_install_plan unknown >/dev/null 2>&1
[ $? -ne 0 ] && ok "安装计划对未知家族失败，不去猜一个 curl | bash" \
             || bad "未知家族不该给出安装计划"
rm -f "$tmp"

printf '\n监听地址判定（这是脚本里最要紧的一处）\n'
port_is_loopback_only "$(fixture ss-loopback-only.txt)" 2019 2>/dev/null
eq 0 "$?" "只在 127.0.0.1 上监听 → 通过"
port_is_loopback_only "$(fixture ss-v6-loopback.txt)" 2019 2>/dev/null
eq 0 "$?" "只在 ::1 上监听 → 通过"
port_is_loopback_only "$(fixture ss-admin-exposed.txt)" 2019 2>/dev/null
eq 1 "$?" "监听 0.0.0.0 → 拒绝"
port_is_loopback_only "$(fixture ss-admin-lan-ip.txt)" 2019 2>/dev/null
eq 1 "$?" "监听内网 IP → 拒绝（内网不等于回环）"
port_is_loopback_only "$(fixture ss-admin-wildcard-v6.txt)" 2019 2>/dev/null
eq 1 "$?" "监听 :: → 拒绝"
# 「没在监听」与「监听错地方」要分开：前者是 Caddy 没起来，后者是私钥暴露。
# 两种的处置完全不同，合成一个返回值就分不出来了。
port_is_loopback_only "$(fixture ss-not-listening.txt)" 2019 2>/dev/null
eq 2 "$?" "没在监听 → 与「暴露」区分开的另一个返回值"

printf '\n端口号不能被后缀匹配蒙混\n'
# :12019 结尾是 2019，但它不是 2019。夹具里没有这种情况，
# 所以自己造一个——这类错在真机上表现为「查过了，通过了」，而其实查的是别的端口。
tmp="$(mktemp)"
cat > "$tmp" <<'SS'
State  Recv-Q Send-Q Local Address:Port  Peer Address:Port
LISTEN 0      4096         0.0.0.0:12019       0.0.0.0:*
SS
port_is_loopback_only "$tmp" 2019 2>/dev/null
eq 2 "$?" "0.0.0.0:12019 不该被当成 2019"
rm -f "$tmp"

printf '\nsystemd 单元里那几条承重的约束\n'
unit="$(agent_unit)"
has "$unit" "Restart=always" "Restart=always（ADR-0003：fail-closed 依赖 Agent 存活）"
has "$unit" "EnvironmentFile=/etc/edge-agent.env" "凭据走 EnvironmentFile"
hasnt "$unit" "EC_ENROLL_TOKEN" "Token 不出现在单元里（ExecStart 的参数会进 ps）"
no_directive "$unit" "Requires=" "不 Requires caddy —— Caddy 挂了 Agent 要活着把这件事报上去"
has "$unit" "ProtectSystem=strict" "沙箱：节点被打穿后唯一还在的那道墙"
has "$unit" "ReadWritePaths=/var/lib/edge-agent" "状态目录可写"

printf '\nEnvironmentFile 的内容\n'
env_out="$(agent_env ec.internal:9000 node-hk-01 ec_tok 9e8f22a3)"
has "$env_out" "EC_ENROLL_TOKEN=ec_tok" "带一次性 Token"
has "$env_out" "EC_CA_PIN=9e8f22a3" "带 CA 指纹 —— 少了它接入退化成 TOFU"
has "$env_out" "EC_CADDY_ADMIN=http://127.0.0.1:2019" "Caddy Admin 指回环"
has "$env_out" "EC_VERIFY_LISTEN=127.0.0.1:2020" "校验端点绑回环"

printf '\n防火墙规则\n'
fw="$(firewall_plan debian no)"
has "$fw" "80/tcp" "放行 80"
has "$fw" "443/tcp" "放行 443"
hasnt "$fw" "443/udp" "没开 HTTP/3 时不放行 443/udp（白送一个攻击面）"
hasnt "$fw" "2019" "绝不放行 Caddy Admin"
hasnt "$fw" "2020" "绝不放行校验端点"
fw3="$(firewall_plan debian yes)"
has "$fw3" "443/udp" "开了 HTTP/3 才放行 443/udp"

printf '\nCaddy Admin 钉在回环\n'
has "$(caddy_admin_dropin)" "CADDY_ADMIN=127.0.0.1:2019" "drop-in 显式钉死 Admin 地址"

printf '\n装机前的连通性检查\n'
#
# 这一条守的是**「在动这台机器之前先停下」**。
#
# 少了它，装机会一路成功——装 Caddy、写单元、起 Agent——然后节点永远不
# 出现在控制台里，而这台机器上没有任何东西说得出为什么。灰度上真实撞到过：
# 主控的域名被 Cloudflare 代理，CDN 只转发 80/443，9000 根本到不了主控。
#
# **不能用「装完再看有没有上线」代替**：那时错误已经散落在三个地方
#（Agent 日志、systemd 状态、控制台的空列表），而没有一处说得出真因。

# 端口格式错要当场拒，而不是拿一个空 port 去连。
out="$( (preflight_master "ec.example.com") 2>&1 )"
has "$out" "host:port" "--master 没写端口时点名格式"

# 连不上时必须**拒绝继续**，并且报错里要给出可查的方向。
# 用 127.0.0.1 上一个几乎不可能有人监听的端口。
out="$( (preflight_master "127.0.0.1:59321") 2>&1 )"
has "$out" "连不上" "连不上时拒绝安装"
has "$out" "CDN" "报错点出「域名被 CDN 代理」这个最常见的成因"
has "$out" "ss -lntp" "报错给出在主控那边怎么验"
has "$out" "nc -vz" "报错给出在节点这边怎么验"

# **反向：连得上就不该拦。** 没有这一条，上面四条在
#「preflight 无条件失败」时也全绿——而那会让每一次安装都装不了。
python3 -c 'import socket,sys,time,threading
s=socket.socket(); s.bind(("127.0.0.1",0)); s.listen(1)
print(s.getsockname()[1]); sys.stdout.flush()
time.sleep(6)' > /tmp/ec-preflight-port.txt &
sleep 1
listen_port="$(head -1 /tmp/ec-preflight-port.txt 2>/dev/null)"
if [ -n "$listen_port" ]; then
  out="$( (preflight_master "127.0.0.1:$listen_port") 2>&1 )"
  hasnt "$out" "连不上" "连得上时放行（否则上面几条在「无条件失败」时也全绿）"
else
  bad "连得上时放行" "装置坏了：没能起一个临时监听端口"
fi
wait 2>/dev/null

# **wss:// 那种写法也要能解析出主机和端口。**
#
# 用 ${addr%:*} 那一套的话，`wss://cdn.example.com` 会被切成 host="wss"，
# 然后拿着 "wss" 去连 —— 报的是「域名解析失败」，而人会去查 DNS，
# 那儿没有问题。
out="$( (preflight_master "wss://127.0.0.1:59322") 2>&1 )"
has "$out" "127.0.0.1:59322" "wss://host:port 解析出主机和端口"
hasnt "$out" "wss:" "主机名里不该混进协议头"

# 不带端口时补默认值：wss → 443，ws → 80。
out="$( (preflight_master "wss://cdn.invalid.example") 2>&1 )"
has "$out" "cdn.invalid.example:443" "wss:// 不带端口时补 443"

# 带路径也要能剥掉（安装命令里可能带 /api/v1/tunnel）。
out="$( (preflight_master "wss://cdn.invalid.example/api/v1/tunnel") 2>&1 )"
has "$out" "cdn.invalid.example:443" "wss:// 带路径时只取主机和端口"
hasnt "$out" "/api" "路径不该混进主机名"

# 别的协议要当场拒，而不是猜。
out="$( (preflight_master "https://cdn.example.com") 2>&1 )"
has "$out" "只能是 wss" "https:// 被明确拒绝，而不是悄悄当成 wss"

printf '\n──────────\n通过 %d，失败 %d\n\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
